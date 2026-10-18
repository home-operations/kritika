package egress

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/home-operations/kritika/internal/metrics"
)

// Cache keeps the bodies of upgraded downloads, so a tool every review
// installs is fetched from upstream once rather than once a run. It keeps
// only what any runner could fetch for itself: a GET or HEAD, with no
// query, to a host the rules name as cached, or a locked install's to any
// host they do not keep out, that carries no credential and gets none
// from the proxy, answered by a response that does not ask to stay
// private, so an entry can be handed to every runner that asks for its
// URL. What it serves is checked downstream, mise checking each download
// against the run's lock, so a stale or damaged entry fails an install
// instead of changing one.
type Cache struct {
	// Dir holds one file per URL, named by the URL's hash. It is the
	// cache's alone.
	Dir string
	// MaxBytes bounds what Dir keeps, the least recently used entries
	// going first, and apart from that what the fills under way may write,
	// so Dir holds at most twice it. A body that would not fit is not kept.
	MaxBytes int64
	// RefreshAfter is how long an entry is served before its URL is fetched
	// again, so an asset replaced upstream is not served stale for longer.
	RefreshAfter time.Duration
	// MaxEntryBytes bounds one download, independently of the local budget.
	MaxEntryBytes int64
	s3            *s3Cache
	metrics       *metrics.Metrics
	logger        *slog.Logger
	lastWarning   atomic.Int64

	fills    singleflight.Group
	evicting sync.Mutex
	// filling counts the fills under way and reserved the bytes they may
	// write, guarded by underway.
	underway sync.Mutex
	filling  int
	reserved int64
}

// UncachedProxyUser is the proxy username a runner gives the gateway for
// a request the cache must not answer: the retry of an install that
// failed through it. The gateway marks such a request WithoutCache.
const UncachedProxyUser = "uncached"

type withoutCacheKey struct{}

// WithoutCache marks ctx's request as one the cache must not answer.
func WithoutCache(ctx context.Context) context.Context {
	return context.WithValue(ctx, withoutCacheKey{}, true)
}

// CacheBypassed reports whether ctx's request was marked WithoutCache.
func CacheBypassed(ctx context.Context) bool {
	bypassed, _ := ctx.Value(withoutCacheKey{}).(bool)
	return bypassed
}

// LockedProxyUser is the proxy username a runner gives the gateway for a
// repository's locked install, whose downloads mise checks against the
// lock: the cache may keep and answer such a request from any host the
// rules allow, not only the hosts they name as cached. The gateway marks
// it ForLockedInstall. A command can claim the name too; it then gets what
// the host serves any run, and what it has the cache keep from a host the
// rules do not name as cached is served only to a request that claims the
// name as well.
const LockedProxyUser = "locked"

type lockedInstallKey struct{}

// ForLockedInstall marks ctx's request as a locked install's.
func ForLockedInstall(ctx context.Context) context.Context {
	return context.WithValue(ctx, lockedInstallKey{}, true)
}

// LockedInstall reports whether ctx's request was marked ForLockedInstall.
func LockedInstall(ctx context.Context) bool {
	locked, _ := ctx.Value(lockedInstallKey{}).(bool)
	return locked
}

// OpenCache is a cache in dir, which it creates, checks it can write and
// clears of the fills a previous process left unfinished.
func OpenCache(dir string, maxBytes int64, refreshAfter time.Duration) (*Cache, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("egress: cache: %w", err)
	}
	leftover, err := filepath.Glob(filepath.Join(dir, ".fill-*"))
	if err != nil {
		return nil, fmt.Errorf("egress: cache: %w", err)
	}
	for _, path := range leftover {
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("egress: cache: %w", err)
		}
	}
	probe, err := os.CreateTemp(dir, ".fill-")
	if err != nil {
		return nil, fmt.Errorf("egress: cache: %w", err)
	}
	if err := errors.Join(probe.Close(), os.Remove(probe.Name())); err != nil {
		return nil, fmt.Errorf("egress: cache: %w", err)
	}
	return &Cache{Dir: dir, MaxBytes: maxBytes, RefreshAfter: refreshAfter}, nil
}

