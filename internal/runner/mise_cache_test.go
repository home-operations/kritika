package runner

import (
	"cmp"
	"crypto/sha256"
	"encoding/json"
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

// repoToolDoc is a repository's config for github:acme/repo-tool, the
// release repoToolRelease serves from the GitHub API at api, offering
// command.
func repoToolDoc(api, command string) string {
	return fmt.Sprintf("[tools.'github:acme/repo-tool']\nversion = '1.0.0'\napi_url = '%s'\nasset_pattern = 'repo-tool'\nfilter_bins = '%s'\n",
		api, command)
}

// repoTool is the runner's read of repoToolDoc offering repo-tool.
func repoTool(t *testing.T, api string) *repoMiseConfig {
	t.Helper()
	repo, err := newRepoMiseConfig(repoToolDoc(api, "repo-tool"))
	if err != nil {
		t.Fatal(err)
	}
	return repo
}

// repoToolRelease is the GitHub API's answers for acme/repo-tool's v1.0.0
// release, and the list of its releases, which an unlocked install reads,
// as mise asks a tool's api_url for them: one asset, repo-tool, which
// mise downloads from assetURL, the asset's API URL, as it does from any
// API but GitHub's own, and locks on digest unless it is "".
func repoToolRelease(assetURL, digest string) http.Handler {
	asset := map[string]any{"name": "repo-tool", "url": assetURL, "browser_download_url": assetURL}
	if digest != "" {
		asset["digest"] = digest
	}
	release := map[string]any{"tag_name": "v1.0.0", "draft": false, "prerelease": false, "created_at": "2026-01-01T00:00:00Z", "assets": []any{asset}}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body any
		switch r.URL.Path {
		case "/repos/acme/repo-tool/releases/tags/v1.0.0":
			body = release
		case "/repos/acme/repo-tool/releases":
			body = []any{release}
		default:
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	})
}

// TestMiseLockedInstallThroughCache: a repository's tool, once the run
// has locked its https:// download, reaches the gateway as a plain HTTP
// request whose proxy username says it is a locked install's; should the
// install fail there, it is made once more over the same plain HTTP
// request, its proxy username asking the gateway to fetch again rather
// than answer from the cache. Without a cache, the tool installs locked
// over HTTPS as any other.
func TestMiseLockedInstallThroughCache(t *testing.T) {
	bin, dataDir := hostMise(t)
	t.Setenv("MISE_SYSTEM_DATA_DIR", dataDir)
	systemMiseConfig(t, testMiseConfig)
	payload := []byte("#!/bin/sh\necho upstream\n")
	// The API's digest is the checksum the run locks the tool on, which
	// needs no download.
	release := repoToolRelease("https://tools.example.invalid/repo-tool", fmt.Sprintf("sha256:%x", sha256.Sum256(payload)))
	repo := repoTool(t, "http://api.example.invalid")
	const stale = "#!/bin/sh\necho stale\n"
	for _, tt := range []struct {
		name string
		// cacheLocked is whether the gateway keeps locked downloads;
		// served is what it answers a locked install's plain HTTP request
		// with, and refilled what it answers the retry's.
		cacheLocked      bool
		served, refilled string
	}{
		{"from the cache", true, string(payload), ""},
		{"around a stale entry", true, stale, string(payload)},
		{"stale from upstream too", true, stale, stale},
		{"without a cache", false, "", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var mu sync.Mutex
			var seen []string
			// The gateway answers a plain HTTP request for the tool, from
			// upstream when the proxy's username asks it to fetch again,
			// and refuses every CONNECT, which mise also makes for its
			// versions host; it notes how each request for the tool's
			// host came, and as which user. The tool's api_url is plain
			// HTTP, so the gateway answers as the API too.
			gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				r.Header.Set("Authorization", r.Header.Get("Proxy-Authorization"))
				user, _, _ := r.BasicAuth()
				if host := cmp.Or(r.URL.Hostname(), r.Host); strings.HasPrefix(host, "tools.example.invalid") {
					mu.Lock()
					seen = append(seen, r.Method+" "+user)
					mu.Unlock()
				}
				if r.URL.Host == "api.example.invalid" {
					release.ServeHTTP(w, r)
					return
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
			// mise would back off for seconds retrying each CONNECT the
			// gateway refuses.
			base := []string{"HOME=" + home, "PATH=/usr/bin:/bin", "HTTP_PROXY=" + proxy, "HTTPS_PROXY=" + proxy, "MISE_HTTP_RETRIES=0"}
			var logs strings.Builder
			m, _, err := newMiseTools(t.Context(), bin, home, base, nil, tt.cacheLocked, repo, nil,
				Secrets{Redacted: &Redactions{}}, slog.New(slog.NewTextHandler(&logs, nil)))
			if err != nil {
				t.Fatal(err)
			}
			path, _, err := m.resolve(t.Context(), "repo-tool")
			mu.Lock()
			defer mu.Unlock()
			if !tt.cacheLocked {
				// The locked install goes over HTTPS, which this gateway
				// refuses, however often mise asks: no plain HTTP request,
				// no retry as a locked install's.
				plain := len(seen) > 0 && !slices.ContainsFunc(seen, func(s string) bool { return s != "CONNECT " })
				if err == nil || !plain || strings.Contains(logs.String(), "gateway's cache") {
					t.Fatalf("resolve = %s, %v; the gateway was asked %q\nlog:\n%s", path, err, seen, logs.String())
				}
				return
			}
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
