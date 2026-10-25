package egress

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/home-operations/kritika/internal/egresspolicy"
	"github.com/home-operations/kritika/internal/metrics"
)

func cacheMetric(t *testing.T, reg *prometheus.Registry, name string, labels map[string]string) float64 {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, m := range family.Metric {
			match := len(m.Label) == len(labels)
			for _, l := range m.Label {
				match = match && labels[l.GetName()] == l.GetValue()
			}
			if !match {
				continue
			}
			switch {
			case m.Counter != nil:
				return m.Counter.GetValue()
			case m.Gauge != nil:
				return m.Gauge.GetValue()
			case m.Histogram != nil:
				return float64(m.Histogram.GetSampleCount())
			}
		}
	}
	return 0
}

func TestCacheObservations(t *testing.T) {
	_, opts := newS3TestStore(t)
	reg := prometheus.NewPedanticRegistry()
	m := metrics.New(reg)
	upstream := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = io.WriteString(w, "tool archive")
	}
	p := cacheProxy(t, egresspolicy.Rules{Allow: hosts("example.com")}, 1<<20, upstream)
	useS3(t, p.Cache, opts)
	p.Cache.Observe(m, p.Logger)
	const target = "http://example.com/tool"
	for _, tt := range []struct {
		method  string
		headers http.Header
	}{
		{http.MethodGet, nil}, {http.MethodHead, nil}, {http.MethodGet, http.Header{"Range": {"bytes=5-11"}}},
	} {
		proxied(p, tt.method, target, tt.headers)
	}
	waitS3Upload(t, p.Cache, cacheKey("https://example.com/tool"))
	reader := cacheProxy(t, egresspolicy.Rules{Allow: hosts("example.com")}, 1<<20, upstream)
	useS3(t, reader.Cache, opts)
	reader.Cache.Observe(m, reader.Logger)
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		proxied(reader, method, target, nil)
	}
	for _, tt := range []struct {
		name   string
		labels map[string]string
		want   float64
	}{
		{"kritika_cache_requests_total", map[string]string{"outcome": "miss"}, 1},
		{"kritika_cache_requests_total", map[string]string{"outcome": "local_hit"}, 2},
		{"kritika_cache_requests_total", map[string]string{"outcome": "s3_hit"}, 2},
		{"kritika_cache_transfer_bytes_total", map[string]string{"operation": "origin_fetch"}, 12},
		{"kritika_cache_transfer_bytes_total", map[string]string{"operation": "s3_upload"}, 12},
		{"kritika_cache_transfer_bytes_total", map[string]string{"operation": "response_local"}, 19},
		{"kritika_cache_transfer_bytes_total", map[string]string{"operation": "response_s3"}, 12},
		{"kritika_cache_transfer_bytes_total", map[string]string{"operation": "s3_read"}, 12},
		{"kritika_cache_operation_seconds", map[string]string{"operation": "s3_read", "outcome": "ok"}, 1},
		{"kritika_cache_active_fills", nil, 0},
	} {
		if got := cacheMetric(t, reg, tt.name, tt.labels); got != tt.want {
			t.Errorf("%s %v = %v, want %v", tt.name, tt.labels, got, tt.want)
		}
	}
}

type failingCacheBody struct {
	*strings.Reader
	failure error
}

func (b failingCacheBody) Read(p []byte) (int, error) {
	n, _ := b.Reader.Read(p[:min(3, len(p))])
	return n, b.failure
}
func (b failingCacheBody) Close() error { return nil }

type brokenCacheWriter struct{ *httptest.ResponseRecorder }

func (w brokenCacheWriter) Write(p []byte) (int, error) {
	n, _ := w.ResponseRecorder.Write(p[:min(2, len(p))])
	return n, io.ErrClosedPipe
}