// cacheEntry is the first line of an entry's file, before the body.
type cacheEntry struct {
	URL string `json:"url"`
	// Stored is when the body was fetched, in Unix milliseconds.
	Stored       int64  `json:"stored"`
	Size         int64  `json:"size"`
	ContentType  string `json:"contentType,omitempty"`
	ETag         string `json:"etag,omitempty"`
	LastModified string `json:"lastModified,omitempty"`
}

// errExpired is an entry older than the cache's RefreshAfter.
var (
	errExpired      = errors.New("egress: cache entry: expired")
	errFillBusy     = errors.New("egress: cache: all fill slots are occupied")
	errFillCapacity = errors.New("egress: cache: the fills under way hold the cache's bound")
	errEntrySize    = errors.New("egress: cache: body exceeds the entry limit")
	errPrivate      = errors.New("egress: cache: the response asks not to be shared")
	errEncoded      = errors.New("egress: cache: response is encoded")
	errLength       = errors.New("egress: cache: response has no length")
)

const (
	// maxEntryHeader bounds an entry's first line.
	maxEntryHeader = 16 << 10
	// maxRedirects bounds the redirects one fill follows.
	maxRedirects = 10
	// maxFills bounds the fills under way at once, each holding an
	// upstream connection; a request past them is forwarded.
	maxFills = 8
)

// cacheable reports whether r, an upgraded request to host, may be
// answered from the cache: one to a host the rules name as cached, or a
// locked install's to any host they do not name as uncached. A URL with a
// query is not: a signed URL's query is its credential, and a query would
// let one asset be kept under any number of names.
func cacheable(r *http.Request, host string, rules Rules) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if r.Method != http.MethodGet && r.Method != http.MethodHead || r.URL.RawQuery != "" || r.URL.ForceQuery ||
		CacheBypassed(r.Context()) || rules.keptOut(host) {
		return false
	}
	for _, h := range []string{"Authorization", "Cookie"} {
		if _, ok := r.Header[h]; ok {
			return false
		}
	}
	return slices.Contains(rules.Cached, host) || LockedInstall(r.Context())
}

// keptOut reports whether the cache keeps nothing from host, as the rules
// match a name: a host they name as uncached, or one with a credential.
func (r Rules) keptOut(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	_, credentialed := r.Credentials[host]
	return credentialed || slices.Contains(r.Uncached, host)
}

// upstreamURL is the HTTPS URL an upgraded request for u to host goes to.
func upstreamURL(u *url.URL, host string) string {
	target := url.URL{Scheme: upgradedScheme, Host: host, Path: u.Path, RawPath: u.RawPath, RawQuery: u.RawQuery}
	if strings.Contains(host, ":") {
		target.Host = "[" + host + "]"
	}
	return target.String()
}

// cacheKey names the entry for target.
func cacheKey(target string) string {
	sum := sha256.Sum256([]byte(target))
	return hex.EncodeToString(sum[:])
}

