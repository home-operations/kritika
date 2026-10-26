package egress

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/home-operations/kritika/internal/egresspolicy"
)

// cacheProxy is a proxy with a cache in front of an HTTPS server that
// answers for every host; the rules cache example.com unless they name
// hosts of their own.
func cacheProxy(t *testing.T, rules egresspolicy.Rules, maxBytes int64, upstream http.HandlerFunc) *Proxy {
	t.Helper()
	srv := httptest.NewTLSServer(upstream)
	t.Cleanup(srv.Close)
	transport := srv.Client().Transport.(*http.Transport).Clone()
	transport.TLSClientConfig.ServerName = "example.com"
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
	}
	t.Cleanup(transport.CloseIdleConnections)
	return &Proxy{
		Rules: func() egresspolicy.Rules { return rules }, Logger: slog.New(slog.DiscardHandler), Transport: transport,
		Cache: &Cache{Dir: t.TempDir(), MaxBytes: maxBytes},
	}
}

// proxied makes a locked install's request of p, the kind the cache keeps.
func proxied(p *Proxy, method, target string, header http.Header) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, nil)
	maps.Copy(r.Header, header)
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r.WithContext(ForLockedInstall(r.Context())))
	return w
}

// entries are the files the cache keeps, fills under way left out.
func entries(t *testing.T, c *Cache) int {
	t.Helper()
	dirents, err := os.ReadDir(c.Dir)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, d := range dirents {
		if !strings.HasPrefix(d.Name(), ".") {
			n++
		}
	}
	return n
}

func TestCacheServesRepeatDownloads(t *testing.T) {
	var calls atomic.Int32
	const modified = "Mon, 30 Mar 2026 17:49:19 GMT"
	p := cacheProxy(t, egresspolicy.Rules{Allow: hosts("example.com")}, 1<<20, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		// The fetch is the gateway's own, whoever asked first.
		if r.Header.Get("Accept-Encoding") != "identity" || r.Header.Get("User-Agent") != "kritika" || r.Header.Get("Accept") != "*/*" {
			t.Errorf("upstream request headers = %v", r.Header)
		}
		w.Header().Set("Etag", `"v1"`)
		w.Header().Set("Content-Type", "application/gzip")
		w.Header().Set("Last-Modified", modified)
		_, _ = io.WriteString(w, "tool archive")
	})
	const target = "http://example.com/releases/tool.tar.gz"
	for i, method := range []string{http.MethodGet, http.MethodGet, http.MethodHead} {
		w := proxied(p, method, target, http.Header{"User-Agent": {"Go-http-client/1.1"}})
		want := "tool archive"
		if method == http.MethodHead {
			want = ""
		}
		if w.Code != http.StatusOK || w.Body.String() != want || w.Header().Get("Content-Length") != "12" || w.Header().Get("Etag") != `"v1"` ||
			w.Header().Get("Content-Type") != "application/gzip" || w.Header().Get("Last-Modified") != modified {
			t.Errorf("request %d (%s): status %d, body %q, headers %v", i, method, w.Code, w.Body.String(), w.Header())
		}
	}
	for _, tt := range []struct {
		name   string
		header http.Header
		status int
		body   string
	}{
		{"range", http.Header{"Range": {"bytes=5-"}}, http.StatusPartialContent, "archive"},
		{"etag", http.Header{"If-None-Match": {`"v1"`}}, http.StatusNotModified, ""},
		{"modified since", http.Header{"If-Modified-Since": {modified}}, http.StatusNotModified, ""},
		{"unmodified since", http.Header{"If-Unmodified-Since": {"Sun, 29 Mar 2026 00:00:00 GMT"}}, http.StatusPreconditionFailed, ""},
		{"range if unchanged", http.Header{"Range": {"bytes=5-"}, "If-Range": {modified}}, http.StatusPartialContent, "archive"},
		{"range if changed", http.Header{"Range": {"bytes=5-"}, "If-Range": {"Sun, 29 Mar 2026 00:00:00 GMT"}}, http.StatusOK, "tool archive"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if w := proxied(p, http.MethodGet, target, tt.header); w.Code != tt.status || w.Body.String() != tt.body {
				t.Errorf("status %d, body %q; want %d, %q", w.Code, w.Body.String(), tt.status, tt.body)
			}
		})
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("upstream asked %d times, want 1", n)
	}
}

