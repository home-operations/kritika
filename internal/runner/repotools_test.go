package runner

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/home-operations/kritika/internal/aqua"
	"github.com/home-operations/kritika/internal/repoconfig"
	"github.com/home-operations/kritika/internal/runspec"
)

// testRegistry is an aqua registry for the runner's tests: repo-tool, a
// release asset that is the tool itself, and packages each refused or
// partly offered in its own way.
const testRegistry = `packages:
  - type: github_release
    repo_owner: acme
    repo_name: repo-tool
    asset: repo-tool
    format: raw
  - type: github_release
    repo_owner: acme
    repo_name: other
    asset: other.tar.gz
    files:
      - name: other
      - name: repo-tool
  - type: github_release
    repo_owner: cli
    repo_name: cli
    asset: gh.tar.gz
    files:
      - name: gh
  - type: go_install
    name: acme/built
    path: example.com/acme/built
`

// testGateway is the run's gateway in the tests' specs.
const testGateway = "http://kritika-gateway:8082"

// useTestRegistry has newRepoTools read testRegistry as the registry
// testGateway keeps, and look releases up in the GitHub API at
// http://api.example.invalid, which a test's gateway answers.
func useTestRegistry(t *testing.T) {
	t.Helper()
	fetch, was := fetchRegistry, githubAPI
	t.Cleanup(func() { fetchRegistry, githubAPI = fetch, was })
	fetchRegistry = func(_ context.Context, gatewayURL, _ string) (*aqua.Registry, error) {
		if gatewayURL != testGateway {
			return nil, fmt.Errorf("the registry of %s, want %s's", gatewayURL, testGateway)
		}
		return aqua.Parse([]byte(testRegistry))
	}
	githubAPI = "http://api.example.invalid"
}

