package egress

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type s3TestObject struct {
	body    []byte
	headers http.Header
}

type s3TestStore struct {
	mu                  sync.Mutex
	objects             map[string]s3TestObject
	requests            []string
	failRead, failWrite bool
}

func newS3TestStore(t *testing.T) (*s3TestStore, S3Options) {
	t.Helper()
	store := &s3TestStore{objects: make(map[string]s3TestObject)}
	srv := httptest.NewServer(http.HandlerFunc(store.serve))
	t.Cleanup(srv.Close)
	return store, S3Options{Endpoint: srv.URL, Region: "test-region", Bucket: "cache", Prefix: "instance/", AccessKey: "test-access", SecretKey: "test-secret", ForcePathStyle: true}
}

func (s *s3TestStore) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, r.Method+" "+r.URL.Path)
	if (r.Method == http.MethodPut && s.failWrite) || (r.Method != http.MethodPut && s.failRead) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, "<Error><Code>AccessDenied</Code></Error>")
		return
	}
	switch r.Method {
	case http.MethodPut:
		if r.URL.RawQuery != "" || r.Header.Get("If-None-Match") != "" || r.Header.Get("If-Match") != "" {
			http.Error(w, "unsupported operation", http.StatusNotImplemented)
			return
		}
		b, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		sum := md5.Sum(b)
		if r.ContentLength != int64(len(b)) || r.Header.Get("Content-MD5") != base64.StdEncoding.EncodeToString(sum[:]) {
			http.Error(w, "invalid body length or checksum", http.StatusBadRequest)
			return
		}
		h := make(http.Header)
		h.Set("Etag", fmt.Sprintf("\"%x\"", sum))
		h.Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
		for k, v := range r.Header {
			if strings.HasPrefix(k, "X-Amz-Meta-") {
				h[k] = v
			}
		}
		s.objects[r.URL.Path] = s3TestObject{b, h}
		w.Header().Set("Etag", h.Get("Etag"))
	case http.MethodGet, http.MethodHead:
		o, ok := s.objects[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, "<Error><Code>NoSuchKey</Code></Error>")
			return
		}
		maps.Copy(w.Header(), o.headers)
		w.Header().Set("Content-Type", "application/octet-stream")
		http.ServeContent(w, r, "", time.Now(), bytes.NewReader(o.body))
	default:
		http.Error(w, "unsupported operation", http.StatusNotImplemented)
	}
}

func useS3(t *testing.T, c *Cache, opts S3Options) {
	t.Helper()
	c.MaxEntryBytes = 1 << 20
	if err := c.UseS3(opts); err != nil {
		t.Fatal(err)
	}
}

// Each reader starts with an empty local directory, as a new replica does.
func TestS3CacheSharesDownloads(t *testing.T) {
	_, opts := newS3TestStore(t)
	testS3CacheSharesDownloads(t, opts)
}

func testS3CacheSharesDownloads(t *testing.T, opts S3Options) {
	t.Helper()
	body := strings.Repeat("tool archive", 4096)
	var calls atomic.Int32
	upstream := func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/gzip")
		w.Header().Set("Content-Length", fmt.Sprint(len(body)))
		w.Header().Set("ETag", "\"origin\"")
		w.Header().Set("Last-Modified", "Mon, 30 Mar 2026 17:49:19 GMT")
		_, _ = io.WriteString(w, body)
	}
	writer := cacheProxy(t, Rules{Allow: hosts("example.com")}, 1<<20, upstream)
	useS3(t, writer.Cache, opts)
	const target = "http://example.com/tool.tar.gz"
	if w := proxied(writer, http.MethodGet, target, nil); w.Body.String() != body {
		t.Fatalf("fill: %d %q", w.Code, w.Body.String())
	}
	for _, tt := range []struct {
		name, method string
		headers      http.Header
		status       int
		body         string
	}{
		{"get", http.MethodGet, nil, 200, body},
		{"head", http.MethodHead, nil, 200, ""},
		{"range", http.MethodGet, http.Header{"Range": {"bytes=5-11"}}, 206, "archive"},
		{"suffix", http.MethodGet, http.Header{"Range": {"bytes=-7"}}, 206, "archive"},
		{"etag", http.MethodGet, http.Header{"If-None-Match": {"\"origin\""}}, 304, ""},
		{"date", http.MethodGet, http.Header{"If-Modified-Since": {"Mon, 30 Mar 2026 17:49:19 GMT"}}, 304, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			reader := cacheProxy(t, Rules{Allow: hosts("example.com")}, 1<<20, upstream)
			useS3(t, reader.Cache, opts)
			w := proxied(reader, tt.method, target, tt.headers)
			if w.Code != tt.status || w.Body.String() != tt.body {
				t.Fatalf("got %d %q, want %d %q", w.Code, w.Body.String(), tt.status, tt.body)
			}
			if w.Header().Get("Etag") != "\"origin\"" {
				t.Errorf("origin ETag lost: %v", w.Header())
			}
		})
	}
	if calls.Load() != 1 {
		t.Errorf("upstream fetched %d times", calls.Load())
	}
}