// TestCacheSharesOneFill: requests that arrive while a fill is under way,
// or that missed before it finished, get its body without asking again.
func TestCacheSharesOneFill(t *testing.T) {
	const n = 8
	var calls atomic.Int32
	arrived, release := make(chan struct{}), make(chan struct{})
	p := cacheProxy(t, egresspolicy.Rules{Allow: hosts("example.com")}, 1<<20, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			close(arrived)
		}
		<-release
		_, _ = io.WriteString(w, "tool archive")
	})
	var done sync.WaitGroup
	results := make([]*httptest.ResponseRecorder, n)
	for i := range n {
		done.Go(func() { results[i] = proxied(p, http.MethodGet, "http://example.com/tool.tar.gz", nil) })
	}
	<-arrived
	close(release)
	done.Wait()
	for i, w := range results {
		if w.Code != http.StatusOK || w.Body.String() != "tool archive" {
			t.Errorf("request %d: status %d, body %q", i, w.Code, w.Body.String())
		}
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("upstream asked %d times, want 1", got)
	}
}

func TestCacheBypass(t *testing.T) {
	for _, tt := range []struct {
		name, method, target string
		header               http.Header
		credentials          map[string]string
		unlocked             bool
	}{
		{"not a locked install", http.MethodGet, "http://example.com/tool.tar.gz", nil, nil, true},
		{"credential added", http.MethodGet, "http://example.com/tool.tar.gz", nil, map[string]string{"example.com": "Bearer configured"}, false},
		{"runner credential", http.MethodGet, "http://example.com/tool.tar.gz", http.Header{"Authorization": {"Bearer runner"}}, nil, false},
		{"cookie", http.MethodGet, "http://example.com/tool.tar.gz", http.Header{"Cookie": {"session=1"}}, nil, false},
		{"post", http.MethodPost, "http://example.com/tool.tar.gz", nil, nil, false},
		{"query", http.MethodGet, "http://example.com/tool.tar.gz?X-Amz-Signature=abc", nil, nil, false},
		{"empty query", http.MethodGet, "http://example.com/tool.tar.gz?", nil, nil, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			rules := egresspolicy.Rules{Allow: hosts("example.com", "other.example.com"), Credentials: tt.credentials}
			p := cacheProxy(t, rules, 1<<20, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if want := tt.credentials["example.com"]; r.Header.Get("Authorization") != want {
					t.Errorf("upstream Authorization = %q, want %q", r.Header.Get("Authorization"), want)
				}
				_, _ = io.WriteString(w, "response")
			})
			for range 2 {
				r := httptest.NewRequest(tt.method, tt.target, nil)
				maps.Copy(r.Header, tt.header)
				if !tt.unlocked {
					r = r.WithContext(ForLockedInstall(r.Context()))
				}
				w := httptest.NewRecorder()
				p.ServeHTTP(w, r)
				if w.Code != http.StatusOK || w.Body.String() != "response" {
					t.Fatalf("status %d, body %q", w.Code, w.Body.String())
				}
			}
			if got := calls.Load(); got != 2 {
				t.Errorf("upstream asked %d times, want 2", got)
			}
			if n := entries(t, p.Cache); n != 0 {
				t.Errorf("cache holds %d entries", n)
			}
		})
	}
}

// TestCacheKeepsLockedInstalls: a locked install's download is kept from
// any host and served again to another locked install, but never kept
// from a host the rules name as uncached or one with a credential, and
// never for a request that is not a locked install's.
func TestCacheKeepsLockedInstalls(t *testing.T) {
	for _, tt := range []struct {
		name, host  string
		credentials map[string]string
		// locked says which of the requests, in turn, are a locked
		// install's.
		locked  []bool
		calls   int32
		entries int
	}{
		{"from any host", "other.example.com", nil, []bool{true, true, false}, 2, 1},
		{"not a locked install", "other.example.com", nil, []bool{false, false}, 2, 0},
		{"from an uncached host", "api.example.com", nil, []bool{true, true}, 2, 0},
		{"from an uncached host with a trailing dot", "api.example.com.", nil, []bool{true, true}, 2, 0},
		{"from a host with a credential", "other.example.com", map[string]string{"other.example.com": "Bearer configured"}, []bool{true, true}, 2, 0},
		{"from a host with a credential and a trailing dot", "other.example.com.", map[string]string{"other.example.com": "Bearer configured"},
			[]bool{true, true}, 2, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			rules := egresspolicy.Rules{
				Allow: hosts("example.com", "other.example.com", "api.example.com"), Credentials: tt.credentials,
				Uncached: []string{"api.example.com"},
			}
			p := cacheProxy(t, rules, 1<<20, func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				_, _ = io.WriteString(w, "tool archive")
			})
			for i, locked := range tt.locked {
				r := httptest.NewRequest(http.MethodGet, "http://"+tt.host+"/tool.tar.gz", nil)
				if locked {
					r = r.WithContext(ForLockedInstall(r.Context()))
				}
				w := httptest.NewRecorder()
				p.ServeHTTP(w, r)
				if w.Code != http.StatusOK || w.Body.String() != "tool archive" {
					t.Fatalf("request %d: status %d, body %q", i, w.Code, w.Body.String())
				}
			}
			if got := calls.Load(); got != tt.calls {
				t.Errorf("upstream asked %d times, want %d", got, tt.calls)
			}
			if n := entries(t, p.Cache); n != tt.entries {
				t.Errorf("cache holds %d entries, want %d", n, tt.entries)
			}
		})
	}
}