// cached answers r, an upgraded request to host the cache may answer, from
// the cache, filling the entry first when there is none. It returns false,
// having written nothing, when the entry cannot be had, and r is then
// forwarded as any other request is.
func (p *Proxy) cached(w http.ResponseWriter, r *http.Request, host string) bool {
	ctx, cancel := context.WithTimeout(r.Context(), upgradeTimeout)
	defer cancel()
	target := upstreamURL(r.URL, host)
	key := cacheKey(target)
	outcome, reason := "bypass", "fill_error"
	defer func() { p.Cache.metrics.CacheRequest(outcome, reason) }()
	// A failed or circuit-open S3 lookup is a miss too: the replica then
	// fills its own directory, as the filesystem backend does, rather than
	// forwarding every download for as long as the bucket is unreachable.
	f, entry, err := p.Cache.load(ctx, key)
	if err != nil {
		fill := p.Cache.fills.DoChan(key, func() (any, error) {
			// A fill that finished between this request's miss and its
			// joining is not repeated. Only this directory is checked: the
			// bucket was asked a moment ago, and may be unreachable. An
			// entry over the limit, kept from before it was lowered, does
			// not count: load refuses it, so a fill must replace it.
			if f, entry, _, err := p.Cache.open(key); err == nil {
				_ = f.Close()
				if p.Cache.MaxEntryBytes <= 0 || entry.Size <= p.Cache.MaxEntryBytes {
					return nil, nil
				}
			}
			return nil, p.fill(r.Context(), key, target)
		})
		select {
		case <-r.Context().Done():
			reason = cacheCanceled
			return true
		case res := <-fill:
			if res.Err != nil {
				reason = fillBypassReason(res.Err)
				p.Logger.Debug("egress cache bypassed", "host", host, "path", r.URL.Path, "error", res.Err)
				return false
			}
		}
		reason = "entry_unavailable"
		if f, entry, err = p.Cache.load(ctx, key); err != nil {
			p.Logger.Debug("egress cache bypassed", "host", host, "path", r.URL.Path, "error", err)
			return false
		}
		outcome = "miss"
	} else {
		outcome = f.source + "_hit"
	}
	defer func() { _ = f.Close() }()
	h := w.Header()
	for name, value := range map[string]string{"Content-Type": entry.ContentType, "Etag": entry.ETag} {
		if value != "" {
			h.Set(name, value)
		}
	}
	// ServeContent answers date conditions by the modification time it
	// is given, and writes it as Last-Modified.
	modified, err := http.ParseTime(entry.LastModified)
	if err != nil && entry.LastModified != "" {
		h.Set("Last-Modified", entry.LastModified)
	}
	// Bounded as a forwarded response is, so a client that stops reading
	// cannot hold an entry's file, evicted or not, open for longer. The
	// gateway's writer supports the deadline; one that does not leaves the
	// response unbounded, as forwarding would be without its context.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(upgradeTimeout))
	p.observe("http", OutcomeAllowed)
	p.Logger.Debug("egress cache", "outcome", outcome, "host", host, "method", r.Method, "path", r.URL.Path, "bytes", entry.Size)
	p.serveCached(w, r, key, f, modified)
	return true
}

// fill fetches target, following its redirects, and keeps its body under
// key. Every request waiting on key waits on this one fetch, so it does
// not end with the request that started it; it is bounded as one
// upgraded request is, and is not made at all, leaving the request to be
// forwarded, while maxFills are under way or their bytes hold MaxBytes.
// The fetch is the gateway's own, with a User-Agent and Accept of its
// own, so which representation an entry holds does not depend on the
// request that happened to start it.
func (p *Proxy) fill(ctx context.Context, key, target string) error {
	if !p.Cache.begin() {
		return errFillBusy
	}
	var reserved int64
	defer func() { p.Cache.end(reserved) }()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), upgradeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return fmt.Errorf("egress: cache: %w", err)
	}
	req.Header.Set("User-Agent", "kritika")
	req.Header.Set("Accept", "*/*")
	// The body is kept as the server sends it, not as a transport decodes it.
	req.Header.Set("Accept-Encoding", "identity")
	client := &http.Client{Transport: p.httpTransport(), CheckRedirect: p.checkRedirect}
	started := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		p.Cache.metrics.CacheOperation("origin_fetch", operationResult(err), time.Since(started), 0)
		return fmt.Errorf("egress: cache: %w", err)
	}
	body := &countedReader{Reader: resp.Body}
	originResult := "skipped"
	originDone := false
	finishOrigin := func() {
		if !originDone {
			p.Cache.metrics.CacheOperation("origin_fetch", originResult, time.Since(started), body.bytes)
			originDone = true
		}
	}

	defer finishOrigin()
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode != http.StatusOK:
		originResult = OutcomeError
		return fmt.Errorf("egress: cache: %s", resp.Status)
	case private(resp.Header):
		return errPrivate
	case resp.Header.Get("Content-Encoding") != "":
		return errEncoded
	case resp.ContentLength < 0:
		return errLength
	case resp.ContentLength > p.Cache.MaxBytes:
		return errEntrySize
	case p.Cache.MaxEntryBytes > 0 && resp.ContentLength > p.Cache.MaxEntryBytes:
		return errEntrySize
	case !p.Cache.reserve(resp.ContentLength):
		return errFillCapacity
	}
	reserved = resp.ContentLength
	entry := cacheEntry{URL: target, Stored: time.Now().UnixMilli(), Size: resp.ContentLength, ContentType: resp.Header.Get("Content-Type"),
		ETag: resp.Header.Get("Etag"), LastModified: resp.Header.Get("Last-Modified")}
	if err := p.Cache.store(key, entry, body); err != nil {
		originResult = operationResult(err)
		return err
	}
	originResult = "ok"
	finishOrigin()
	if p.Cache.s3 != nil {
		p.Cache.queueUpload(key)
	}
	p.Logger.Debug("egress cache fill", "url", target, "from", resp.Request.URL.Host, "bytes", entry.Size,
		"elapsed", time.Since(started).Round(time.Millisecond))
	return nil
}