func TestNewRepoTools(t *testing.T) {
	useTestRegistry(t)
	tools := repoconfig.Tools{
		"aqua:acme/repo-tool": "v1.0.0", "aqua:acme/other": "v1.0.0", "aqua:cli/cli": "v2.0.0",
		"aqua:acme/built": "v1.0.0", "aqua:acme/missing": "v1.0.0",
	}
	r, notes, err := newRepoTools(t.Context(), tools, testGateway, "run-token", t.TempDir(), nil, false, []string{"gh"}, "", slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	if got := slices.Sorted(maps.Keys(r.offered())); !slices.Equal(got, []string{"other", "repo-tool"}) {
		t.Errorf("offered = %v", got)
	}
	if owner := r.byCommand["repo-tool"].name; owner != "aqua:acme/other" {
		t.Errorf("repo-tool is aqua:acme/other's, which sorts first; got %s", owner)
	}
	for _, want := range []string{
		"tools: aqua:acme/built not offered: aqua: acme/built@v1.0.0: not installable: its go_install type",
		"tools: aqua:acme/missing not offered: it is not in the aqua registry",
		"tools: aqua:acme/repo-tool does not offer repo-tool: aqua:acme/other offers it already",
		"tools: aqua:cli/cli does not offer gh: it carries one of the run's credentials",
	} {
		if !slices.ContainsFunc(notes, func(n string) bool { return strings.HasPrefix(n, want) }) {
			t.Errorf("notes lack %q:\n%s", want, strings.Join(notes, "\n"))
		}
	}
	none, notes, err := newRepoTools(t.Context(), repoconfig.Tools{"aqua:acme/missing": "1.0.0"}, testGateway, "run-token", t.TempDir(), nil, false, nil, "",
		slog.New(slog.DiscardHandler))
	if none != nil || err != nil || len(notes) != 1 {
		t.Errorf("tools that offer nothing = %v, %v, notes %v", none, err, notes)
	}
}

func TestPlainHTTP(t *testing.T) {
	for raw, want := range map[string]string{
		"https://github.com/acme/tool/releases/download/v1/tool": "http://github.com/acme/tool/releases/download/v1/tool",
		"https://Dl.Example.com:443/tool":                        "http://Dl.Example.com/tool",
		"https://dl.example.com:8443/tool":                       "https://dl.example.com:8443/tool",
		"https://dl.example.com/tool?sig=x":                      "https://dl.example.com/tool?sig=x",
		"https://user@dl.example.com/tool":                       "https://user@dl.example.com/tool",
		"https://raw.githubusercontent.com/acme/tool/v1/tool":    "http://raw.githubusercontent.com/acme/tool/v1/tool",
		"https://api.github.com/repos/acme/tool":                 "https://api.github.com/repos/acme/tool",
		"https://dl.example.com./tool":                           "https://dl.example.com./tool",
		"https://[::1]/tool":                                     "https://[::1]/tool",
		"http://dl.example.com/tool":                             "http://dl.example.com/tool",
	} {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if got := plainHTTP(u).String(); got != want {
			t.Errorf("plainHTTP(%s) = %s, want %s", raw, got, want)
		}
	}
}

// TestRepoToolsInstallThroughCache: a repository's tool reaches the gateway
// as a plain HTTP request naming a digest. The runner makes sure that the
// download matches that digest, which the GitHub API keeps. If the install
// fails there, it is made once more over HTTPS around the cache. Without a
// cache, the tool installs over HTTPS alone. Home is read-only again after.
func TestRepoToolsInstallThroughCache(t *testing.T) {
	useTestRegistry(t)
	payload := []byte("#!/bin/sh\necho upstream\n")
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(payload))
	for _, tt := range []struct {
		name string
		// cacheLocked is whether the gateway keeps checked downloads, and
		// served what it answers a request naming the digest with.
		cacheLocked bool
		served      string
	}{
		{"from the cache", true, string(payload)},
		{"around a stale entry", true, "#!/bin/sh\necho stale\n"},
		{"without a cache", false, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var mu sync.Mutex
			var seen []string
			env := standInGateway(t, payload, func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				seen = append(seen, r.Method+" "+r.Header.Get(runspec.DigestHeader))
				mu.Unlock()
				if r.Method == http.MethodConnect {
					http.Error(w, "refused", http.StatusForbidden)
					return
				}
				_, _ = io.WriteString(w, tt.served)
			})
			home := t.TempDir()
			var logs strings.Builder
			r, _, err := newRepoTools(t.Context(), repoconfig.Tools{"aqua:acme/repo-tool": "v1.0.0"}, testGateway, "run-token", home, env, tt.cacheLocked, nil, "",
				slog.New(slog.NewTextHandler(&logs, nil)))
			if err != nil {
				t.Fatal(err)
			}
			if err := lockTree(t.Context(), home, true); err != nil {
				t.Fatal(err)
			}
			r.locked = true
			t.Cleanup(func() { _ = lockTree(context.Background(), home, false) })
			path, _, err := r.resolveWith(nil, env)(t.Context(), "repo-tool")
			mu.Lock()
			defer mu.Unlock()
			if info, serr := os.Stat(r.bin); serr != nil || info.Mode().Perm()&0o222 != 0 {
				t.Errorf("home is not read-only after the install: %v, %v", info.Mode(), serr)
			}
			switch tt.name {
			case "without a cache":
				if err == nil || !slices.Equal(seen, []string{"CONNECT "}) {
					t.Fatalf("resolve = %s, %v; the gateway was asked %q: want one CONNECT, refused", path, err, seen)
				}
			case "around a stale entry":
				if err == nil || !slices.Equal(seen, []string{"GET " + digest, "CONNECT "}) {
					t.Fatalf("resolve = %s, %v; the gateway was asked %q: want the cache, then a CONNECT around it", path, err, seen)
				}
				if !strings.Contains(logs.String(), "installing it without the cache") {
					t.Errorf("log lacks the retry:\n%s", logs.String())
				}
			default:
				if err != nil {
					t.Fatalf("resolve: %v\nlog:\n%s", err, logs.String())
				}
				if out, err := exec.Command(path).Output(); err != nil || string(out) != "upstream\n" {
					t.Fatalf("installed tool printed %q, %v", out, err)
				}
				if !slices.Equal(seen, []string{"GET " + digest}) || !strings.Contains(logs.String(), "gateway_cache=true") {
					t.Errorf("the gateway was asked %q\nlog:\n%s", seen, logs.String())
				}
			}
		})
	}
}

// TestRepoToolsBroken: an install after which home cannot be made
// read-only again fails, so the command it was for does not run, and no
// later command runs.
func TestRepoToolsBroken(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every directory")
	}
	useTestRegistry(t)
	home := t.TempDir()
	r, _, err := newRepoTools(t.Context(), repoconfig.Tools{"aqua:acme/repo-tool": "v1.0.0"}, testGateway, "run-token", home, nil, false, nil, "",
		slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	// A directory the walk cannot read fails the relock.
	sealed := filepath.Join(home, "sealed")
	if err := os.Mkdir(sealed, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(sealed, 0o755)
		_ = lockTree(context.Background(), home, false)
	})
	r.locked = true
	resolve := r.resolveWith(map[string]string{"grep": "/usr/bin/grep"}, nil)
	if _, _, err := resolve(t.Context(), "repo-tool"); err == nil {
		t.Fatal("resolve succeeded though home could not be made writable")
	}
	if _, _, err := resolve(t.Context(), "grep"); err == nil || !strings.Contains(err.Error(), "commands are not run") {
		t.Fatalf("resolve after a failed relock = %v", err)
	}
}