func TestCacheKeepsOnlyWholeSharedOKBodies(t *testing.T) {
	for _, tt := range []struct {
		name    string
		respond func(http.ResponseWriter)
		status  int
	}{
		{"not found", func(w http.ResponseWriter) { http.Error(w, "missing", http.StatusNotFound) }, http.StatusNotFound},
		{"encoded", func(w http.ResponseWriter) {
			w.Header().Set("Content-Encoding", "gzip")
			z := gzip.NewWriter(w)
			_, _ = io.WriteString(z, "zipped")
			_ = z.Close()
		}, http.StatusOK},
		{"no length", func(w http.ResponseWriter) {
			_, _ = io.WriteString(w, "streamed")
			w.(http.Flusher).Flush()
		}, http.StatusOK},
		{"over the bound", func(w http.ResponseWriter) {
			w.Header().Set("Content-Length", "2048")
			_, _ = io.WriteString(w, strings.Repeat("x", 2048))
		}, http.StatusOK},
		{"sets a cookie", func(w http.ResponseWriter) {
			w.Header().Set("Set-Cookie", "session=1")
			_, _ = io.WriteString(w, "personal")
		}, http.StatusOK},
		{"private", func(w http.ResponseWriter) {
			w.Header().Set("Cache-Control", "max-age=60, Private")
			_, _ = io.WriteString(w, "personal")
		}, http.StatusOK},
		{"no-store", func(w http.ResponseWriter) {
			w.Header().Set("Cache-Control", "no-store")
			_, _ = io.WriteString(w, "personal")
		}, http.StatusOK},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			p := cacheProxy(t, egresspolicy.Rules{Allow: hosts("example.com")}, 1024, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				tt.respond(w)
			})
			for i := range 2 {
				before := calls.Load()
				if w := proxied(p, http.MethodGet, "http://example.com/tool.tar.gz", nil); w.Code != tt.status {
					t.Fatalf("request %d: status %d, want %d", i, w.Code, tt.status)
				}
				if calls.Load() == before {
					t.Errorf("request %d answered without asking upstream", i)
				}
			}
			if n := entries(t, p.Cache); n != 0 {
				t.Errorf("cache holds %d entries", n)
			}
		})
	}
}

