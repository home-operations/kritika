package runner

import (
	"cmp"
	"crypto/sha256"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/home-operations/kritika/internal/egress"
	"github.com/home-operations/kritika/internal/repoconfig"
)

func TestCacheReplacements(t *testing.T) {
	for _, tt := range []struct {
		name   string
		cached []string
		want   []urlReplacement
	}{
		{"none cached", nil, nil},
		{"each host to plain HTTP", []string{"get.helm.sh", "github.com"}, []urlReplacement{
			{From: "https://get.helm.sh/", To: "http://get.helm.sh/"}, {From: "https://github.com/", To: "http://github.com/"},
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := cacheReplacements(tt.cached); !slices.Equal(got, tt.want) {
				t.Fatalf("cacheReplacements = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestWithProxyUser(t *testing.T) {
	env := []string{
		"PATH=/usr/bin", "HTTP_PROXY=http://:krk_token@gateway.example:8082", "https_proxy=http://:krk_token@gateway.example:8082",
		"NO_PROXY=gateway.example", "OTHER=http://:krk_token@gateway.example:8082",
	}
	got := withProxyUser(env, egress.UncachedProxyUser)
	want := []string{
		"PATH=/usr/bin", "HTTP_PROXY=http://uncached:krk_token@gateway.example:8082", "https_proxy=http://uncached:krk_token@gateway.example:8082",
		"NO_PROXY=gateway.example", "OTHER=http://:krk_token@gateway.example:8082",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("withProxyUser = %v, want %v", got, want)
	}
	if env[1] != "HTTP_PROXY=http://:krk_token@gateway.example:8082" {
		t.Fatalf("withProxyUser changed its argument: %v", env)
	}
}

// TestMiseInstallRetriesWithRefill: a locked install that fails through
// the gateway's cache, here because what the gateway served from it does
// not match the lock, is made once more with the proxy's username asking
// the gateway to fetch again, over the same plain HTTP URLs, and the tool
// comes from upstream.
func TestMiseInstallRetriesWithRefill(t *testing.T) {
	bin, dataDir := hostMise(t)
	t.Setenv("MISE_SYSTEM_DATA_DIR", dataDir)
	systemMiseConfig(t, testMiseConfig)
	payload := []byte("#!/bin/sh\necho upstream\n")
	var mu sync.Mutex
	var served []string
	// The gateway: a cached copy for a locked install's request, the
	// upstream file for one the proxy's username marks as its retry. A
	// CONNECT, which mise makes for its versions host, is refused.
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodConnect || r.URL.Host != "tools.example.invalid" {
			http.Error(w, "refused", http.StatusForbidden)
			return
		}
		// BasicAuth reads Authorization; the proxy's credentials come as
		// Proxy-Authorization.
		r.Header.Set("Authorization", r.Header.Get("Proxy-Authorization"))
		user, _, _ := r.BasicAuth()
		mu.Lock()
		served = append(served, user)
		mu.Unlock()
		if user == egress.UncachedProxyUser {
			_, _ = w.Write(payload)
			return
		}
		_, _ = io.WriteString(w, "#!/bin/sh\necho stale\n")
	}))
	defer gateway.Close()
	repo := repoToolLock(t, "https://tools.example.invalid/repo-tool", payload)
	home := t.TempDir()
	proxy := strings.Replace(gateway.URL, "://", "://:krk_token@", 1)
	base := []string{"HOME=" + home, "PATH=/usr/bin:/bin", "HTTP_PROXY=" + proxy, "HTTPS_PROXY=" + proxy}
	var logs strings.Builder
	m, _, err := newMiseTools(t.Context(), bin, home, base, nil, true, repo, nil,
		Secrets{Redacted: &Redactions{}}, slog.New(slog.NewTextHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	path, _, err := m.resolve(t.Context(), "repo-tool")
	if err != nil {
		t.Fatalf("resolve: %v\nlog:\n%s", err, logs.String())
	}
	if out, err := exec.Command(path).Output(); err != nil || string(out) != "upstream\n" {
		t.Fatalf("installed tool printed %q, %v", out, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if first, retry := slices.Index(served, egress.LockedProxyUser), slices.Index(served, egress.UncachedProxyUser); first < 0 || retry < first {
		t.Fatalf("the gateway was asked as %q: want the cache's answer, then a retry asking it to fetch again", served)
	}
	for _, want := range []string{"installing it again from upstream", "gateway_cache=false"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("log lacks %q:\n%s", want, logs.String())
		}
	}
}

func TestLockHosts(t *testing.T) {
	for _, tt := range []struct {
		name string
		urls []string
		want []string
	}{
		{"each host once, sorted", []string{
			"https://github.com/home-operations/flate/releases/download/v0.6.5/flate_0.6.5_linux_amd64.tar.gz",
			"https://get.helm.sh/helm-v4.3.0-linux-amd64.tar.gz",
			"https://github.com/google/go-containerregistry/releases/download/v0.22.1/go-containerregistry_Linux_x86_64.tar.gz",
		}, []string{"get.helm.sh", "github.com"}},
		{"in lowercase, on the default port", []string{"https://GitHub.com:443/a/b"}, []string{"github.com"}},
		{"none the cache cannot keep, or that mise sends its token", []string{
			"http://tools.example.com/plain", "https://tools.example.com:8443/port", "https://user:pass@tools.example.com/credentials",
			"https://tools.example.com/signed?X-Amz-Signature=abc", "https://tools.example.com/empty?",
			"https://api.github.com/repos/a/b/releases/assets/1", "https://raw.githubusercontent.com/a/b/main/tool",
			"https://[2001:db8::1]/tool", "https://api.github.com./repos/a/b/releases/assets/1", "https://github.com./a/b", "::not a url",
		}, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := lockHosts(tt.urls); !slices.Equal(got, tt.want) {
				t.Fatalf("lockHosts = %v, want %v", got, tt.want)
			}
		})
	}
}

// repoToolLock is a repository's config and lock for http:repo-tool,
// pinned at url with payload's checksum.
func repoToolLock(t *testing.T, url string, payload []byte) *repoMiseConfig {
	t.Helper()
	doc := fmt.Sprintf("[tools]\n\"http:repo-tool\" = { version = \"1.0.0\", url = %q, filter_bins = \"repo-tool\" }\n", url)
	lock := fmt.Sprintf(`lockfile_version = 3
[[tools."http:repo-tool"]]
version = "1.0.0"
backend = "http:repo-tool"
specifiers = ["1.0.0"]
[tools."http:repo-tool"."platforms.%s"]
url = %q
checksum = "sha256:%x"
`, misePlatform(), url, sha256.Sum256(payload))
	repo, _, err := keptRepoMiseConfig(repoconfig.Files{RepoMiseConfig: doc, RepoMiseLock: lock})
	if err != nil {
		t.Fatal(err)
	}
	return repo
}

// TestMiseToolsLockedInstalls: while the gateway keeps downloads and is
// the runner's HTTP proxy, a repository's locked install has a rule for
// each host its lock pins downloads on and the proxy's username saying it
// is locked; every other install has none of those rules.
func TestMiseToolsLockedInstalls(t *testing.T) {
	bin, dataDir := hostMise(t)
	t.Setenv("MISE_SYSTEM_DATA_DIR", dataDir)
	systemMiseConfig(t, "[tools]\nyq = \""+testYqVersion+"\"\n")
	repo := repoToolLock(t, "https://tools.example.invalid/repo-tool", nil)
	const (
		proxy    = "http://:krk_token@gateway.example:8082"
		lockRule = `"https://tools.example.invalid/" = "http://tools.example.invalid/"`
	)
	for _, tt := range []struct {
		name               string
		proxy, cacheLocked bool
	}{
		{"with a cache", true, true},
		{"without a proxy", false, true},
		{"without a cache", true, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			base := []string{"PATH=/usr/bin", "HOME=" + home}
			if tt.proxy {
				base = append(base, "HTTP_PROXY="+proxy, "http_proxy="+proxy, "HTTPS_PROXY="+proxy, "https_proxy="+proxy)
			}
			m, _, err := newMiseTools(t.Context(), bin, home, base, nil, tt.cacheLocked, repo, nil,
				Secrets{Redacted: &Redactions{}}, slog.New(slog.DiscardHandler))
			if err != nil {
				t.Fatal(err)
			}
			if !tt.proxy || !tt.cacheLocked {
				if m.repoEnv != nil {
					t.Fatalf("a locked install has an environment of its own: %v", m.repoEnv)
				}
				return
			}
			if got := urlReplacementsSetting(t, m, m.repoEnv); !strings.Contains(got, lockRule) {
				t.Fatalf("a locked install's url_replacements = %q: want the lock's hosts", got)
			}
			for _, kv := range []string{"HTTP_PROXY=http://locked:krk_token@gateway.example:8082", "https_proxy=http://locked:krk_token@gateway.example:8082"} {
				if !slices.Contains(m.repoEnv, kv) {
					t.Errorf("a locked install's environment lacks %s: %v", kv, m.repoEnv)
				}
			}
			if got := urlReplacementsSetting(t, m, m.env); got != "" {
				t.Errorf("any other install's url_replacements = %q: want none", got)
			}
		})
	}
}

// TestMiseLockedInstallThroughCache: a repository's tool, whose lock pins
// an https:// download, reaches the gateway as a plain HTTP request whose
// proxy username says it is a locked install's; should the install fail
// there, it is made once more over the same plain HTTP request, its proxy
// username asking the gateway to fetch again rather than answer from the
// cache.
func TestMiseLockedInstallThroughCache(t *testing.T) {
	bin, dataDir := hostMise(t)
	t.Setenv("MISE_SYSTEM_DATA_DIR", dataDir)
	systemMiseConfig(t, testMiseConfig)
	payload := []byte("#!/bin/sh\necho upstream\n")
	repo := repoToolLock(t, "https://tools.example.invalid/repo-tool", payload)
	const stale = "#!/bin/sh\necho stale\n"
	for _, tt := range []struct {
		name string
		// served is what the gateway answers a locked install's plain
		// HTTP request with, and refilled what it answers the retry's.
		served, refilled string
	}{
		{"from the cache", string(payload), ""},
		{"around a stale entry", stale, string(payload)},
		{"stale from upstream too", stale, stale},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var mu sync.Mutex
			var seen []string
			// The gateway answers a plain HTTP request for the tool, from
			// upstream when the proxy's username asks it to fetch again,
			// and refuses every CONNECT, which mise also makes for its
			// versions host; it notes how each request for the tool's
			// host came, and as which user.
			gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				r.Header.Set("Authorization", r.Header.Get("Proxy-Authorization"))
				user, _, _ := r.BasicAuth()
				if host := cmp.Or(r.URL.Hostname(), r.Host); strings.HasPrefix(host, "tools.example.invalid") {
					mu.Lock()
					seen = append(seen, r.Method+" "+user)
					mu.Unlock()
				}
				if r.Method == http.MethodConnect || r.URL.Host != "tools.example.invalid" {
					http.Error(w, "refused", http.StatusForbidden)
					return
				}
				if user == egress.UncachedProxyUser {
					_, _ = io.WriteString(w, tt.refilled)
					return
				}
				_, _ = io.WriteString(w, tt.served)
			}))
			defer gateway.Close()
			home := t.TempDir()
			proxy := strings.Replace(gateway.URL, "://", "://:krk_token@", 1)
			base := []string{"HOME=" + home, "PATH=/usr/bin:/bin", "HTTP_PROXY=" + proxy, "HTTPS_PROXY=" + proxy}
			var logs strings.Builder
			m, _, err := newMiseTools(t.Context(), bin, home, base, nil, true, repo, nil,
				Secrets{Redacted: &Redactions{}}, slog.New(slog.NewTextHandler(&logs, nil)))
			if err != nil {
				t.Fatal(err)
			}
			path, _, err := m.resolve(t.Context(), "repo-tool")
			mu.Lock()
			defer mu.Unlock()
			if len(seen) == 0 || seen[0] != "GET locked" {
				t.Fatalf("the gateway was asked %q: want a locked install's plain HTTP request first\nlog:\n%s", seen, logs.String())
			}
			if tt.refilled == stale {
				// The refill failed the lock too: the last attempt goes over
				// HTTPS, around the cache, which this gateway refuses.
				if err == nil {
					t.Fatalf("resolve installed %s from two stale answers and a refused CONNECT", path)
				}
				retry, direct := slices.Index(seen, "GET uncached"), slices.Index(seen, "CONNECT ")
				if retry < 0 || direct < retry {
					t.Fatalf("the gateway was asked %q: want a refill, then a plain CONNECT around the cache", seen)
				}
				if !strings.Contains(logs.String(), "installing it without the cache") {
					t.Errorf("log lacks the last retry:\n%s", logs.String())
				}
				return
			}
			if err != nil {
				t.Fatalf("resolve: %v\nlog:\n%s", err, logs.String())
			}
			if out, err := exec.Command(path).Output(); err != nil || string(out) != "upstream\n" {
				t.Fatalf("installed tool printed %q, %v", out, err)
			}
			if tt.refilled == "" {
				if slices.Contains(seen, "GET uncached") || !strings.Contains(logs.String(), "gateway_cache=true") {
					t.Errorf("a sound entry was retried: asked %q\nlog:\n%s", seen, logs.String())
				}
				return
			}
			if retry := slices.Index(seen, "GET uncached"); retry < 0 || slices.Contains(seen, "CONNECT uncached") {
				t.Fatalf("the gateway was asked %q: want the retry as a plain HTTP request asking it to fetch again", seen)
			}
			for _, want := range []string{"installing it again from upstream", "gateway_cache=false"} {
				if !strings.Contains(logs.String(), want) {
					t.Errorf("log lacks %q:\n%s", want, logs.String())
				}
			}
		})
	}
}

// urlReplacementsSetting is mise's url_replacements setting under env, as
// mise prints it, or empty when env sets none, which mise refuses to print.
func urlReplacementsSetting(t *testing.T, m *miseTools, env []string) string {
	t.Helper()
	if !slices.ContainsFunc(env, func(kv string) bool { return strings.HasPrefix(kv, "MISE_URL_REPLACEMENTS=") }) {
		return ""
	}
	saved := m.env
	m.env = env
	defer func() { m.env = saved }()
	out, err := m.run(t.Context(), "settings", "get", "url_replacements")
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}
