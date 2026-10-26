package egress

import (
	"bufio"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
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

	"github.com/home-operations/kritika/internal/egresspolicy"
	"github.com/home-operations/kritika/internal/metrics"
)

// Cache keeps the bodies of upgraded downloads a runner names the digest
// of, by that digest, so a tool every review installs is fetched from
// upstream once rather than once a run. A fill hashes what it fetches and
// keeps it only when it has the digest asked for, so an entry is the body
// any request naming that digest accepts, whichever request fetched it
// first. It keeps only what any runner could fetch for itself: a GET or
// HEAD, with no query, to a host the rules do not keep out, that carries no
// credential and gets none from the proxy, answered by a response that does
// not ask to stay private.
type Cache struct {
	// Dir holds one file per digest. It is the cache's alone.
	Dir string
	// MaxBytes bounds what Dir keeps, the least recently used entries
	// going first, and apart from that what the fills under way may write,
	// so Dir holds at most twice it. A body that would not fit is not kept.
	MaxBytes int64
	// MaxEntryBytes bounds one download, independently of the local budget.
	MaxEntryBytes int64
	s3            *s3Cache
	metrics       *metrics.Metrics
	logger        *slog.Logger
	lastWarning   atomic.Int64

	fills    singleflight.Group
	evicting sync.Mutex
	// entries serializes the fills of one entry, so a fill that finished
	// between a request's miss and its own fill is not made again.
	entries keyLocks
	// filling counts the fills under way and reserved the bytes they may
	// write, guarded by underway.
	underway sync.Mutex
	filling  int
	reserved int64
}

// Digest is what a download's body must hash to: the algorithm, sha256
// or sha512, and the hash in lowercase hex.
type Digest struct {
	Algorithm, Hex string
}

// digestSizes are the algorithms a Digest may name, by their hash's size.
var digestSizes = map[string]int{"sha256": sha256.Size, "sha512": sha512.Size}

// ParseDigest reads s, "<algorithm>:<hex>", as a runner names the digest
// a download must have; false for anything else.
func ParseDigest(s string) (Digest, bool) {
	algorithm, value, _ := strings.Cut(s, ":")
	size := digestSizes[algorithm]
	if size == 0 || len(value) != 2*size || strings.ToLower(value) != value {
		return Digest{}, false
	}
	if _, err := hex.DecodeString(value); err != nil {
		return Digest{}, false
	}
	return Digest{Algorithm: algorithm, Hex: value}, true
}

func (d Digest) String() string { return d.Algorithm + ":" + d.Hex }

// key names d's entry.
func (d Digest) key() string { return d.Algorithm + "-" + d.Hex }

func (d Digest) hash() hash.Hash {
	if d.Algorithm == "sha512" {
		return sha512.New()
	}
	return sha256.New()
}

type digestKey struct{}

// ForDigest marks ctx's request as one for a body with d, which the cache
// keeps under d.
func ForDigest(ctx context.Context, d Digest) context.Context {
	return context.WithValue(ctx, digestKey{}, d)
}

// DigestOf is the digest ctx's request was marked ForDigest with.
func DigestOf(ctx context.Context) (Digest, bool) {
	d, ok := ctx.Value(digestKey{}).(Digest)
	return d, ok
}

// OpenCache is a cache in dir, which it creates, checks it can write and
// clears of the fills a previous process left unfinished.
func OpenCache(dir string, maxBytes int64) (*Cache, error) {
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
	return &Cache{Dir: dir, MaxBytes: maxBytes}, nil
}

// cacheEntry is the first line of an entry's file, before the body.
type cacheEntry struct {
	// URL is where the body was fetched from, for the log.
	URL         string `json:"url"`
	Digest      string `json:"digest"`
	Size        int64  `json:"size"`
	ContentType string `json:"contentType,omitempty"`
}