func TestCacheFollowsRedirects(t *testing.T) {
	for _, tt := range []struct {
		name, location string
		allow          egresspolicy.List
		// redirect is what the redirect's response carries besides.
		redirect http.Header
		kept     bool
	}{
		// As a GitHub release download's redirect is answered.
		{"allowed", "https://assets.example.net/blob?sig=1", hosts("example.com", "assets.example.net"), http.Header{"Cache-Control": {"no-cache"}}, true},
		{"host refused", "https://assets.example.net/blob?sig=1", hosts("example.com"), nil, false},
		{"port refused", "https://assets.example.net:8443/blob?sig=1", hosts("example.com", "assets.example.net"), nil, false},
		{"not https", "http://assets.example.net/blob?sig=1", hosts("example.com", "assets.example.net"), nil, false},
		{"credentials", "https://user:secret@assets.example.net/blob?sig=1", hosts("example.com", "assets.example.net"), nil, false},
		{"sets a cookie", "https://assets.example.net/blob?sig=1", hosts("example.com", "assets.example.net"), http.Header{"Set-Cookie": {"session=1"}}, false},
		{"no-store", "https://assets.example.net/blob?sig=1", hosts("example.com", "assets.example.net"), http.Header{"Cache-Control": {"no-store"}}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			p := cacheProxy(t, egresspolicy.Rules{Allow: tt.allow}, 1<<20, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Host == "example.com" {
					maps.Copy(w.Header(), tt.redirect)
					http.Redirect(w, r, tt.location, http.StatusFound)
					return
				}
				if r.URL.RawQuery != "sig=1" || r.Header.Get("Authorization") != "" {
					t.Errorf("redirect target query %q, Authorization %q", r.URL.RawQuery, r.Header.Get("Authorization"))
				}
				_, _ = io.WriteString(w, "tool archive")
			})
			for i := range 2 {
				before := calls.Load()
				w := proxied(p, http.MethodGet, "http://example.com/releases/download/tool.tar.gz", nil)
				switch {
				case tt.kept && (w.Code != http.StatusOK || w.Body.String() != "tool archive"):
					t.Fatalf("request %d: status %d, body %q", i, w.Code, w.Body.String())
				case !tt.kept && (w.Code != http.StatusFound || w.Header().Get("Location") != tt.location):
					t.Fatalf("request %d: status %d, Location %q; want the redirect handed back", i, w.Code, w.Header().Get("Location"))
				}
				if asked := calls.Load() > before; i == 1 && asked == tt.kept {
					t.Errorf("second request asked upstream: %v, want %v", asked, !tt.kept)
				}
			}
		})
	}
}

// TestCacheKeepsNothingRedirectedOut: a fill whose redirect goes to a host
// the cache keeps nothing from, one the rules name as uncached or one with
// a credential, however the name is written, is not kept, and the
// redirect is handed back.
func TestCacheKeepsNothingRedirectedOut(t *testing.T) {
	for _, tt := range []struct{ name, location string }{
		{"to an uncached host", "https://api.example.net/blob"},
		{"to an uncached host with a trailing dot", "https://api.example.net./blob"},
		{"to a host with a credential", "https://assets.example.net/blob"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rules := egresspolicy.Rules{
				Allow: hosts("example.com", "api.example.net", "assets.example.net"), Uncached: []string{"api.example.net"},
				Credentials: map[string]string{"assets.example.net": "Bearer configured"},
			}
			p := cacheProxy(t, rules, 1<<20, func(w http.ResponseWriter, r *http.Request) {
				if r.Host == "example.com" {
					http.Redirect(w, r, tt.location, http.StatusFound)
					return
				}
				_, _ = io.WriteString(w, "tool archive")
			})
			for i := range 2 {
				r := httptest.NewRequest(http.MethodGet, "http://example.com/releases/download/tool.tar.gz", nil)
				w := httptest.NewRecorder()
				p.ServeHTTP(w, r.WithContext(ForLockedInstall(r.Context())))
				if w.Code != http.StatusFound || w.Header().Get("Location") != tt.location {
					t.Fatalf("request %d: status %d, Location %q; want the redirect handed back", i, w.Code, w.Header().Get("Location"))
				}
			}
			if n := entries(t, p.Cache); n != 0 {
				t.Errorf("cache holds %d entries", n)
			}
		})
	}
}

// TestCacheBoundsFills: a fill past maxFills, or past the bytes the fills
// under way may write, is not made, and the request is forwarded.
func TestCacheBoundsFills(t *testing.T) {
	c := &Cache{MaxBytes: 100}
	for range maxFills {
		if !c.begin() {
			t.Fatal("a fill was refused below maxFills")
		}
	}
	if c.begin() {
		t.Fatal("a fill past maxFills was let through")
	}
	if !c.reserve(60) || c.reserve(41) || !c.reserve(40) {
		t.Fatalf("reserved = %d: want 60 and 40 of 100 held, 41 more refused", c.reserved)
	}
	c.end(60)
	if !c.begin() || !c.reserve(60) || c.reserve(1) {
		t.Fatalf("after one fill ended: filling %d, reserved %d", c.filling, c.reserved)
	}

	var calls atomic.Int32
	p := cacheProxy(t, egresspolicy.Rules{Allow: hosts("example.com")}, 1024, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, strings.Repeat("x", 100))
	})
	if !p.Cache.reserve(1000) {
		t.Fatal("could not hold the bytes of fills under way")
	}
	if w := proxied(p, http.MethodGet, "http://example.com/tool.tar.gz", nil); w.Code != http.StatusOK || w.Body.Len() != 100 {
		t.Fatalf("status %d, %d bytes", w.Code, w.Body.Len())
	}
	if n := entries(t, p.Cache); n != 0 || calls.Load() != 2 {
		t.Fatalf("with the fills under way holding the bound: %d entries, %d upstream requests; want none kept, forwarded", n, calls.Load())
	}
	p.Cache.end(1000)
	proxied(p, http.MethodGet, "http://example.com/tool.tar.gz", nil)
	if n := entries(t, p.Cache); n != 1 {
		t.Fatalf("once they ended, the cache holds %d entries, want 1", n)
	}
}

