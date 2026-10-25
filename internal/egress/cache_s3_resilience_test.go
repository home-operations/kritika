package egress

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/home-operations/kritika/internal/egresspolicy"
	"github.com/home-operations/kritika/internal/metrics"
)

func faultS3Cache(t *testing.T, transport http.RoundTripper) *Cache {
	t.Helper()
	c := &Cache{Dir: t.TempDir(), MaxEntryBytes: 1 << 20}
	opts := S3Options{Endpoint: "https://s3.example", Region: "test", Bucket: "cache", AccessKey: "key", SecretKey: "secret"}
	if err := c.UseS3(opts); err != nil {
		t.Fatal(err)
	}
	client, err := minio.New("s3.example", &minio.Options{
		Creds: credentials.NewStaticV4("key", "secret", ""), Region: "test", Secure: true,
		BucketLookup: minio.BucketLookupPath, MaxRetries: 1, Transport: &cacheTransport{base: transport, cache: c},
	})
	if err != nil {
		t.Fatal(err)
	}
	c.s3.client = client
	return c
}

func s3Reply(r *http.Request, status int, code string) *http.Response {
	body := ""
	if code != "" {
		body = "<Error><Code>" + code + "</Code></Error>"
	}
	return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: r}
}

type stalledS3Credentials struct {
	credentials.Static
}

func (*stalledS3Credentials) RetrieveWithCredContext(cc *credentials.CredContext) (credentials.Value, error) {
	<-cc.Context.Done()
	return credentials.Value{}, cc.Context.Err()
}

func TestS3CredentialDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client, err := minio.New("s3.example", &minio.Options{
			Creds:  credentials.New(&s3Credentials{Provider: &stalledS3Credentials{}}),
			Region: "test", Secure: true, MaxRetries: 1,
		})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), s3LookupTimeout)
		defer cancel()
		started := time.Now()
		_, err = client.StatObject(ctx, "cache", "key", minio.StatObjectOptions{})
		if err == nil || time.Since(started) > s3LookupTimeout {
			t.Errorf("credential discovery: elapsed=%s error=%v", time.Since(started), err)
		}
		// The SDK's own default credential context carries no Context.
		bounded := &s3Credentials{Provider: &stalledS3Credentials{}}
		started = time.Now()
		if _, err := bounded.RetrieveWithCredContext(&credentials.CredContext{}); err == nil || time.Since(started) != s3LookupTimeout {
			t.Errorf("without a context: elapsed=%s error=%v", time.Since(started), err)
		}
	})
}

func TestS3OutageRecovery(t *testing.T) {
	for _, failure := range []string{"dns", "missing bucket", "denied", "server error", "stalled headers"} {
		t.Run(failure, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var requests atomic.Int32
				var healthy atomic.Bool
				c := faultS3Cache(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
					requests.Add(1)
					if healthy.Load() {
						return s3Reply(r, http.StatusNotFound, "NoSuchKey"), nil
					}
					switch failure {
					case "dns":
						return nil, &net.DNSError{Name: "s3.example", Err: "no such host", IsNotFound: true}
					case "missing bucket":
						return s3Reply(r, http.StatusNotFound, "NoSuchBucket"), nil
					case "denied":
						return s3Reply(r, http.StatusForbidden, "AccessDenied"), nil
					case "server error":
						return s3Reply(r, http.StatusServiceUnavailable, "SlowDown"), nil
					default:
						<-r.Context().Done()
						return nil, r.Context().Err()
					}
				}))
				if requests.Load() != 0 {
					t.Fatal("startup contacted S3")
				}
				p := &Proxy{Cache: c, Logger: slog.New(slog.DiscardHandler), Rules: func() egresspolicy.Rules {
					return egresspolicy.Rules{Allow: hosts("example.com")}
				}, Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
					resp := s3Reply(r, http.StatusOK, "")
					resp.Body, resp.ContentLength = io.NopCloser(strings.NewReader("origin")), 6
					return resp, nil
				})}
				// Distinct URLs: a failed lookup fills the local directory,
				// so a repeated URL would be a local hit and not ask S3.
				for i := range s3Failures + 2 {
					started := time.Now()
					w := proxied(p, http.MethodGet, fmt.Sprintf("http://example.com/tool%d", i), nil)
					if w.Code != http.StatusOK || w.Body.String() != "origin" {
						t.Fatalf("fallback: %d %q", w.Code, w.Body.String())
					}
					if elapsed := time.Since(started); elapsed > s3LookupTimeout || i >= s3Failures && elapsed != 0 {
						t.Fatalf("lookup %d took %s", i, elapsed)
					}
				}
				if requests.Load() != s3Failures {
					t.Fatalf("outage requests = %d, want %d", requests.Load(), s3Failures)
				}
				w := proxied(p, http.MethodGet, "http://example.com/tool0", nil)
				if w.Code != http.StatusOK || w.Body.String() != "origin" || requests.Load() != s3Failures {
					t.Fatalf("local fill during outage: %d %q after %d requests", w.Code, w.Body.String(), requests.Load())
				}
				healthy.Store(true)
				time.Sleep(s3Cooldown)
				if _, _, err := c.load(t.Context(), "missing"); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("recovery probe = %v", err)
				}
				if _, _, err := c.load(t.Context(), "another"); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("read after recovery = %v", err)
				}
				if requests.Load() != s3Failures+2 {
					t.Fatalf("recovery requests = %d", requests.Load())
				}
			})
		})
	}
}