// TestRepoToolsBrokenAfterAnInstall: home that an install leaves unable to
// be made read-only again leaves no command to run.
func TestRepoToolsBrokenAfterAnInstall(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every directory")
	}
	useTestRegistry(t)
	home := t.TempDir()
	r, _, err := newRepoTools(t.Context(), repoconfig.Tools{"aqua:acme/repo-tool": "v1.0.0"}, testGateway, "run-token", home, nil, false, nil, "",
		slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	sealed := filepath.Join(home, "sealed")
	t.Cleanup(func() {
		_ = os.Chmod(sealed, 0o755)
		_ = lockTree(context.Background(), home, false)
	})
	r.locked = true
	ran := false
	// A directory the walk cannot read, made while home is writable, fails
	// the relock after it.
	err = r.unlocked(t.Context(), func() error {
		ran = true
		return os.Mkdir(sealed, 0)
	})
	if !ran || err == nil || r.broken == nil {
		t.Fatalf("unlocked ran the install: %v, = %v, broken %v; want the relock's failure", ran, err, r.broken)
	}
	resolve := r.resolveWith(map[string]string{"grep": "/usr/bin/grep"}, nil)
	if _, _, err := resolve(t.Context(), "grep"); err == nil || !strings.Contains(err.Error(), "commands are not run") {
		t.Fatalf("resolve after a failed relock = %v", err)
	}
}

func TestLockTree(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "d")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "f")
	if err := os.WriteFile(file, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/nowhere", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if err := lockTree(t.Context(), root, true); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]os.FileMode{root: 0o555, dir: 0o555, file: 0o555} {
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != want {
			t.Fatalf("%s: mode %v, %v; want %v", path, info.Mode().Perm(), err, want)
		}
	}
	if os.Geteuid() != 0 {
		if err := os.WriteFile(file, []byte("y"), 0o644); err == nil {
			t.Fatal("a locked file was written")
		}
		if err := os.WriteFile(filepath.Join(dir, "new"), nil, 0o644); err == nil {
			t.Fatal("a file was created in a locked directory")
		}
	}
	if err := lockTree(t.Context(), root, false); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(file); err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("unlocked mode = %v, %v", info.Mode().Perm(), err)
	}
	if err := os.WriteFile(file, []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// standInGateway is the gateway for a repository tool's install. It answers
// the GitHub API for acme/repo-tool's release, whose asset has payload's
// digest. It hands serve a request for the tool's download and every CONNECT,
// and it refuses the rest. env is the commands' environment with it as both
// proxies.
func standInGateway(t *testing.T, payload []byte, serve func(w http.ResponseWriter, r *http.Request)) (env []string) {
	t.Helper()
	release := map[string]any{"assets": []any{map[string]any{"name": "repo-tool", "digest": fmt.Sprintf("sha256:%x", sha256.Sum256(payload))}}}
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Host == "api.example.invalid":
			_ = json.NewEncoder(w).Encode(release)
		case r.Method == http.MethodConnect || r.URL.Host == "github.com":
			serve(w, r)
		default:
			http.Error(w, "refused", http.StatusForbidden)
		}
	}))
	t.Cleanup(gateway.Close)
	proxy := strings.Replace(gateway.URL, "://", "://:krk_token@", 1)
	return []string{"PATH=/usr/bin:/bin", "HTTP_PROXY=" + proxy, "HTTPS_PROXY=" + proxy}
}

// TestRepoToolsInstallAfterAStall: an attempt at the download that stalls
// takes its own installTimeout, and leaves the next attempt its own: here
// one around the cache, which the stand-in gateway refuses at once.
func TestRepoToolsInstallAfterAStall(t *testing.T) {
	useTestRegistry(t)
	was := installTimeout
	t.Cleanup(func() { installTimeout = was })
	installTimeout = 300 * time.Millisecond
	payload := []byte("#!/bin/sh\necho upstream\n")
	connected := make(chan struct{}, 1)
	env := standInGateway(t, payload, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodConnect {
			connected <- struct{}{}
			http.Error(w, "refused", http.StatusForbidden)
			return
		}
		<-r.Context().Done()
	})
	home := t.TempDir()
	r, _, err := newRepoTools(t.Context(), repoconfig.Tools{"aqua:acme/repo-tool": "v1.0.0"}, testGateway, "run-token", home, env, true, nil, "", slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = r.resolveWith(nil, env)(t.Context(), "repo-tool")
	select {
	case <-connected:
	default:
		t.Fatalf("no attempt around the cache after the stalled one: %v", err)
	}
	if err == nil || strings.Contains(err.Error(), "stopped after") {
		t.Fatalf("resolve = %v; want the attempt around the cache's own failure", err)
	}
}