// checkRedirect lets a fill follow a redirect only where the runner's own
// request could go, over HTTPS on 443 to a host the rules admit and the
// cache does not keep out, and only as a request with no credential, from
// a response that does not ask to stay private: Go's client turns a URL's
// userinfo into Basic authorization.
func (p *Proxy) checkRedirect(req *http.Request, via []*http.Request) error {
	switch {
	case len(via) >= maxRedirects:
		return fmt.Errorf("egress: cache: stopped after %d redirects", maxRedirects)
	case req.URL.Scheme != upgradedScheme:
		return fmt.Errorf("egress: cache: redirect to %s", req.URL.Scheme)
	case req.URL.User != nil:
		return errors.New("egress: cache: redirect to a URL with credentials")
	case req.Response != nil && private(req.Response.Header):
		return errors.New("egress: cache: a redirect asks not to be shared")
	case req.URL.Port() != "" && req.URL.Port() != "443":
		return &refusal{req.URL.Host, "only port 443 is served"}
	}
	rules := p.Rules()
	if reason := rules.refuses(req.URL.Hostname()); reason != "" {
		return &refusal{req.URL.Host, reason}
	}
	if rules.keptOut(req.URL.Hostname()) {
		return fmt.Errorf("egress: cache: redirect to %s, which the cache keeps nothing from", req.URL.Host)
	}
	return nil
}

// private reports whether a response's headers keep it from being shared
// between runs: a cookie it sets, or a Cache-Control of private or
// no-store. no-cache, which a GitHub release download's redirect carries,
// only asks a cache to check before reuse.
func private(h http.Header) bool {
	if len(h.Values("Set-Cookie")) > 0 {
		return true
	}
	for _, value := range h.Values("Cache-Control") {
		for directive := range strings.SplitSeq(value, ",") {
			name, _, _ := strings.Cut(strings.TrimSpace(directive), "=")
			if strings.EqualFold(name, "private") || strings.EqualFold(name, "no-store") {
				return true
			}
		}
	}
	return false
}

// begin claims a place for a fill among the maxFills under way at once,
// false when there is none.
func (c *Cache) begin() bool {
	c.underway.Lock()
	defer c.underway.Unlock()
	if c.filling >= maxFills {
		return false
	}
	c.filling++
	c.metrics.CacheFill(1)
	return true
}

// reserve claims size bytes for a fill under way, false when the fills
// under way would then hold more than MaxBytes.
func (c *Cache) reserve(size int64) bool {
	c.underway.Lock()
	defer c.underway.Unlock()
	if c.reserved+size > c.MaxBytes {
		return false
	}
	c.reserved += size
	return true
}

// end gives back a fill's place and the bytes it reserved.
func (c *Cache) end(reserved int64) {
	c.underway.Lock()
	defer c.underway.Unlock()
	c.filling--
	c.metrics.CacheFill(-1)
	c.reserved -= reserved
}

