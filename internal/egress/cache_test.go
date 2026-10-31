package egress

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
// answers for every host. The rules cache example.com unless they name
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

// digestOf is the digest of body, as a runner names it.
func digestOf(body string) Digest {
	sum := sha256.Sum256([]byte(body))
	return Digest{Algorithm: "sha256", Hex: hex.EncodeToString(sum[:])}
}

// keyOf is the key of the entry that keeps body.
func keyOf(body string) string { return digestOf(body).key() }

// entryOf is the entry a fill of body from url keeps.
func entryOf(url, body string) cacheEntry {
	return cacheEntry{URL: url, Digest: digestOf(body).String(), Size: int64(len(body))}
}

// proxied makes a request of p for a body with body's digest, the kind
// the cache keeps.
func proxied(p *Proxy, method, target, body string, header http.Header) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, nil)
	maps.Copy(r.Header, header)
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r.WithContext(ForDigest(r.Context(), digestOf(body))))
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
	p := cacheProxy(t, egresspolicy.Rules{Allow: hosts("example.com")}, 1<<20, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		// The fetch is the gateway's own, whoever asked first.
		if r.Header.Get("Accept-Encoding") != "identity" || r.Header.Get("User-Agent") != "kritika" || r.Header.Get("Accept") != "*/*" {
			t.Errorf("upstream request headers = %v", r.Header)
		}
		w.Header().Set("Content-Type", "application/gzip")
		_, _ = io.WriteString(w, "tool archive")
	})
	const target = "http://example.com/releases/tool.tar.gz"
	etag := `"` + digestOf("tool archive").String() + `"`
	for i, method := range []string{http.MethodGet, http.MethodGet, http.MethodHead} {
		w := proxied(p, method, target, "tool archive", http.Header{"User-Agent": {"Go-http-client/1.1"}})
		want := "tool archive"
		if method == http.MethodHead {
			want = ""
		}
		if w.Code != http.StatusOK || w.Body.String() != want || w.Header().Get("Content-Length") != "12" || w.Header().Get("Etag") != etag ||
			w.Header().Get("Content-Type") != "application/gzip" {
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
		{"etag", http.Header{"If-None-Match": {etag}}, http.StatusNotModified, ""},
		{"range if unchanged", http.Header{"Range": {"bytes=5-"}, "If-Range": {etag}}, http.StatusPartialContent, "archive"},
		{"range if changed", http.Header{"Range": {"bytes=5-"}, "If-Range": {`"other"`}}, http.StatusOK, "tool archive"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if w := proxied(p, http.MethodGet, target, "tool archive", tt.header); w.Code != tt.status || w.Body.String() != tt.body {
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
		done.Go(func() { results[i] = proxied(p, http.MethodGet, "http://example.com/tool.tar.gz", "tool archive", nil) })
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
		unnamed              bool
	}{
		{"no digest named", http.MethodGet, "http://example.com/tool.tar.gz", nil, nil, true},
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
				if !tt.unnamed {
					r = r.WithContext(ForDigest(r.Context(), digestOf("response")))
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

// TestCacheKeepsNamedDigests: a download whose digest the request names is
// kept from any host and served again to another request naming it. It is
// never kept from a host that the rules name as uncached or from a host with
// a credential. It is never kept for a request that names no digest.
func TestCacheKeepsNamedDigests(t *testing.T) {
	for _, tt := range []struct {
		name, host  string
		credentials map[string]string
		// named says which of the requests, in turn, name the digest.
		named   []bool
		calls   int32
		entries int
	}{
		{"from any host", "other.example.com", nil, []bool{true, true, false}, 2, 1},
		{"no digest named", "other.example.com", nil, []bool{false, false}, 2, 0},
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
			for i, named := range tt.named {
				r := httptest.NewRequest(http.MethodGet, "http://"+tt.host+"/tool.tar.gz", nil)
				if named {
					r = r.WithContext(ForDigest(r.Context(), digestOf("tool archive")))
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
	var gz bytes.Buffer
	z := gzip.NewWriter(&gz)
	_, _ = io.WriteString(z, "zipped")
	_ = z.Close()
	over := strings.Repeat("x", 2048)
	// Each names the digest of the very body it answers with. So what keeps
	// it out of the cache is the case and not a digest mismatch.
	for _, tt := range []struct {
		name, body string
		respond    func(http.ResponseWriter)
		status     int
	}{
		{"not found", "missing\n", func(w http.ResponseWriter) { http.Error(w, "missing", http.StatusNotFound) }, http.StatusNotFound},
		{"encoded", gz.String(), func(w http.ResponseWriter) {
			w.Header().Set("Content-Encoding", "gzip")
			_, _ = w.Write(gz.Bytes())
		}, http.StatusOK},
		{"no length", "streamed", func(w http.ResponseWriter) {
			_, _ = io.WriteString(w, "streamed")
			w.(http.Flusher).Flush()
		}, http.StatusOK},
		{"over the bound", over, func(w http.ResponseWriter) {
			w.Header().Set("Content-Length", "2048")
			_, _ = io.WriteString(w, over)
		}, http.StatusOK},
		{"sets a cookie", "personal", func(w http.ResponseWriter) {
			w.Header().Set("Set-Cookie", "session=1")
			_, _ = io.WriteString(w, "personal")
		}, http.StatusOK},
		{"private", "personal", func(w http.ResponseWriter) {
			w.Header().Set("Cache-Control", "max-age=60, Private")
			_, _ = io.WriteString(w, "personal")
		}, http.StatusOK},
		{"no-store", "personal", func(w http.ResponseWriter) {
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
				if w := proxied(p, http.MethodGet, "http://example.com/tool.tar.gz", tt.body, nil); w.Code != tt.status {
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
				w := proxied(p, http.MethodGet, "http://example.com/releases/download/tool.tar.gz", "tool archive", nil)
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

// TestCacheKeepsNothingRedirectedOut: a fill is not kept when its redirect
// goes to a host that the cache keeps nothing from. That host is one that
// the rules name as uncached or one with a credential, however the name is
// written. The redirect is handed back.
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
				p.ServeHTTP(w, r.WithContext(ForDigest(r.Context(), digestOf("tool archive"))))
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
// under way can write, is not made, and the request is forwarded.
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
	body := strings.Repeat("x", 100)
	if w := proxied(p, http.MethodGet, "http://example.com/tool.tar.gz", body, nil); w.Code != http.StatusOK || w.Body.Len() != 100 {
		t.Fatalf("status %d, %d bytes", w.Code, w.Body.Len())
	}
	if n := entries(t, p.Cache); n != 0 || calls.Load() != 2 {
		t.Fatalf("with the fills under way holding the bound: %d entries, %d upstream requests; want none kept, forwarded", n, calls.Load())
	}
	p.Cache.end(1000)
	proxied(p, http.MethodGet, "http://example.com/tool.tar.gz", body, nil)
	if n := entries(t, p.Cache); n != 1 {
		t.Fatalf("once they ended, the cache holds %d entries, want 1", n)
	}
}

func TestCacheEvictsLeastRecentlyUsed(t *testing.T) {
	calls := map[string]int{}
	var mu sync.Mutex
	// Room for two entries of 100 bytes and their first lines, not three.
	bodyOf := func(path string) string { return path + strings.Repeat("x", 100-len(path)) }
	p := cacheProxy(t, egresspolicy.Rules{Allow: hosts("example.com")}, 550, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls[r.URL.Path]++
		mu.Unlock()
		_, _ = io.WriteString(w, bodyOf(r.URL.Path))
	})
	for _, path := range []string{"/a", "/b", "/a", "/c", "/a", "/b"} {
		if w := proxied(p, http.MethodGet, "http://example.com"+path, bodyOf(path), nil); w.Code != http.StatusOK || w.Body.Len() != 100 {
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
	proxied(p, http.MethodGet, target, "tool archive", nil)
	path := filepath.Join(p.Cache.Dir, keyOf("tool archive"))
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, info.Size()-4); err != nil {
		t.Fatal(err)
	}
	if w := proxied(p, http.MethodGet, target, "tool archive", nil); w.Code != http.StatusOK || w.Body.String() != "tool archive" {
		t.Fatalf("status %d, body %q", w.Code, w.Body.String())
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("upstream asked %d times, want 2", got)
	}
}

// TestCacheKeepsOnlyTheDigest: a body without the digest the request
// names is not kept, and the request is forwarded. So the runner sees what
// the origin serves and refuses it itself.
func TestCacheKeepsOnlyTheDigest(t *testing.T) {
	var calls atomic.Int32
	p := cacheProxy(t, egresspolicy.Rules{Allow: hosts("example.com")}, 1<<20, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, "replaced archive")
	})
	for i := range 2 {
		if w := proxied(p, http.MethodGet, "http://example.com/tool.tar.gz", "tool archive", nil); w.Code != http.StatusOK || w.Body.String() != "replaced archive" {
			t.Fatalf("request %d: status %d, body %q; want the origin's answer forwarded", i, w.Code, w.Body.String())
		}
	}
	if n := entries(t, p.Cache); n != 0 || calls.Load() != 4 {
		t.Fatalf("%d entries after %d upstream requests; want none kept, each fill and each forward asking", n, calls.Load())
	}
}

// TestCacheSharesADigestAcrossURLs: two URLs that serve a body with the
// same digest share its entry, whichever was fetched first.
func TestCacheSharesADigestAcrossURLs(t *testing.T) {
	var calls atomic.Int32
	p := cacheProxy(t, egresspolicy.Rules{Allow: hosts("example.com")}, 1<<20, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, "tool archive")
	})
	for _, target := range []string{"http://example.com/v1/tool.tar.gz", "http://example.com/mirror/tool.tar.gz"} {
		if w := proxied(p, http.MethodGet, target, "tool archive", nil); w.Code != http.StatusOK || w.Body.String() != "tool archive" {
			t.Fatalf("%s: status %d, body %q", target, w.Code, w.Body.String())
		}
	}
	if n := entries(t, p.Cache); n != 1 || calls.Load() != 1 {
		t.Fatalf("%d entries after %d upstream requests; want one of each", n, calls.Load())
	}
}

func TestParseDigest(t *testing.T) {
	sha256Hex, sha512Hex := strings.Repeat("ab", 32), strings.Repeat("cd", 64)
	for in, want := range map[string]bool{
		"sha256:" + sha256Hex: true, "sha512:" + sha512Hex: true,
		"sha256:" + strings.ToUpper(sha256Hex): false, "sha256:" + sha512Hex: false, "md5:" + strings.Repeat("ab", 16): false,
		"sha256:" + strings.Repeat("zz", 32): false, sha256Hex: false, "": false,
	} {
		if _, ok := ParseDigest(in); ok != want {
			t.Errorf("ParseDigest(%q) = %v, want %v", in, ok, want)
		}
	}
}

func TestOpenCache(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cache")
	if _, err := OpenCache(dir, 1<<20); err != nil {
		t.Fatal(err)
	}
	leftover := filepath.Join(dir, ".fill-123")
	kept := filepath.Join(dir, keyOf("tool archive"))
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