func TestCacheStreamResults(t *testing.T) {
	for _, mode := range []string{"read error", "early EOF", "write error", "canceled", "multipart"} {
		t.Run(mode, func(t *testing.T) {
			reg := prometheus.NewPedanticRegistry()
			c := &Cache{metrics: metrics.New(reg)}
			p := &Proxy{Cache: c, Logger: slog.New(slog.DiscardHandler)}
			reader := struct {
				io.ReadSeeker
				io.Closer
			}{strings.NewReader("tool archive"), io.NopCloser(strings.NewReader(""))}
			f := &cacheBody{ReadSeekCloser: reader, cache: c, source: "s3"}
			var w http.ResponseWriter = httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodGet, "http://example.com/tool", nil)
			wantResult, wantBytes := "error", float64(3)
			switch mode {
			case "read error":
				f.ReadSeekCloser = failingCacheBody{strings.NewReader("tool archive"), io.ErrUnexpectedEOF}
			case "early EOF":
				f.ReadSeekCloser = failingCacheBody{strings.NewReader("tool archive"), io.EOF}
			case "write error":
				w = brokenCacheWriter{httptest.NewRecorder()}
				wantBytes = 2
			case "canceled":
				ctx, cancel := context.WithCancel(r.Context())
				cancel()
				r = r.WithContext(ctx)
				wantResult, wantBytes = "canceled", 12
			case "multipart":
				r.Header.Set("Range", "bytes=0-2,5-11")
				wantResult = "ok"
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			p.serveCached(w, r, "test", f, time.Time{})
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}
			if got := cacheMetric(t, reg, "kritika_cache_responses_total", map[string]string{"source": "s3", "outcome": wantResult}); got != 1 {
				t.Errorf("result %s = %v", wantResult, got)
			}
			gotBytes := cacheMetric(t, reg, "kritika_cache_transfer_bytes_total", map[string]string{"operation": "response_s3"})
			if mode == "multipart" {
				wantBytes = float64(w.(*httptest.ResponseRecorder).Body.Len())
			}
			if gotBytes != wantBytes {
				t.Errorf("bytes = %v, want %v", gotBytes, wantBytes)
			}
			if (mode == "read error" || mode == "early EOF") && cacheMetric(t, reg, "kritika_cache_operation_seconds", map[string]string{"operation": "s3_read", "outcome": "error"}) != 1 {
				t.Error("S3 stream error was not recorded")
			}
		})
	}
}

func TestCacheStorageWarningsAndFallback(t *testing.T) {
	store, opts := newS3TestStore(t)
	store.failRead = true
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	reg := prometheus.NewPedanticRegistry()
	p := cacheProxy(t, egresspolicy.Rules{Allow: hosts("example.com")}, 1<<20, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "direct") })
	useS3(t, p.Cache, opts)
	p.Cache.Observe(metrics.New(reg), logger)
	for i := range 3 {
		if w := proxied(p, http.MethodGet, fmt.Sprintf("http://example.com/tool%d", i), nil); w.Body.String() != "direct" {
			t.Fatal(w.Body.String())
		}
	}
	if strings.Count(logs.String(), "egress cache storage error") != 1 {
		t.Fatalf("warnings not rate limited: %s", logs.String())
	}
	if got := cacheMetric(t, reg, "kritika_cache_operation_seconds", map[string]string{"operation": "s3_lookup", "outcome": "error"}); got != 3 {
		t.Fatalf("failed lookups = %v", got)
	}
	// Each failed lookup fills the replica's own directory, which serves
	// the next request for the same URL without asking the bucket again.
	if got := cacheMetric(t, reg, "kritika_cache_requests_total", map[string]string{"outcome": "miss"}); got != 3 {
		t.Fatalf("misses = %v", got)
	}
	if w := proxied(p, http.MethodGet, "http://example.com/tool0", nil); w.Body.String() != "direct" {
		t.Fatal(w.Body.String())
	}
	if got := cacheMetric(t, reg, "kritika_cache_requests_total", map[string]string{"outcome": "local_hit"}); got != 1 {
		t.Fatalf("local hits = %v", got)
	}
	if got := cacheMetric(t, reg, "kritika_cache_bypasses_total", map[string]string{"reason": "fill_error"}); got != 0 {
		t.Fatalf("bypasses = %v", got)
	}
}

func TestCacheFillAndEvictionGauges(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	p := cacheProxy(t, egresspolicy.Rules{Allow: hosts("example.com")}, 512, func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		_, _ = io.WriteString(w, strings.Repeat("x", 256))
	})
	reg := prometheus.NewPedanticRegistry()
	p.Cache.Observe(metrics.New(reg), p.Logger)
	done := make(chan struct{})
	go func() { proxied(p, http.MethodGet, "http://example.com/first", nil); close(done) }()
	<-started
	active := cacheMetric(t, reg, "kritika_cache_active_fills", nil)
	close(release)
	<-done
	if active != 1 || cacheMetric(t, reg, "kritika_cache_active_fills", nil) != 0 {
		t.Fatal("fill gauge did not track in-flight work")
	}
	e := cacheEntry{URL: "https://example.com/second", Size: 256, Stored: time.Now().UnixMilli()}
	if err := p.Cache.store(cacheKey(e.URL), e, strings.NewReader(strings.Repeat("y", 256))); err != nil {
		t.Fatal(err)
	}
	if got := cacheMetric(t, reg, "kritika_cache_evictions_total", nil); got != 1 {
		t.Fatalf("evictions = %v", got)
	}
	if size := cacheMetric(t, reg, "kritika_cache_local_bytes", nil); size < 256 || size > 512 {
		t.Fatalf("retained bytes = %v", size)
	}
}
