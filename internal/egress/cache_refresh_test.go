package egress

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/home-operations/kritika/internal/egresspolicy"
	"github.com/home-operations/kritika/internal/metrics"
)

const modifiedOn = "Mon, 30 Mar 2026 17:49:19 GMT"

// versionedUpstream serves one body per version, answering a request that
// names the current version 304 and counting what it sent.
type versionedUpstream struct {
	mu                sync.Mutex
	version           string
	private           bool
	bodies, unchanged atomic.Int32
	conditional       []string
}

func (u *versionedUpstream) serve(w http.ResponseWriter, r *http.Request) {
	u.mu.Lock()
	version, private := u.version, u.private
	u.mu.Unlock()
	if private {
		w.Header().Set("Cache-Control", "private")
	}
	if match := r.Header.Get("If-None-Match"); match != "" {
		u.mu.Lock()
		u.conditional = append(u.conditional, match)
		u.mu.Unlock()
		if match == version {
			u.unchanged.Add(1)
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	u.bodies.Add(1)
	w.Header().Set("Etag", version)
	w.Header().Set("Last-Modified", modifiedOn)
	_, _ = fmt.Fprintf(w, "archive %s", version)
}

func (u *versionedUpstream) release(version string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.version = version
}

// expireLocal ages the entry under key in c's directory past any RefreshAfter.
func expireLocal(t *testing.T, c *Cache, key string) {
	t.Helper()
	f, entry, offset, err := c.open(key)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	entry.Stored = 1
	if err := c.store(key, entry, io.NewSectionReader(f, offset, entry.Size)); err != nil {
		t.Fatal(err)
	}
}

func TestCacheRevalidatesExpiredEntry(t *testing.T) {
	up := &versionedUpstream{version: `"v1"`}
	p := cacheProxy(t, egresspolicy.Rules{Allow: hosts("example.com")}, 1<<20, up.serve)
	reg := prometheus.NewPedanticRegistry()
	p.Cache.Observe(metrics.New(reg), slog.New(slog.DiscardHandler))
	const target = "http://example.com/tool.tar.gz"
	key := cacheKey("https://example.com/tool.tar.gz")
	get := func(want string) {
		t.Helper()
		if w := proxied(p, http.MethodGet, target, nil); w.Code != http.StatusOK || w.Body.String() != want {
			t.Fatalf("got %d %q, want %q", w.Code, w.Body.String(), want)
		}
	}
	get(`archive "v1"`)
	expireLocal(t, p.Cache, key)
	// Unchanged: the origin is asked once and the body is not downloaded again.
	get(`archive "v1"`)
	if up.bodies.Load() != 1 || up.unchanged.Load() != 1 {
		t.Fatalf("after revalidation: %d bodies, %d 304s", up.bodies.Load(), up.unchanged.Load())
	}
	// Refreshed: the entry is fresh again and served without the origin.
	get(`archive "v1"`)
	if up.bodies.Load() != 1 || up.unchanged.Load() != 1 {
		t.Fatalf("refreshed entry asked the origin: %d bodies, %d 304s", up.bodies.Load(), up.unchanged.Load())
	}
	// Changed: the new body replaces the entry.
	up.release(`"v2"`)
	expireLocal(t, p.Cache, key)
	get(`archive "v2"`)
	get(`archive "v2"`)
	if up.bodies.Load() != 2 || up.unchanged.Load() != 1 {
		t.Fatalf("after a change: %d bodies, %d 304s", up.bodies.Load(), up.unchanged.Load())
	}
	up.mu.Lock()
	conditional := append([]string{}, up.conditional...)
	up.mu.Unlock()
	if len(conditional) != 2 || conditional[0] != `"v1"` || conditional[1] != `"v1"` {
		t.Fatalf("validators offered = %q", conditional)
	}
	for outcome, want := range map[string]float64{"not_modified": 1, "ok": 2} {
		if got := cacheMetric(t, reg, "kritika_cache_operation_seconds", map[string]string{"operation": "origin_fetch", "outcome": outcome}); got != want {
			t.Errorf("origin_fetch %s = %v, want %v", outcome, got, want)
		}
	}
	if got := cacheMetric(t, reg, "kritika_cache_requests_total", map[string]string{"outcome": "miss"}); got != 3 {
		t.Errorf("misses = %v, want 3", got)
	}
}

// A 304 that asks to stay private, or comes from another resource than
// the body did, does not keep the entry; a redirect whose signature alone
// changed is the same resource.
func TestCacheRevalidationRefusals(t *testing.T) {
	for _, mode := range []string{"private", "moved", "requery", "resigned", "github"} {
		t.Run(mode, func(t *testing.T) {
			up := &versionedUpstream{version: `"v1"`}
			var requests atomic.Int32
			var redirect atomic.Pointer[string]
			rules := egresspolicy.Rules{Allow: hosts("example.com", "objects.githubusercontent.com")}
			p := cacheProxy(t, rules, 1<<20, func(w http.ResponseWriter, r *http.Request) {
				n := requests.Add(1)
				if to := redirect.Load(); to != nil && r.URL.Path == "/tool.tar.gz" {
					// A signed redirect carries a signature and requester that change every time.
					http.Redirect(w, r, strings.ReplaceAll(*to, "N", fmt.Sprint(n)), http.StatusFound)
					return
				}
				up.serve(w, r)
			})
			const target = "http://example.com/tool.tar.gz"
			key := cacheKey("https://example.com/tool.tar.gz")
			switch mode {
			case "requery", "resigned":
				// The asset sits behind a signed redirect from the start.
				to := "https://example.com/blob?id=old&X-Amz-Signature=N&X-Amz-Date=N"
				redirect.Store(&to)
			case "github":
				// GitHub names the requester in the query too.
				to := "https://objects.githubusercontent.com/blob?repo_id=1&X-Amz-Signature=N&actor_id=N&key_id=N"
				redirect.Store(&to)
			}
			if w := proxied(p, http.MethodGet, target, nil); w.Body.String() != `archive "v1"` {
				t.Fatalf("fill: %q", w.Body.String())
			}
			expireLocal(t, p.Cache, key)
			switch mode {
			case "private":
				up.mu.Lock()
				up.private = true
				up.mu.Unlock()
			case "moved":
				to := "https://example.com/other.tar.gz"
				redirect.Store(&to)
			case "requery":
				to := "https://example.com/blob?id=new&X-Amz-Signature=N&X-Amz-Date=N"
				redirect.Store(&to)
			}
			w := proxied(p, http.MethodGet, target, nil)
			if w.Code != http.StatusOK || w.Body.String() != `archive "v1"` {
				t.Fatalf("got %d %q", w.Code, w.Body.String())
			}
			f, entry, _, err := p.Cache.open(key)
			if err != nil {
				t.Fatal(err)
			}
			_ = f.Close()
			got := fmt.Sprintf("%d 304s, %d bodies, via %q, stale %v", up.unchanged.Load(), up.bodies.Load(), entry.Via, entry.Stored == 1)
			var want string
			switch mode {
			case "private":
				// The 304 was refused: the request was forwarded and the entry is still stale.
				want = `1 304s, 2 bodies, via "https://example.com/tool.tar.gz", stale true`
			case "moved":
				// Validators were not offered to the new resource: its body was fetched.
				want = `0 304s, 2 bodies, via "https://example.com/other.tar.gz", stale false`
			case "requery":
				// A query that is not a signature names another resource.
				want = `0 304s, 2 bodies, via "https://example.com/blob?id=new", stale false`
			case "resigned":
				// A new signature on the same resource revalidates.
				want = `1 304s, 1 bodies, via "https://example.com/blob?id=old", stale false`
			case "github":
				want = `1 304s, 1 bodies, via "https://objects.githubusercontent.com/blob?repo_id=1", stale false`
			}
			if got != want {
				t.Fatalf("got %s, want %s", got, want)
			}
		})
	}
}

// A 304 for a copy that has gone since it was looked up costs one more,
// unconditional, fetch rather than a bypass.
func TestCacheRevalidationFetchesWhenCopyIsGone(t *testing.T) {
	up := &versionedUpstream{version: `"v1"`}
	p := cacheProxy(t, egresspolicy.Rules{Allow: hosts("example.com")}, 1<<20, up.serve)
	const target = "https://example.com/tool.tar.gz"
	key := cacheKey(target)
	stale := cacheEntry{URL: target, Stored: 1, Size: 12, ETag: `"v1"`, LastModified: modifiedOn, Via: target}
	if err := p.fill(t.Context(), key, target, stale, false); err != nil {
		t.Fatal(err)
	}
	if up.unchanged.Load() != 1 || up.bodies.Load() != 1 {
		t.Fatalf("%d 304s, %d bodies", up.unchanged.Load(), up.bodies.Load())
	}
	f, entry, err := p.Cache.load(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if body, _ := io.ReadAll(f); string(body) != `archive "v1"` || entry.Stored == 1 {
		t.Fatalf("stored %q at %d", body, entry.Stored)
	}
	if p.Cache.reserved != 0 || p.Cache.filling != 0 {
		t.Fatalf("fill budget left held: %d bytes, %d fills", p.Cache.reserved, p.Cache.filling)
	}
}

// A replica with nothing local revalidates the bucket's stale object, keeps
// the body it fetches from the bucket and rewrites the object's fetch time.
func TestS3CacheRevalidatesSharedEntry(t *testing.T) {
	store, opts := newS3TestStore(t)
	up := &versionedUpstream{version: `"v1"`}
	writer := cacheProxy(t, egresspolicy.Rules{Allow: hosts("example.com")}, 1<<20, up.serve)
	useS3(t, writer.Cache, opts)
	const target = "http://example.com/tool.tar.gz"
	key := cacheKey("https://example.com/tool.tar.gz")
	if w := proxied(writer, http.MethodGet, target, nil); w.Body.String() != `archive "v1"` {
		t.Fatalf("fill: %d %q", w.Code, w.Body.String())
	}
	waitS3Upload(t, writer.Cache, key)
	object := "/cache/instance/v1/" + key
	stored := func(set int64) int64 {
		t.Helper()
		store.mu.Lock()
		defer store.mu.Unlock()
		o := store.objects[object]
		raw, err := base64.StdEncoding.DecodeString(o.headers.Get("X-Amz-Meta-Kritika-Entry"))
		if err != nil {
			t.Fatal(err)
		}
		var entry cacheEntry
		if err := json.Unmarshal(raw, &entry); err != nil {
			t.Fatal(err)
		}
		if set != 0 {
			entry.Stored = set
			raw, _ = json.Marshal(entry)
			o.headers.Set("X-Amz-Meta-Kritika-Entry", base64.StdEncoding.EncodeToString(raw))
			store.objects[object] = o
		}
		return entry.Stored
	}
	stored(1)
	reader := cacheProxy(t, egresspolicy.Rules{Allow: hosts("example.com")}, 1<<20, up.serve)
	useS3(t, reader.Cache, opts)
	if w := proxied(reader, http.MethodGet, target, nil); w.Body.String() != `archive "v1"` {
		t.Fatalf("revalidated: %d %q", w.Code, w.Body.String())
	}
	if up.bodies.Load() != 1 || up.unchanged.Load() != 1 {
		t.Fatalf("%d bodies, %d 304s", up.bodies.Load(), up.unchanged.Load())
	}
	if f, _, _, err := reader.Cache.open(key); err != nil {
		t.Fatalf("no local copy after the refresh: %v", err)
	} else {
		_ = f.Close()
	}
	deadline := time.Now().Add(10 * time.Second)
	for stored(0) == 1 {
		if time.Now().After(deadline) {
			t.Fatal("the refreshed entry was not published")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Another replica finds the object fresh again.
	third := cacheProxy(t, egresspolicy.Rules{Allow: hosts("example.com")}, 1<<20, up.serve)
	useS3(t, third.Cache, opts)
	if w := proxied(third, http.MethodGet, target, nil); w.Body.String() != `archive "v1"` || up.unchanged.Load() != 1 {
		t.Fatalf("after publication: %q, %d 304s", w.Body.String(), up.unchanged.Load())
	}
}