// open opens the entry under key and marks it used, returning its file,
// what its first line records and where its body starts. An entry whose
// file does not hold what its first line says is removed, to be fetched
// again; one past RefreshAfter is left for the fill that replaces it.
func (c *Cache) open(key string) (*os.File, cacheEntry, int64, error) {
	path := filepath.Join(c.Dir, key)
	f, err := os.Open(path)
	if err != nil {
		return nil, cacheEntry{}, 0, err
	}
	entry, offset, err := readEntry(f)
	if err != nil {
		info, serr := f.Stat()
		_ = f.Close()
		// Only this file: a fill may have put a sound one in its place.
		if cur, cerr := os.Stat(path); serr == nil && cerr == nil && os.SameFile(info, cur) {
			_ = os.Remove(path)
		}
		return nil, cacheEntry{}, 0, err
	}
	if c.RefreshAfter > 0 && time.Since(time.UnixMilli(entry.Stored)) > c.RefreshAfter {
		_ = f.Close()
		return nil, cacheEntry{}, 0, errExpired
	}
	now := time.Now()
	// Eviction ranks entries by this time; failing to set it only ages
	// this entry.
	_ = os.Chtimes(path, now, now)
	return f, entry, offset, nil
}

// readEntry reads the first line of f, an entry's file, and checks that
// the body after it is as long as the line says.
func readEntry(f *os.File) (cacheEntry, int64, error) {
	line, err := bufio.NewReader(io.LimitReader(f, maxEntryHeader)).ReadBytes('\n')
	if err != nil {
		return cacheEntry{}, 0, fmt.Errorf("egress: cache entry: %w", err)
	}
	var e cacheEntry
	if err := json.Unmarshal(line, &e); err != nil {
		return cacheEntry{}, 0, fmt.Errorf("egress: cache entry: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		return cacheEntry{}, 0, fmt.Errorf("egress: cache entry: %w", err)
	}
	if info.Size() != int64(len(line))+e.Size {
		return cacheEntry{}, 0, errors.New("egress: cache entry: body is not the recorded size")
	}
	return e, int64(len(line)), nil
}

// store writes body, entry.Size bytes of it, as the entry under key, in
// place of any entry there, then evicts what no longer fits.
func (c *Cache) store(key string, entry cacheEntry, body io.Reader) error {
	line, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("egress: cache: %w", err)
	}
	tmp, err := os.CreateTemp(c.Dir, ".fill-")
	if err != nil {
		return fmt.Errorf("egress: cache: %w", err)
	}
	// Gone already once renamed into place.
	defer func() { _ = os.Remove(tmp.Name()) }()
	_, err = tmp.Write(append(line, '\n'))
	var n int64
	if err == nil {
		n, err = io.Copy(tmp, io.LimitReader(body, entry.Size+1))
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	switch {
	case err != nil:
		return fmt.Errorf("egress: cache: %w", err)
	case n != entry.Size:
		return fmt.Errorf("egress: cache: body is %d bytes, not %d", n, entry.Size)
	}
	if err := os.Rename(tmp.Name(), filepath.Join(c.Dir, key)); err != nil {
		return fmt.Errorf("egress: cache: %w", err)
	}
	c.evict(key)
	return nil
}

// evict removes the least recently used entries until Dir holds no more
// than MaxBytes, sparing keep, the entry just stored. A request already
// serving an evicted entry reads it to the end from its open file. Fills
// that a crash left behind go once no fill could still be writing them.
// Eviction is best effort: an entry it cannot remove stays until the next.
func (c *Cache) evict(keep string) {
	c.evicting.Lock()
	defer c.evicting.Unlock()
	dirents, err := os.ReadDir(c.Dir)
	if err != nil {
		return
	}
	type file struct {
		name string
		size int64
		used time.Time
	}
	var files []file
	var total int64
	for _, d := range dirents {
		info, err := d.Info()
		if err != nil {
			continue
		}
		if strings.HasPrefix(d.Name(), ".") {
			if time.Since(info.ModTime()) > 2*upgradeTimeout {
				_ = os.Remove(filepath.Join(c.Dir, d.Name()))
			}
			continue
		}
		total += info.Size()
		if d.Name() != keep {
			files = append(files, file{d.Name(), info.Size(), info.ModTime()})
		}
	}
	slices.SortFunc(files, func(a, b file) int { return a.used.Compare(b.used) })
	evictions := 0
	for _, f := range files {
		if total <= c.MaxBytes {
			break
		}
		if os.Remove(filepath.Join(c.Dir, f.name)) == nil {
			total -= f.size
			evictions++
		}
	}
	c.metrics.CacheLocal(total, c.MaxBytes, evictions)
}
