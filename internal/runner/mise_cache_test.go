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

	"github.com/home-operations/kritika/internal/config"
	"github.com/home-operations/kritika/internal/egress"
	"github.com/home-operations/kritika/internal/repoconfig"
)

func TestCacheReplacements(t *testing.T) {
	mirror := []config.MiseURLReplacement{{From: "https://github.com/", To: "https://mirror.example.com/github/"}}
	for _, tt := range []struct {
		name         string
		replacements []config.MiseURLReplacement
		cached       []string
		want         []config.MiseURLReplacement
	}{
		{"none cached", mirror, nil, nil},
		{"each host to plain HTTP", nil, []string{"get.helm.sh", "github.com"}, []config.MiseURLReplacement{
			{From: "https://get.helm.sh/", To: "http://get.helm.sh/"}, {From: "https://github.com/", To: "http://github.com/"},
		}},
		{"the admin's rule for a host stands", mirror, []string{"get.helm.sh", "github.com"}, []config.MiseURLReplacement{
			{From: "https://get.helm.sh/", To: "http://get.helm.sh/"},
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := cacheReplacements(tt.replacements, tt.cached); !slices.Equal(got, tt.want) {
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

// TestMiseToolsCachedHosts: the cached hosts reach mise as rules after the
// admin's, which keep their place, while the runner has the gateway as
// its HTTP proxy, and an install made around the cache has the same rules
// with the proxy's username asking the gateway not to answer from it.
func TestMiseToolsCachedHosts(t *testing.T) {
	bin, dataDir := hostMise(t)
	t.Setenv("MISE_SYSTEM_DATA_DIR", dataDir)
	systemMiseConfig(t, "[tools]\nyq = \""+testYqVersion+"\"\n")
	rules := []config.MiseURLReplacement{{From: "https://example.com/", To: "https://mirror.example.com/"}}
	settings := func(t *testing.T, m *miseTools) string {
		t.Helper()
		out, err := m.run(t.Context(), "settings", "get", "url_replacements")
		if err != nil {
			t.Fatal(err)
		}
		return string(out)
	}
	const proxy = "http://:krk_token@gateway.example:8082"
	for _, tt := range []struct {
		name  string
		proxy bool
	}{{"through the gateway", true}, {"without a proxy", false}} {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			base := []string{"PATH=/usr/bin", "HOME=" + home}
			if tt.proxy {
				base = append(base, "HTTP_PROXY="+proxy, "http_proxy="+proxy, "HTTPS_PROXY="+proxy, "https_proxy="+proxy)
			}
			m, _, err := newMiseTools(t.Context(), bin, home, base, rules, []string{"get.helm.sh"}, false, nil, nil,
				Secrets{Redacted: &Redactions{}}, slog.New(slog.DiscardHandler))
			if err != nil {
				t.Fatal(err)
			}
			got := settings(t, m)
			mirror, cache := strings.Index(got, `"https://example.com/" = "https://mirror.example.com/"`), strings.Index(got, `"https://get.helm.sh/" = "http://get.helm.sh/"`)
			if mirror < 0 || (cache < mirror) == tt.proxy {
				t.Fatalf("mise settings get url_replacements = %s: want the admin's rule, then the cache's only through the gateway", got)
			}
			if !tt.proxy {
				if m.uncached != nil {
					t.Fatalf("without a proxy, an install has an environment around the cache: %v", m.uncached)
				}
				return
			}
			for _, kv := range []string{"HTTP_PROXY=http://uncached:krk_token@gateway.example:8082", "https_proxy=http://uncached:krk_token@gateway.example:8082"} {
				if !slices.Contains(m.uncached, kv) {
					t.Errorf("around the cache, the environment lacks %s: %v", kv, m.uncached)
				}
			}
			m.env = m.uncached
			if got := settings(t, m); !strings.Contains(got, `"https://get.helm.sh/" = "http://get.helm.sh/"`) {
				t.Fatalf("around the cache, url_replacements = %s: want the same rules", got)
			}
		})
	}
}

// TestMiseInstallRetriesWithoutCache: an install that fails through the
// gateway's cache, here because what the gateway served from it does not
// match the lock, is made once more with the proxy's username asking the
// gateway not to answer from the cache, and the tool comes from upstream.
// The tool's URL is plain HTTP, which reaches the gateway, and its cache,
// without any rule of the runner's.
func TestMiseInstallRetriesWithoutCache(t *testing.T) {
	bin, dataDir := hostMise(t)
	t.Setenv("MISE_SYSTEM_DATA_DIR", dataDir)
	systemMiseConfig(t, testMiseConfig)
	payload := []byte("#!/bin/sh\necho upstream\n")
	var mu sync.Mutex
	var served []string
	// The gateway: a cached copy for a request, the upstream file for one
	// the proxy's username marks as a retry around the cache. A CONNECT,
	// which mise makes for its versions host, is refused.
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
	const url = "http://tools.example.invalid/repo-tool"
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
	home := t.TempDir()
	proxy := strings.Replace(gateway.URL, "://", "://:krk_token@", 1)
	base := []string{"HOME=" + home, "PATH=/usr/bin:/bin", "HTTP_PROXY=" + proxy, "HTTPS_PROXY=" + proxy}
	var logs strings.Builder
	m, _, err := newMiseTools(t.Context(), bin, home, base, nil, []string{"tools.example.invalid"}, false, repo, nil,
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
	if first, retry := slices.Index(served, ""), slices.Index(served, egress.UncachedProxyUser); first < 0 || retry < first {
		t.Fatalf("the gateway was asked as %q: want the cache's answer, then a retry around it", served)
	}
	for _, want := range []string{"installing it without", "gatewayCache=false"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("log lacks %q:\n%s", want, logs.String())
		}
	}
}

func TestLockHosts(t *testing.T) {
	for _, tt := range []struct {
		name         string
		urls, cached []string
		want         []string
	}{
		{"each host once, sorted", []string{
			"https://github.com/home-operations/flate/releases/download/v0.6.5/flate_0.6.5_linux_amd64.tar.gz",
			"https://get.helm.sh/helm-v4.3.0-linux-amd64.tar.gz",
			"https://github.com/google/go-containerregistry/releases/download/v0.22.1/go-containerregistry_Linux_x86_64.tar.gz",
		}, nil, []string{"get.helm.sh", "github.com"}},
		{"in lowercase, on the default port", []string{"https://GitHub.com:443/a/b"}, nil, []string{"github.com"}},
		{"not a host the cache has already", []string{"https://get.helm.sh/helm.tar.gz", "https://github.com/a/b"}, []string{"github.com"},
			[]string{"get.helm.sh"}},
		{"none the cache cannot keep, or that mise sends its token", []string{
			"http://tools.example.com/plain", "https://tools.example.com:8443/port", "https://user:pass@tools.example.com/credentials",
			"https://tools.example.com/signed?X-Amz-Signature=abc", "https://tools.example.com/empty?",
			"https://api.github.com/repos/a/b/releases/assets/1", "https://raw.githubusercontent.com/a/b/main/tool",
			"https://[2001:db8::1]/tool", "https://api.github.com./repos/a/b/releases/assets/1", "https://github.com./a/b", "::not a url",
		}, nil, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := lockHosts(tt.urls, tt.cached); !slices.Equal(got, tt.want) {
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
// each host its lock pins downloads on, after the admin's rules and the
// cached hosts', and the proxy's username saying it is locked; every
// other install has none of those rules, and is made again around the
// cache only when a cached host's rule sent it through.
func TestMiseToolsLockedInstalls(t *testing.T) {
	bin, dataDir := hostMise(t)
	t.Setenv("MISE_SYSTEM_DATA_DIR", dataDir)
	systemMiseConfig(t, "[tools]\nyq = \""+testYqVersion+"\"\n")
	repo := repoToolLock(t, "https://tools.example.invalid/repo-tool", nil)
	rules := []config.MiseURLReplacement{{From: "https://example.com/", To: "https://mirror.example.com/"}}
	settings := func(t *testing.T, m *miseTools, env []string) string {
		t.Helper()
		saved := m.env
		m.env = env
		defer func() { m.env = saved }()
		out, err := m.run(t.Context(), "settings", "get", "url_replacements")
		if err != nil {
			t.Fatal(err)
		}
		return string(out)
	}
	const (
		proxy      = "http://:krk_token@gateway.example:8082"
		mirrorRule = `"https://example.com/" = "https://mirror.example.com/"`
		cachedRule = `"https://get.helm.sh/" = "http://get.helm.sh/"`
		lockRule   = `"https://tools.example.invalid/" = "http://tools.example.invalid/"`
	)
	for _, tt := range []struct {
		name               string
		cached             []string
		proxy, cacheLocked bool
	}{
		{"with cached hosts", []string{"get.helm.sh"}, true, true},
		{"with a cache alone", nil, true, true},
		{"without a proxy", nil, false, true},
		{"without a cache", nil, true, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			base := []string{"PATH=/usr/bin", "HOME=" + home}
			if tt.proxy {
				base = append(base, "HTTP_PROXY="+proxy, "http_proxy="+proxy, "HTTPS_PROXY="+proxy, "https_proxy="+proxy)
			}
			m, _, err := newMiseTools(t.Context(), bin, home, base, rules, tt.cached, tt.cacheLocked, repo, nil,
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
			got := settings(t, m, m.repoEnv)
			mirror, cached, locked := strings.Index(got, mirrorRule), strings.Index(got, cachedRule), strings.Index(got, lockRule)
			if mirror < 0 || locked < mirror || (cached >= 0) != (tt.cached != nil) || cached >= 0 && (cached < mirror || locked < cached) {
				t.Fatalf("a locked install's url_replacements = %s: want the admin's rule, the cached hosts', then the lock's", got)
			}
			for _, kv := range []string{"HTTP_PROXY=http://locked:krk_token@gateway.example:8082", "https_proxy=http://locked:krk_token@gateway.example:8082"} {
				if !slices.Contains(m.repoEnv, kv) {
					t.Errorf("a locked install's environment lacks %s: %v", kv, m.repoEnv)
				}
			}
			got = settings(t, m, m.env)
			if !strings.Contains(got, mirrorRule) || strings.Contains(got, lockRule) || strings.Contains(got, cachedRule) != (tt.cached != nil) {
				t.Errorf("any other install's url_replacements = %s: want the admin's rule and the cached hosts' alone", got)
			}
			if (m.uncached != nil) != (tt.cached != nil) {
				t.Errorf("any other install's retry around the cache: %v", m.uncached)
			}
		})
	}
}

// TestMiseLockedInstallThroughCache: a repository's tool, whose lock pins
// an https:// download on a host the gateway was not told to cache,
// reaches the gateway as a plain HTTP request whose proxy username says
// it is a locked install's; should the install fail there, it is made
// once more as it would be without the cache, over HTTPS, its CONNECT
// asking the gateway not to answer from the cache.
func TestMiseLockedInstallThroughCache(t *testing.T) {
	bin, dataDir := hostMise(t)
	t.Setenv("MISE_SYSTEM_DATA_DIR", dataDir)
	systemMiseConfig(t, testMiseConfig)
	payload := []byte("#!/bin/sh\necho upstream\n")
	repo := repoToolLock(t, "https://tools.example.invalid/repo-tool", payload)
	for _, tt := range []struct {
		name string
		// served is what the gateway answers a plain HTTP request with.
		served string
		stale  bool
	}{
		{"from the cache", string(payload), false},
		{"around a stale entry", "#!/bin/sh\necho stale\n", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var mu sync.Mutex
			var seen []string
			// The gateway answers a plain HTTP request for the tool and
			// refuses every CONNECT, which mise also makes for its
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
				_, _ = io.WriteString(w, tt.served)
			}))
			defer gateway.Close()
			home := t.TempDir()
			proxy := strings.Replace(gateway.URL, "://", "://:krk_token@", 1)
			base := []string{"HOME=" + home, "PATH=/usr/bin:/bin", "HTTP_PROXY=" + proxy, "HTTPS_PROXY=" + proxy}
			var logs strings.Builder
			m, _, err := newMiseTools(t.Context(), bin, home, base, nil, nil, true, repo, nil,
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
			if !tt.stale {
				if err != nil {
					t.Fatalf("resolve: %v\nlog:\n%s", err, logs.String())
				}
				if out, err := exec.Command(path).Output(); err != nil || string(out) != "upstream\n" {
					t.Fatalf("installed tool printed %q, %v", out, err)
				}
				if !strings.Contains(logs.String(), "gatewayCache=true") {
					t.Errorf("log does not say the install went through the cache:\n%s", logs.String())
				}
				return
			}
			if err == nil {
				t.Fatalf("resolve installed %s from a stale entry and a refused CONNECT", path)
			}
			retry := slices.Index(seen, "CONNECT uncached")
			if retry < 0 || slices.ContainsFunc(seen[retry:], func(s string) bool { return s != "CONNECT uncached" }) {
				t.Fatalf("the gateway was asked %q: want the retry to CONNECT around the cache alone", seen)
			}
			if !strings.Contains(logs.String(), "installing it without") {
				t.Errorf("log lacks the retry:\n%s", logs.String())
			}
		})
	}
}