func TestS3CircuitProbeAndStaleCompletions(t *testing.T) {
	var b s3Circuit
	now := time.Now()
	stale, _ := b.allow(now)
	for range s3Failures {
		a, _ := b.allow(now)
		b.complete(a, io.ErrUnexpectedEOF, now)
	}
	b.complete(stale, nil, now)
	if _, ok := b.allow(now); ok {
		t.Fatal("old success closed the circuit")
	}
	now = now.Add(s3Cooldown)
	a, ok := b.allow(now)
	if !ok || !a.probe {
		t.Fatal("cooldown did not allow a probe")
	}
	for range 100 {
		if _, ok := b.allow(now); ok {
			t.Fatal("another request joined the recovery probe")
		}
	}
	b.complete(a, context.Canceled, now)
	a, ok = b.allow(now)
	if !ok {
		t.Fatal("canceled probe stranded the circuit")
	}
	b.complete(a, io.ErrUnexpectedEOF, now)
	if _, ok := b.allow(now); ok {
		t.Fatal("failed probe did not restore cooldown")
	}
	now = now.Add(s3Cooldown)
	a, _ = b.allow(now)
	b.complete(a, nil, now)
	b.complete(stale, io.ErrUnexpectedEOF, now)
	if _, ok := b.allow(now); !ok {
		t.Fatal("successful probe did not restore service")
	}
}

type stalledS3Body struct{ ctx context.Context }

func (b stalledS3Body) Read([]byte) (int, error) { <-b.ctx.Done(); return 0, b.ctx.Err() }
func (stalledS3Body) Close() error               { return nil }

func TestS3StalledBody(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entry := cacheEntry{URL: "https://example.com/tool", Size: 3, Stored: time.Now().UnixMilli()}
		raw, _ := json.Marshal(entry)
		c := faultS3Cache(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
			resp := s3Reply(r, http.StatusOK, "")
			resp.Header.Set("Etag", "\"revision\"")
			resp.Header.Set("Content-Length", "3")
			resp.Header.Set("Last-Modified", time.Now().Format(http.TimeFormat))
			resp.Header.Set("X-Amz-Meta-Kritika-Entry", base64.StdEncoding.EncodeToString(raw))
			resp.ContentLength = 3
			if r.Method == http.MethodGet {
				resp.Body = stalledS3Body{ctx: r.Context()}
			}
			return resp, nil
		}))
		for range s3Failures {
			f, _, err := c.load(t.Context(), cacheKey(entry.URL))
			if err != nil {
				t.Fatal(err)
			}
			// Metadata's short deadline must not end a healthy streaming context.
			time.Sleep(s3LookupTimeout + time.Second)
			started := time.Now()
			if _, err := io.ReadAll(f); err == nil || time.Since(started) != s3ReadTimeout {
				t.Fatalf("stalled read: %v after %s", err, time.Since(started))
			}
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}
		}
		if _, _, err := c.load(t.Context(), cacheKey(entry.URL)); !errors.Is(err, errS3CircuitOpen) {
			t.Fatalf("stalled reads did not open circuit: %v", err)
		}
	})
}

