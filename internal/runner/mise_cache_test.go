package runner

import (
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
			m, _, err := newMiseTools(t.Context(), bin, home, base, rules, []string{"get.helm.sh"}, nil, nil,
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
	m, _, err := newMiseTools(t.Context(), bin, home, base, nil, []string{"tools.example.invalid"}, repo, nil,
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
