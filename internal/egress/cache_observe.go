package egress

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/home-operations/kritika/internal/metrics"
)

const (
	cacheCanceled = "canceled"
	cacheMiss     = "miss"
)

// Observe enables process-local cache telemetry. No bucket requests are made.
func (c *Cache) Observe(m *metrics.Metrics, logger *slog.Logger) {
	c.metrics, c.logger = m, logger
	dir, err := os.ReadDir(c.Dir)
	if err != nil {
		c.warn("local_scan", "", err)
		return
	}
	var size int64
	for _, d := range dir {
		if !strings.HasPrefix(d.Name(), ".") {
			if info, err := d.Info(); err == nil {
				size += info.Size()
			}
		}
	}
	c.metrics.CacheLocal(size, c.MaxBytes, 0)
}

// One warning per minute per replica bounds logs during a storage outage;
// operation metrics still record every failure.
func (c *Cache) warn(operation, key string, err error) {
	if c.logger == nil || errors.Is(err, context.Canceled) {
		return
	}
	now := time.Now().Unix()
	previous := c.lastWarning.Load()
	if now-previous >= 60 && c.lastWarning.CompareAndSwap(previous, now) {
		c.logger.Warn("egress cache storage error", "operation", operation, "cache_key", key, "error", err,
			"warning_interval", "1m")
	}
}

func operationResult(err error) string {
	if errors.Is(err, context.Canceled) {
		return cacheCanceled
	}
	if err != nil {
		return OutcomeError
	}
	return "ok"
}

type cacheBody struct {
	mu     sync.Mutex
	closed bool
	io.ReadSeekCloser
	cache   *Cache
	key     string
	source  string
	started time.Time
	bytes   int64
	err     error
	finish  func(error)
}

func (b *cacheBody) Read(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return 0, os.ErrClosed
	}
	if b.started.IsZero() {
		b.started = time.Now()
	}
	n, err := b.ReadSeekCloser.Read(p)
	b.bytes += int64(n)
	if err != nil && !errors.Is(err, io.EOF) {
		b.err = err
	}
	return n, err
}

func (b *cacheBody) Seek(offset int64, whence int) (int64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return 0, os.ErrClosed
	}
	n, err := b.ReadSeekCloser.Seek(offset, whence)
	if err != nil {
		b.err = err
	}
	return n, err
}

func (b *cacheBody) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	b.closed = true
	err := b.ReadSeekCloser.Close()
	if b.err == nil {
		b.err = err
	}
	// An object that went, or was replaced, while being read is a miss,
	// not a storage failure: the bucket answered, with the revision gone.
	gone := s3Gone(b.err)
	if b.finish != nil {
		if gone {
			b.finish(nil)
		} else {
			b.finish(b.err)
		}
	}
	if b.source == "s3" && (!b.started.IsZero() || b.err != nil) {
		var took time.Duration
		if !b.started.IsZero() {
			took = time.Since(b.started)
		}
		result := operationResult(b.err)
		if gone {
			result = cacheMiss
		}
		b.cache.metrics.CacheOperation("s3_read", result, took, b.bytes)
		if b.err != nil && !gone {
			b.cache.warn("s3_read", b.key, b.err)
		}
	}
	return err
}

type cacheResponse struct {
	http.ResponseWriter
	status int
	bytes  int64
	err    error
}

func (w *cacheResponse) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *cacheResponse) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *cacheResponse) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(p)
	w.bytes += int64(n)
	if err != nil {
		w.err = err
	}
	return n, err
}

func (p *Proxy) serveCached(w http.ResponseWriter, r *http.Request, key string, f *cacheBody, modified time.Time) {
	started := time.Now()
	out := &cacheResponse{ResponseWriter: w}
	http.ServeContent(out, r, "", modified, f)
	f.mu.Lock()
	if r.Method != http.MethodHead && out.status != http.StatusNotModified && out.err == nil && r.Context().Err() == nil {
		if expected, err := strconv.ParseInt(out.Header().Get("Content-Length"), 10, 64); err == nil && out.bytes < expected && f.err == nil {
			f.err = io.ErrUnexpectedEOF
		}
	}
	result := operationResult(errors.Join(f.err, out.err))
	f.mu.Unlock()
	if out.status >= 500 {
		result = OutcomeError
	}
	if r.Context().Err() != nil {
		result = cacheCanceled
	}
	p.Cache.metrics.CacheResponse(f.source, result, out.bytes)
	p.Logger.Debug("egress cache download", "cache_key", key, "source", f.source, "result", result,
		"status", out.status, "method", r.Method, "bytes", out.bytes, "elapsed", time.Since(started).Round(time.Millisecond))
}

type countedReader struct {
	io.Reader
	bytes int64
}

func (r *countedReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.bytes += int64(n)
	return n, err
}

// Count the HTTP body rather than SDK reads, which also include checksum passes.
type cacheTransport struct {
	base  http.RoundTripper
	cache *Cache
}

func (t *cacheTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	ctx, cancel := context.WithCancelCause(r.Context())
	r = r.Clone(ctx)
	if r.Method == http.MethodPut && r.Body != nil {
		body := &uploadReader{Reader: r.Body, metrics: t.cache.metrics}
		r.Body = struct {
			io.Reader
			io.Closer
		}{body, r.Body}
	}
	resp, err := t.base.RoundTrip(r)
	if err != nil {
		cancel(context.Canceled)
		return nil, err
	}
	resp.Body = &s3ResponseBody{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
}

type uploadReader struct {
	io.Reader
	metrics *metrics.Metrics
}

func (r *uploadReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.metrics.CacheTransfer("s3_upload", int64(n))
	return n, err
}

func fillBypassReason(err error) string {
	for _, reason := range []struct {
		err   error
		label string
	}{
		{errFillBusy, "fill_busy"}, {errFillCapacity, "fill_capacity"}, {errEntrySize, "too_large"},
		{errPrivate, "private"}, {errEncoded, "encoded"}, {errLength, "unknown_length"}, {context.Canceled, cacheCanceled},
	} {
		if errors.Is(err, reason.err) {
			return reason.label
		}
	}
	return "fill_error"
}