var (
	errFillBusy     = errors.New("egress: cache: all fill slots are occupied")
	errFillCapacity = errors.New("egress: cache: the fills under way hold the cache's bound")
	errEntrySize    = errors.New("egress: cache: body exceeds the entry limit")
	errPrivate      = errors.New("egress: cache: the response asks not to be shared")
	errEncoded      = errors.New("egress: cache: response is encoded")
	errLength       = errors.New("egress: cache: response has no length")
	errDigest       = errors.New("egress: cache: the body does not have the digest asked for")
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
// answered from the cache: one marked ForDigest, to any host the rules do
// not name as uncached. A URL with a query is not: a signed URL's query is
// its credential.
func cacheable(r *http.Request, host string, rules egresspolicy.Rules) bool {
	_, named := DigestOf(r.Context())
	if !named || r.Method != http.MethodGet && r.Method != http.MethodHead || r.URL.RawQuery != "" || r.URL.ForceQuery ||
		keptOut(rules, host) {
		return false
	}
	for _, h := range []string{"Authorization", "Cookie"} {
		if _, ok := r.Header[h]; ok {
			return false
		}
	}
	return true
}

// keptOut reports whether the cache keeps nothing from host, as the rules
// match a name: a host they name as uncached, or one with a credential.
func keptOut(r egresspolicy.Rules, host string) bool {
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

// cached answers r, an upgraded request to host the cache may answer, from
// the entry for its digest, filling it first when there is none. It
// returns false, having written nothing, when the entry cannot be had, and
// r is then forwarded as any other request is.
func (p *Proxy) cached(w http.ResponseWriter, r *http.Request, host string) bool {
	ctx, cancel := context.WithTimeout(r.Context(), upgradeTimeout)
	defer cancel()
	d, _ := DigestOf(r.Context())
	key := d.key()
	outcome, reason := "bypass", "fill_error"
	defer func() { p.Cache.metrics.CacheRequest(outcome, reason) }()
	// A failed or circuit-open S3 lookup is a miss too: the replica then
	// fills its own directory, as the filesystem backend does, rather than
	// forwarding every download for as long as the bucket is unreachable.
	f, entry, err := p.Cache.load(ctx, key)
	if err != nil {
		// Requests for one digest share one fill, from whichever URL the
		// first named: any body that has the digest is the one each asks for.
		target := upstreamURL(r.URL, host)
		fill := p.Cache.fills.DoChan(key, func() (any, error) {
			return nil, p.fill(r.Context(), key, d, target)
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
	if entry.ContentType != "" {
		h.Set("Content-Type", entry.ContentType)
	}
	h.Set("Etag", `"`+d.String()+`"`)
	// Bounded as a forwarded response is, so a client that stops reading
	// cannot hold an entry's file, evicted or not, open for longer. The
	// gateway's writer supports the deadline; one that does not leaves the
	// response unbounded, as forwarding would be without its context.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(upgradeTimeout))
	p.observe("http", OutcomeAllowed)
	p.Logger.Debug("egress cache", "outcome", outcome, "host", host, "method", r.Method, "path", r.URL.Path, "bytes", entry.Size)
	p.serveCached(w, r, key, f)
	return true
}

// fill fetches target, following its redirects, and keeps its body under
// key, d's, when it has d. Every request waiting on key waits on this one
// fetch, so it does not end with the request that started it; it is
// bounded as one upgraded request is, and is not made at all, leaving the
// request to be forwarded, while maxFills are under way or their bytes hold
// MaxBytes. The fetch is the gateway's own, with a User-Agent and Accept of
// its own.
func (p *Proxy) fill(ctx context.Context, key string, d Digest, target string) error {
	lock := p.Cache.entries.lock(key)
	defer p.Cache.entries.unlock(key, lock)
	// A fill that finished between a request's miss and this one is not
	// repeated. Only this directory is checked: the bucket was asked a
	// moment ago, and may be unreachable. An entry over the limit, kept from
	// before it was lowered, does not count: load refuses it, so a fill
	// must replace it.
	if f, entry, _, err := p.Cache.open(key); err == nil {
		_ = f.Close()
		if p.Cache.MaxEntryBytes <= 0 || entry.Size <= p.Cache.MaxEntryBytes {
			return nil
		}
	}
	if !p.Cache.begin() {
		return errFillBusy
	}
	var reserved int64
	defer func() { p.Cache.end(reserved) }()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), upgradeTimeout)
	defer cancel()
	started := time.Now()
	resp, err := p.fetch(ctx, target)
	if err != nil {
		return err
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
	entry := cacheEntry{URL: target, Digest: d.String(), Size: resp.ContentLength, ContentType: resp.Header.Get("Content-Type")}
	h := d.hash()
	err = p.Cache.store(key, entry, io.TeeReader(body, h), func() error {
		if hex.EncodeToString(h.Sum(nil)) != d.Hex {
			return errDigest
		}
		return nil
	})
	if err != nil {
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

// fetch asks the origin for target and records a request that got no
// response.
func (p *Proxy) fetch(ctx context.Context, target string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("egress: cache: %w", err)
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
		return nil, fmt.Errorf("egress: cache: %w", err)
	}
	return resp, nil
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
	if reason := rules.Refuses(req.URL.Hostname()); reason != "" {
		return &refusal{req.URL.Host, reason}
	}
	if keptOut(rules, req.URL.Hostname()) {
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
// again.
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
// place of any entry there, then evicts what no longer fits. check, when
// set, is why what body held may not be kept, asked once it is all read.
func (c *Cache) store(key string, entry cacheEntry, body io.Reader, check func() error) error {
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
	if check != nil {
		if err := check(); err != nil {
			return err
		}
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