func TestS3CacheFailures(t *testing.T) {
	for _, mode := range []string{"read denied", "write denied", "expired", "damaged metadata", "wrong size", "wrong URL"} {
		t.Run(mode, func(t *testing.T) {
			store, opts := newS3TestStore(t)
			var calls atomic.Int32
			p := cacheProxy(t, Rules{Allow: hosts("example.com")}, 1<<20, func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				_, _ = io.WriteString(w, "tool archive")
			})
			useS3(t, p.Cache, opts)
			const target = "http://example.com/tool.tar.gz"
			key := cacheKey("https://example.com/tool.tar.gz")
			if mode == "read denied" {
				store.failRead = true
			}
			if mode == "write denied" {
				store.failWrite = true
			}
			if mode != "read denied" && mode != "write denied" {
				e := cacheEntry{URL: "https://example.com/tool.tar.gz", Size: 12, Stored: time.Now().UnixMilli()}
				switch mode {
				case "expired":
					e.Stored = time.Now().Add(-2 * time.Hour).UnixMilli()
				case "wrong size":
					e.Size++
				case "wrong URL":
					e.URL = "https://example.com/other"
				}
				raw, _ := json.Marshal(e)
				if mode == "damaged metadata" {
					raw = []byte("broken")
				}
				store.objects["/cache/instance/v1/"+key] = s3TestObject{[]byte("tool archive"), http.Header{
					"Etag": {"\"stored\""}, "X-Amz-Meta-Kritika-Entry": {base64.StdEncoding.EncodeToString(raw)},
				}}
			}
			w := proxied(p, http.MethodGet, target, nil)
			if w.Code != 200 || w.Body.String() != "tool archive" {
				t.Fatalf("got %d %q", w.Code, w.Body.String())
			}
			if calls.Load() != 1 {
				t.Fatalf("upstream fetched %d times", calls.Load())
			}
			if mode == "write denied" {
				w = proxied(p, http.MethodGet, target, nil)
				if w.Body.String() != "tool archive" || calls.Load() != 1 {
					t.Fatalf("failed upload lost local fill: %d calls, %q", calls.Load(), w.Body.String())
				}
			}
		})
	}
}

func TestS3CacheConcurrentFills(t *testing.T) {
	_, opts := newS3TestStore(t)
	testS3CacheConcurrentFills(t, opts)
}

func testS3CacheConcurrentFills(t *testing.T, opts S3Options) {
	t.Helper()
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	var calls atomic.Int32
	proxies := make([]*Proxy, 2)
	for i := range proxies {
		proxies[i] = cacheProxy(t, Rules{Allow: hosts("example.com")}, 1<<20, func(w http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			started <- struct{}{}
			<-release
			_, _ = io.WriteString(w, "tool archive")
		})
		useS3(t, proxies[i].Cache, opts)
	}
	done := make(chan *httptest.ResponseRecorder, 2)
	for _, p := range proxies {
		go func() { done <- proxied(p, http.MethodGet, "http://example.com/tool", nil) }()
	}
	<-started
	<-started
	close(release)
	for range proxies {
		w := <-done
		if w.Code != 200 || w.Body.String() != "tool archive" {
			t.Fatalf("got %d %q", w.Code, w.Body.String())
		}
	}
	if calls.Load() != 2 {
		t.Errorf("cold fetches = %d", calls.Load())
	}
	reader := cacheProxy(t, Rules{Allow: hosts("example.com")}, 1<<20, func(w http.ResponseWriter, _ *http.Request) {
		t.Error("concurrent fills did not publish a reusable object")
		w.WriteHeader(http.StatusBadGateway)
	})
	useS3(t, reader.Cache, opts)
	if w := proxied(reader, http.MethodGet, "http://example.com/tool", nil); w.Code != 200 || w.Body.String() != "tool archive" {
		t.Fatalf("concurrent object: %d %q", w.Code, w.Body.String())
	}
}

func TestS3CachePinsReadRevision(t *testing.T) {
	store, opts := newS3TestStore(t)
	c := &Cache{Dir: t.TempDir(), MaxAge: time.Hour}
	useS3(t, c, opts)
	key := cacheKey("https://example.com/tool")
	entry := cacheEntry{URL: "https://example.com/tool", Stored: time.Now().UnixMilli(), Size: 3}
	if err := c.store(key, entry, strings.NewReader("old")); err != nil {
		t.Fatal(err)
	}
	if err := c.upload(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	c.Dir = t.TempDir()
	f, _, err := c.load(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	store.mu.Lock()
	o := store.objects["/cache/instance/v1/"+key]
	o.body = []byte("new")
	o.headers.Set("Etag", "\"new\"")
	store.objects["/cache/instance/v1/"+key] = o
	store.mu.Unlock()
	if b, err := io.ReadAll(f); err == nil || len(b) != 0 {
		t.Fatalf("replacement was read: %q, %v", b, err)
	}
}

func TestS3CacheBypass(t *testing.T) {
	store, opts := newS3TestStore(t)
	p := cacheProxy(t, Rules{Allow: hosts("example.com")}, 1<<20, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "direct")
	})
	useS3(t, p.Cache, opts)
	r := httptest.NewRequest(http.MethodGet, "http://example.com/tool", nil)
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r.WithContext(WithoutCache(r.Context())))
	if w.Code != 200 || w.Body.String() != "direct" {
		t.Fatalf("got %d %q", w.Code, w.Body.String())
	}
	if len(store.requests) != 0 {
		t.Fatalf("bypass contacted S3: %v", store.requests)
	}
}

func TestS3CacheCanceledRead(t *testing.T) {
	_, opts := newS3TestStore(t)
	c := &Cache{Dir: t.TempDir()}
	useS3(t, c, opts)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if f, _, err := c.load(ctx, cacheKey("https://example.com/tool")); err == nil {
		_ = f.Close()
		t.Fatal("canceled read succeeded")
	}
}