func TestCacheEvictsLeastRecentlyUsed(t *testing.T) {
	calls := map[string]int{}
	var mu sync.Mutex
	// Room for two entries of 100 bytes and their first lines, not three.
	p := cacheProxy(t, egresspolicy.Rules{Allow: hosts("example.com")}, 500, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls[r.URL.Path]++
		mu.Unlock()
		_, _ = io.WriteString(w, strings.Repeat("x", 100))
	})
	for _, path := range []string{"/a", "/b", "/a", "/c", "/a", "/b"} {
		if w := proxied(p, http.MethodGet, "http://example.com"+path, nil); w.Code != http.StatusOK || w.Body.Len() != 100 {
			t.Fatalf("%s: status %d, %d bytes", path, w.Code, w.Body.Len())
		}
		// Use times a filesystem keeps apart.
		time.Sleep(20 * time.Millisecond)
	}
	// /a, used again before /c was stored, outlived /b.
	if want := map[string]int{"/a": 1, "/b": 2, "/c": 1}; !maps.Equal(calls, want) {
		t.Errorf("upstream asked %v, want %v", calls, want)
	}
}

func TestCacheRefetchesDamagedEntry(t *testing.T) {
	var calls atomic.Int32
	p := cacheProxy(t, egresspolicy.Rules{Allow: hosts("example.com")}, 1<<20, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, "tool archive")
	})
	const target = "http://example.com/tool.tar.gz"
	proxied(p, http.MethodGet, target, nil)
	path := filepath.Join(p.Cache.Dir, cacheKey("https://example.com/tool.tar.gz"))
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, info.Size()-4); err != nil {
		t.Fatal(err)
	}
	if w := proxied(p, http.MethodGet, target, nil); w.Code != http.StatusOK || w.Body.String() != "tool archive" {
		t.Fatalf("status %d, body %q", w.Code, w.Body.String())
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("upstream asked %d times, want 2", got)
	}
}

func TestCacheRefetchesExpiredEntry(t *testing.T) {
	var calls atomic.Int32
	p := cacheProxy(t, egresspolicy.Rules{Allow: hosts("example.com")}, 1<<20, func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, "archive %d", calls.Add(1))
	})
	const target = "http://example.com/tool.tar.gz"
	for i, want := range []string{"archive 1", "archive 1"} {
		if w := proxied(p, http.MethodGet, target, nil); w.Body.String() != want {
			t.Fatalf("request %d: body %q, want %q", i, w.Body.String(), want)
		}
	}
	// With no validators to offer, an expired entry is fetched again.
	expireLocal(t, p.Cache, cacheKey("https://example.com/tool.tar.gz"))
	if w := proxied(p, http.MethodGet, target, nil); w.Body.String() != "archive 2" {
		t.Fatalf("after refreshAfter: body %q, want a fresh fetch", w.Body.String())
	}
}