func TestS3BackgroundUploads(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var puts atomic.Int32
		c := faultS3Cache(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
			puts.Add(1)
			<-r.Context().Done()
			return nil, r.Context().Err()
		}))
		reg := prometheus.NewPedanticRegistry()
		c.Observe(metrics.New(reg), slog.New(slog.DiscardHandler))
		// One entry per worker: one entry's uploads go one at a time, and
		// synctest cannot wait out a mutex.
		key := cacheKey("https://example.com/tool")
		keys := []string{key}
		for i := 2; len(keys) < s3UploadWorkers; i++ {
			keys = append(keys, cacheKey(fmt.Sprintf("https://example.com/tool%d", i)))
		}
		for i, k := range keys {
			entry := cacheEntry{URL: fmt.Sprintf("https://example.com/tool%d", i), Size: 3, Stored: time.Now().UnixMilli()}
			if err := c.store(k, entry, strings.NewReader("abc")); err != nil {
				t.Fatal(err)
			}
		}
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() { done <- c.RunUploads(ctx) }()
		for _, k := range keys {
			c.queueUpload(k)
		}
		synctest.Wait()
		if puts.Load() != s3UploadWorkers {
			t.Fatalf("active uploads = %d", puts.Load())
		}
		for range s3UploadQueue + 3 {
			c.queueUpload(key)
		}
		if len(c.s3.uploads) != s3UploadQueue || cacheMetric(t, reg, "kritika_cache_upload_skips_total", map[string]string{"reason": "queue_full"}) != 3 {
			t.Fatal("queue was not bounded or skips were not observed")
		}
		f, _, err := c.load(t.Context(), key)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(f)
		if err != nil || string(body) != "abc" {
			t.Fatalf("local download: %q %v", body, err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if puts.Load() != s3UploadWorkers {
			t.Fatal("shutdown started queued uploads")
		}
	})
}

func TestS3UploadDeadlineAndRecovery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var healthy atomic.Bool
		c := faultS3Cache(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if healthy.Load() {
				return s3Reply(r, http.StatusOK, ""), nil
			}
			<-r.Context().Done()
			return nil, r.Context().Err()
		}))
		key := cacheKey("https://example.com/tool")
		entry := cacheEntry{URL: "https://example.com/tool", Size: 3, Stored: time.Now().UnixMilli()}
		if err := c.store(key, entry, strings.NewReader("abc")); err != nil {
			t.Fatal(err)
		}
		for range s3Failures {
			started := time.Now()
			if err := c.upload(t.Context(), key); err == nil || time.Since(started) != s3UploadTimeout {
				t.Fatalf("upload: %v after %s", err, time.Since(started))
			}
		}
		if err := c.upload(t.Context(), key); !errors.Is(err, errS3CircuitOpen) {
			t.Fatalf("upload circuit: %v", err)
		}
		if _, ok := c.s3.reads.allow(time.Now()); !ok {
			t.Fatal("failed uploads disabled reads")
		}
		healthy.Store(true)
		time.Sleep(s3Cooldown)
		if err := c.upload(t.Context(), key); err != nil {
			t.Fatalf("upload recovery: %v", err)
		}
		// A large entry gets a deadline it can meet at s3UploadRate.
		healthy.Store(false)
		large := cacheEntry{URL: "https://example.com/large", Size: 3 << 20, Stored: time.Now().UnixMilli()}
		if err := c.store(cacheKey(large.URL), large, strings.NewReader(strings.Repeat("x", int(large.Size)))); err != nil {
			t.Fatal(err)
		}
		started := time.Now()
		if err := c.upload(t.Context(), cacheKey(large.URL)); err == nil || time.Since(started) != s3UploadTimeout+3*time.Second {
			t.Fatalf("large upload: %v after %s", err, time.Since(started))
		}
	})
}

func TestS3DownloadDoesNotWaitForUpload(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	var store s3TestStore
	store.objects = make(map[string]s3TestObject)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			close(started)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		store.serve(w, r)
	}))
	t.Cleanup(srv.Close)
	p := cacheProxy(t, egresspolicy.Rules{Allow: hosts("example.com")}, 1<<20, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, "tool archive")
	})
	useS3(t, p.Cache, S3Options{Endpoint: srv.URL, Region: "test", Bucket: "cache", AccessKey: "key", SecretKey: "secret"})
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- proxied(p, http.MethodGet, "http://example.com/tool", nil) }()
	select {
	case w := <-done:
		if w.Code != http.StatusOK || w.Body.String() != "tool archive" {
			t.Fatalf("download: %d %q", w.Code, w.Body.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("download waited for the upload")
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("upload did not start")
	}
}
