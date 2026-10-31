package egress

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/minio/minio-go/v7/pkg/credentials"
	"golang.org/x/sync/errgroup"
)

const (
	s3LookupTimeout = 2 * time.Second
	s3ReadTimeout   = 10 * time.Second
	s3UploadTimeout = 30 * time.Second
	// s3UploadRate is the slowest upload allowed past the fixed budget: one
	// second per MiB. So a 256Mi entry has about five minutes and the 5Gi
	// cap about ninety, instead of a deadline that no large entry can meet.
	s3UploadRate    = 1 << 20
	s3Cooldown      = 30 * time.Second
	s3Failures      = 3
	s3UploadWorkers = 2
	s3UploadQueue   = 16
)

var errS3CircuitOpen = errors.New("egress: cache: S3 circuit is open")

// uploadDeadline is the fixed budget plus what size bytes take at s3UploadRate.
func uploadDeadline(size int64) time.Duration {
	return s3UploadTimeout + time.Duration(size/s3UploadRate)*time.Second
}

// The SDK detaches credential refresh from request cancellation and holds its
// credential mutex during retrieval, so discovery needs its own total budget.
type s3Credentials struct {
	credentials.Provider
}

func (p *s3Credentials) RetrieveWithCredContext(cc *credentials.CredContext) (credentials.Value, error) {
	bounded := *cc
	// The SDK's default credential context carries no Context at all.
	parent := cc.Context
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, s3LookupTimeout)
	defer cancel()
	bounded.Context = ctx
	return p.Provider.RetrieveWithCredContext(&bounded)
}

// Read and write circuits are independent: denied uploads must not suppress
// usable shared entries. Generations fence completions from before an outage.
type s3Circuit struct {
	mu         sync.Mutex
	generation uint64
	failures   int
	retryAt    time.Time
	probing    bool
}

type s3Attempt struct {
	generation uint64
	probe      bool
}

func (b *s3Circuit) allow(now time.Time) (s3Attempt, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	a := s3Attempt{generation: b.generation}
	if b.retryAt.IsZero() {
		return a, true
	}
	if now.Before(b.retryAt) || b.probing {
		return a, false
	}
	b.probing, a.probe = true, true
	return a, true
}

func (b *s3Circuit) complete(a s3Attempt, err error, now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if a.generation != b.generation {
		return
	}
	if errors.Is(err, context.Canceled) {
		if a.probe {
			b.probing = false
		}
		return
	}
	if err == nil {
		b.failures = 0
		if a.probe {
			b.retryAt, b.probing = time.Time{}, false
			b.generation++
		}
		return
	}
	b.failures++
	if a.probe || b.failures >= s3Failures {
		b.retryAt, b.probing = now.Add(s3Cooldown), false
		b.generation++
	}
}

// RunUploads owns the background workers. Cancellation drops queued keys and
// cancels active uploads. Uploads never extend gateway shutdown.
func (c *Cache) RunUploads(ctx context.Context) error {
	g, ctx := errgroup.WithContext(ctx)
	for range s3UploadWorkers {
		g.Go(func() error {
			for {
				select {
				case <-ctx.Done():
					return nil
				case key := <-c.s3.uploads:
					if ctx.Err() != nil {
						return nil
					}
					// upload records storage errors. Publication is best effort.
					_ = c.upload(ctx, key)
				}
			}
		})
	}
	return g.Wait()
}

func (c *Cache) queueUpload(key string) {
	select {
	case c.s3.uploads <- key:
	default:
		c.metrics.CacheUploadSkipped("queue_full")
	}
}

// The idle budget runs only while reading S3, not while a slow downstream
// client is consuming bytes already received. Close cancels the request too.
type s3ResponseBody struct {
	io.ReadCloser
	cancel context.CancelCauseFunc
}

func (b *s3ResponseBody) Read(p []byte) (int, error) {
	done := make(chan struct{})
	timer := time.AfterFunc(s3ReadTimeout, func() {
		b.cancel(context.DeadlineExceeded)
		close(done)
	})
	n, err := b.ReadCloser.Read(p)
	if !timer.Stop() {
		<-done
		return n, context.DeadlineExceeded
	}
	return n, err
}

func (b *s3ResponseBody) Close() error {
	b.cancel(context.Canceled)
	return b.ReadCloser.Close()
}