// TestCacheRefills: a locked install's retry, which the proxy's username
// marks, is answered from the origin, and what it fetched takes the
// entry's place for the locked installs that follow.
func TestCacheRefills(t *testing.T) {
	var calls atomic.Int32
	p := cacheProxy(t, egresspolicy.Rules{Allow: hosts("example.com")}, 1<<20, func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, "archive %d", calls.Add(1))
	})
	const target = "http://example.com/tool.tar.gz"
	if w := proxied(p, http.MethodGet, target, nil); w.Body.String() != "archive 1" {
		t.Fatalf("fill: %q", w.Body.String())
	}
	r := httptest.NewRequest(http.MethodGet, target, nil)
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r.WithContext(ForRefill(r.Context())))
	if w.Code != http.StatusOK || w.Body.String() != "archive 2" {
		t.Fatalf("refill: %d %q, want the origin's answer", w.Code, w.Body.String())
	}
	if w := proxied(p, http.MethodGet, target, nil); w.Body.String() != "archive 2" || calls.Load() != 2 {
		t.Fatalf("after the refill: %q after %d fetches, want the refilled entry", w.Body.String(), calls.Load())
	}
	if n := entries(t, p.Cache); n != 1 {
		t.Errorf("cache holds %d entries, want 1", n)
	}
	// A retry for an entry replaced a moment ago is served the entry; a HEAD
	// is never a retry, and is forwarded without touching the entry.
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		r := httptest.NewRequest(method, target, nil)
		w := httptest.NewRecorder()
		p.ServeHTTP(w, r.WithContext(ForRefill(r.Context())))
		if w.Code != http.StatusOK {
			t.Fatalf("%s retry: %d", method, w.Code)
		}
	}
	if w := proxied(p, http.MethodGet, target, nil); w.Body.String() != "archive 2" || calls.Load() != 3 {
		t.Fatalf("after a second retry and a HEAD: %q after %d fetches, want one forwarded HEAD only", w.Body.String(), calls.Load())
	}
}

// TestCacheRefillDoesNotJoinRevalidation: a retry arriving while another
// request has the stale entry revalidated does not wait for, or get, that
// request's answer, which would be the body the retry refused.
func TestCacheRefillDoesNotJoinRevalidation(t *testing.T) {
	conditional, release := make(chan struct{}, 1), make(chan struct{})
	defer close(release)
	var unconditional atomic.Int32
	p := cacheProxy(t, egresspolicy.Rules{Allow: hosts("example.com")}, 1<<20, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == `"v1"` {
			conditional <- struct{}{}
			<-release
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Etag", fmt.Sprintf(`"v%d"`, unconditional.Add(1)))
		_, _ = fmt.Fprintf(w, "archive %d", unconditional.Load())
	})
	const target = "http://example.com/tool.tar.gz"
	if w := proxied(p, http.MethodGet, target, nil); w.Body.String() != "archive 1" {
		t.Fatalf("fill: %q", w.Body.String())
	}
	expireLocal(t, p.Cache, cacheKey("https://example.com/tool.tar.gz"))
	revalidated, refilled := make(chan *httptest.ResponseRecorder, 1), make(chan *httptest.ResponseRecorder, 1)
	go func() { revalidated <- proxied(p, http.MethodGet, target, nil) }()
	<-conditional
	go func() {
		r := httptest.NewRequest(http.MethodGet, target, nil)
		w := httptest.NewRecorder()
		p.ServeHTTP(w, r.WithContext(ForRefill(r.Context())))
		refilled <- w
	}()
	// The retry waits its turn behind the revalidation rather than joining
	// it, and then fetches without conditions.
	release <- struct{}{}
	if w := <-refilled; w.Code != http.StatusOK || w.Body.String() != "archive 2" || unconditional.Load() != 2 {
		t.Fatalf("refill during a revalidation: %d %q after %d fetches, want the origin's new body", w.Code, w.Body.String(), unconditional.Load())
	}
	if w := <-revalidated; w.Code != http.StatusOK {
		t.Fatalf("revalidating request: %d", w.Code)
	}
}

func TestOpenCache(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cache")
	if _, err := OpenCache(dir, 1<<20); err != nil {
		t.Fatal(err)
	}
	leftover := filepath.Join(dir, ".fill-123")
	kept := filepath.Join(dir, cacheKey("https://example.com/tool.tar.gz"))
	for _, path := range []string{leftover, kept} {
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	c, err := OpenCache(dir, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if c.Dir != dir || c.MaxBytes != 1<<20 {
		t.Errorf("cache = %+v", c)
	}
	if _, err := os.Stat(leftover); !os.IsNotExist(err) {
		t.Errorf("a fill left from before is still there: %v", err)
	}
	if _, err := os.Stat(kept); err != nil {
		t.Errorf("an entry was removed: %v", err)
	}
	if os.Geteuid() != 0 {
		readOnly := t.TempDir()
		if err := os.Chmod(readOnly, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(readOnly, 0o700) })
		if _, err := OpenCache(readOnly, 1<<20); err == nil {
			t.Error("a directory the cache cannot write was accepted")
		}
	}
}