// TestRepoToolsLinkFailure: an install whose commands cannot be linked
// leaves neither the tool nor a link behind, and the next call installs it.
func TestRepoToolsLinkFailure(t *testing.T) {
	useTestRegistry(t)
	payload := []byte("#!/bin/sh\necho upstream\n")
	env := standInGateway(t, payload, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(payload) })
	home := t.TempDir()
	r, _, err := newRepoTools(t.Context(), repoconfig.Tools{"aqua:acme/repo-tool": "v1.0.0"}, testGateway, "run-token", home, env, true, nil, "", slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	// A file where bin must be fails every link.
	if err := os.Remove(r.bin); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.bin, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	resolve := r.resolveWith(nil, env)
	if _, _, err := resolve(t.Context(), "repo-tool"); err == nil {
		t.Fatal("resolve succeeded though its command could not be linked")
	}
	if _, err := os.Stat(r.byCommand["repo-tool"].dir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the tool's directory was left: %v", err)
	}
	if err := os.Remove(r.bin); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(r.bin, 0o755); err != nil {
		t.Fatal(err)
	}
	path, _, err := resolve(t.Context(), "repo-tool")
	if err != nil {
		t.Fatalf("resolve once bin is back: %v", err)
	}
	if out, err := exec.Command(path).Output(); err != nil || string(out) != "upstream\n" {
		t.Fatalf("installed tool printed %q, %v", out, err)
	}
}

// TestRepoToolsCanceledAsInstalled: a call whose context ends as its
// install finishes still links the tool, and a later call runs it.
func TestRepoToolsCanceledAsInstalled(t *testing.T) {
	useTestRegistry(t)
	payload := []byte("#!/bin/sh\necho upstream\n")
	var downloads atomic.Int32
	env := standInGateway(t, payload, func(w http.ResponseWriter, _ *http.Request) {
		downloads.Add(1)
		_, _ = w.Write(payload)
	})
	home := t.TempDir()
	r, _, err := newRepoTools(t.Context(), repoconfig.Tools{"aqua:acme/repo-tool": "v1.0.0"}, testGateway, "run-token", home, env, true, nil, "", slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	// The download alone goes through a client that ends ctx once read,
	// sent as the plain HTTP request the stand-in gateway answers.
	plain := cachedClient(r.cache, aqua.Sum{})
	r.cache, r.direct = nil, &http.Client{Transport: cancelOnClose{plain.Transport, cancel}}
	resolve := r.resolveWith(nil, env)
	_, _, _ = resolve(ctx, "repo-tool")
	if ctx.Err() == nil || r.byCommand["repo-tool"].commands == nil {
		t.Fatalf("the first call: canceled %v, installed %v", ctx.Err(), r.byCommand["repo-tool"].commands)
	}
	if _, err := os.Stat(filepath.Join(r.bin, "repo-tool")); err != nil {
		t.Fatalf("the canceled call left repo-tool unlinked: %v", err)
	}
	before := downloads.Load()
	path, _, err := resolve(t.Context(), "repo-tool")
	if err != nil {
		t.Fatalf("resolve after a call canceled as it installed: %v", err)
	}
	if out, err := exec.Command(path).Output(); err != nil || string(out) != "upstream\n" {
		t.Fatalf("installed tool printed %q, %v", out, err)
	}
	if downloads.Load() != before {
		t.Fatalf("the second call downloaded repo-tool again")
	}
}

// cancelOnClose ends a context once a response's body is closed, as the
// download is after the install reads it.
type cancelOnClose struct {
	base   http.RoundTripper
	cancel context.CancelFunc
}

func (rt cancelOnClose) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := rt.base.RoundTrip(req)
	if err == nil {
		resp.Body = closeCanceling{resp.Body, rt.cancel}
	}
	return resp, err
}

type closeCanceling struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c closeCanceling) Close() error {
	defer c.cancel()
	return c.ReadCloser.Close()
}
