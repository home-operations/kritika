package runner

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/home-operations/kritika/internal/aqua"
	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/repoconfig"
	"github.com/home-operations/kritika/internal/runspec"
)

// installTimeout bounds each step of installing a tool, the registry's
// fetch, a checksum's lookup and each attempt at the download, as
// fetchTimeout bounds a fetch; fetchRegistry fetches the aqua registry the
// run's gateway keeps, and githubAPI is where the GitHub REST API is.
// Variables for the tests.
var (
	installTimeout = 2 * time.Minute
	fetchRegistry  = func(ctx context.Context, gatewayURL, gatewayToken string) (*aqua.Registry, error) {
		return aqua.FetchRegistry(ctx, http.DefaultClient, strings.TrimSuffix(gatewayURL, "/")+runspec.AquaRegistryPath,
			http.Header{"Authorization": {"Bearer " + gatewayToken}})
	}
	githubAPI = "https://" + configfile.GitHubAPIHost
)

// repoTool is one of the repository's tools.
type repoTool struct {
	// name is the tool as .kritika.yaml declares it.
	name string
	pkg  *aqua.Package
	// dir is where it installs, and commands where its commands are once
	// it has.
	dir      string
	commands map[string]string
}

// repoTools are the repository's tools and the commands they offer, each
// tool installed on the first call that needs it. Everything an install
// writes lives under the run's HOME, which the runner keeps read-only
// between its own installs, so a command cannot replace an installed
// binary. An installed command is linked from bin, which leads the
// commands' PATH, so one tool can run another's command.
type repoTools struct {
	home, bin string
	installer *aqua.Installer
	// cache reaches the gateway's plain HTTP proxy, which a checked
	// download goes through first, nil when the gateway keeps none: one
	// transport each download's digest is named over, so their connections
	// are reused rather than left open. direct fetches around it, the
	// checksums always.
	cache     http.RoundTripper
	direct    *http.Client
	byCommand map[string]*repoTool
	logger    *slog.Logger
	// locked says home is read-only, as it is once the run tool is built,
	// and writable that unlocked has it writable for the while; broken is
	// why it could not be made read-only again, after which no command
	// runs.
	locked, writable bool
	broken           error
}

// download is one way to fetch a tool's download, and what the log says
// of a retry that takes it.
type download struct {
	client *http.Client
	note   string
}

// newRepoTools resolves tools, the repository's, in the aqua registry the
// run's gateway at gatewayURL keeps, read with the run's gatewayToken, to
// install under home. env is the commands' environment, through whose
// proxy, the gateway, a tool's download reaches its host. cacheLocked says
// the gateway keeps a checked download, which a tool's then goes through
// first. credentialed names the commands that run with
// one of the run's credentials, which a repository's tool may not offer;
// token is the run's GitHub token, for the GitHub API's account of a
// release, "" off GitHub. notes say what was not offered and why, for the
// review's summary.
func newRepoTools(
	ctx context.Context, tools repoconfig.Tools, gatewayURL, gatewayToken, home string, env []string, cacheLocked bool,
	credentialed []string, token string, logger *slog.Logger,
) (r *repoTools, notes []string, err error) {
	r = &repoTools{
		home: home, bin: filepath.Join(home, "tools", "bin"), byCommand: map[string]*repoTool{}, logger: logger,
		direct: proxiedClient(envValue(env, httpsProxyEnv)),
	}
	if plain := envValue(env, "HTTP_PROXY"); cacheLocked && plain != "" {
		r.cache = proxiedClient(plain).Transport
	}
	r.installer = &aqua.Installer{Client: r.direct, APIURL: githubAPI, Token: token, Logger: logger}
	registry, err := within(ctx, func(ctx context.Context) (*aqua.Registry, error) {
		return fetchRegistry(ctx, gatewayURL, gatewayToken)
	})
	if err != nil {
		return nil, nil, fmt.Errorf("runner: %w", err)
	}
	platform := aqua.Platform{OS: runtime.GOOS, Arch: runtime.GOARCH, LibC: libc()}
	for i, name := range slices.Sorted(maps.Keys(tools)) {
		pkg, err := registry.Resolve(strings.TrimPrefix(name, repoconfig.ToolPrefix), tools[name], platform)
		switch {
		case errors.Is(err, aqua.ErrNotFound):
			notes = append(notes, fmt.Sprintf("tools: %s not offered: it is not in the aqua registry", name))
			continue
		case err != nil:
			notes = append(notes, fmt.Sprintf("tools: %s not offered: %v", name, err))
			continue
		}
		t := &repoTool{name: name, pkg: pkg, dir: filepath.Join(home, "tools", strconv.Itoa(i))}
		for _, f := range pkg.Files {
			switch other := r.byCommand[f.Name]; {
			case other != nil:
				notes = append(notes, fmt.Sprintf("tools: %s does not offer %s: %s offers it already", name, f.Name, other.name))
			case slices.Contains(credentialed, f.Name):
				notes = append(notes, fmt.Sprintf("tools: %s does not offer %s: it carries one of the run's credentials, "+
					"so only the image provides it", name, f.Name))
			default:
				r.byCommand[f.Name] = t
			}
		}
	}
	if len(r.byCommand) == 0 {
		return nil, notes, nil
	}
	if err := os.MkdirAll(r.bin, 0o755); err != nil {
		return nil, notes, fmt.Errorf("runner: tools: %w", err)
	}
	return r, notes, nil
}

// libc is the C library of the runner's image as aqua names it: musl
// where its loader is, else glibc.
func libc() string {
	if m, _ := filepath.Glob("/lib/ld-musl-*.so.1"); len(m) > 0 {
		return "musl"
	}
	return "glibc"
}

// envValue is the value of name in env, under either spelling.
func envValue(env []string, name string) string {
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && strings.EqualFold(k, name) && v != "" {
			return v
		}
	}
	return ""
}

// proxiedClient is a client whose requests go through proxy, when set.
func proxiedClient(proxy string) *http.Client {
	t := &http.Transport{ForceAttemptHTTP2: true}
	if u, err := url.Parse(proxy); err == nil && proxy != "" {
		t.Proxy = http.ProxyURL(u)
	}
	return &http.Client{Transport: t}
}

// cachedClient is a client for a download with sum that goes through the
// gateway's cache over proxy, the transport to it: as the plain HTTP
// request the gateway upgrades, naming sum as the digest the cache keeps
// the download by, for a URL plainHTTP rewrites, and as is for any other.
func cachedClient(proxy http.RoundTripper, sum aqua.Sum) *http.Client {
	return &http.Client{Transport: digestNaming{proxy, sum.Algorithm + ":" + sum.Hex}}
}

// digestNaming sends each request plainHTTP rewrites as that plain HTTP
// request, naming digest.
type digestNaming struct {
	base   http.RoundTripper
	digest string
}

func (rt digestNaming) RoundTrip(req *http.Request) (*http.Response, error) {
	if u := plainHTTP(req.URL); u != req.URL {
		req = req.Clone(req.Context())
		req.URL, req.Host = u, u.Host
		req.Header.Set(runspec.DigestHeader, rt.digest)
	}
	return rt.base.RoundTrip(req)
}

// keptOutHosts are hosts whose downloads the gateway's cache does not
// keep: the GitHub API, which a request with the run's token reaches.
var keptOutHosts = []string{configfile.GitHubAPIHost}

// plainHTTP is u as the plain HTTP request the gateway upgrades, so it can
// keep what it fetches: an https:// URL on the default port, with no
// credentials or query, to a host not kept out. Any other URL is u.
func plainHTTP(u *url.URL) *url.URL {
	host := strings.ToLower(u.Hostname())
	if u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Port() != "" && u.Port() != "443" ||
		host == "" || strings.Contains(host, ":") || strings.HasSuffix(host, ".") || slices.Contains(keptOutHosts, host) {
		return u
	}
	plain := *u
	plain.Scheme, plain.Host = "http", u.Hostname()
	return &plain
}

// offered are the commands the tools provide. The map's values are
// empty: the binaries are resolved per call.
func (r *repoTools) offered() map[string]string {
	out := make(map[string]string, len(r.byCommand))
	for c := range r.byCommand {
		out[c] = ""
	}
	return out
}

// pending reports whether an offered tool is still to install.
func (r *repoTools) pending() bool {
	return slices.ContainsFunc(slices.Collect(maps.Values(r.byCommand)), func(t *repoTool) bool { return t.commands == nil })
}

// withPath is env with bin leading its PATH.
func (r *repoTools) withPath(env []string) []string {
	out := slices.Clone(env)
	for i, kv := range out {
		if path, ok := strings.CutPrefix(kv, "PATH="); ok {
			out[i] = "PATH=" + r.bin + string(os.PathListSeparator) + path
		}
	}
	return out
}

// resolveWith resolves a command one of the tools offers as its link in
// bin, installing the tool first when it is not yet, and any other
// command as the binary onPath names for it, each run with env, and none
// once home could not be made read-only again. Its error is for the agent.
func (r *repoTools) resolveWith(onPath map[string]string, env []string) func(context.Context, string) (string, []string, error) {
	return func(ctx context.Context, command string) (string, []string, error) {
		if r.broken != nil {
			return "", nil, fmt.Errorf("commands are not run: %w", r.broken)
		}
		t := r.byCommand[command]
		if t == nil {
			return onPath[command], env, nil
		}
		if t.commands == nil {
			if err := r.unlocked(ctx, func() error { return r.install(ctx, t) }); err != nil {
				return "", nil, err
			}
		}
		return filepath.Join(r.bin, command), env, nil
	}
}

// install installs t and links the commands it offers from bin. The
// download is checked against a checksum fetched apart from it, around
// the cache: first as it comes through the gateway's cache, while the
// gateway keeps checked downloads, then, should that fail, around the
// cache, in case the gateway's own fetch is what the origin refuses. The
// lookup and each attempt have installTimeout of their own, so one that
// stalls leaves the next its time. Whatever an earlier call left of t's
// directory goes first, and a tool whose commands could not all be linked
// is removed again, links and all, so the next call installs it anew; one
// installed is linked even if ctx ended meanwhile.
func (r *repoTools) install(ctx context.Context, t *repoTool) error {
	started := time.Now()
	if err := os.RemoveAll(t.dir); err != nil {
		return fmt.Errorf("%s could not be installed: %w", t.name, err)
	}
	sum, err := within(ctx, func(ctx context.Context) (aqua.Sum, error) { return r.installer.Checksum(ctx, t.pkg) })
	var downloads []download
	if r.cache != nil {
		downloads = append(downloads, download{cachedClient(r.cache, sum), ""})
	}
	downloads = append(downloads, download{r.direct, "installing it without the cache"})
	var commands map[string]string
	attempt := 0
	for ; err == nil; attempt++ {
		in := *r.installer
		in.Downloads = downloads[attempt].client
		commands, err = within(ctx, func(ctx context.Context) (map[string]string, error) { return in.Install(ctx, t.pkg, sum, t.dir) })
		if err == nil || attempt == len(downloads)-1 || !errors.Is(err, aqua.ErrDownload) || ctx.Err() != nil {
			break
		}
		r.logger.Warn("tool not installed from the gateway's cache, "+downloads[attempt+1].note,
			"tool", t.name, "version", t.pkg.Version, "error", err)
		err = nil
	}
	switch {
	case err == nil:
	case ctx.Err() != nil:
		return ctx.Err()
	case errors.Is(err, context.DeadlineExceeded):
		r.logger.Warn("tool not installed", "tool", t.name, "version", t.pkg.Version, "error", err)
		return fmt.Errorf("%s could not be installed: stopped after %s", t.name, installTimeout)
	default:
		r.logger.Warn("tool not installed", "tool", t.name, "version", t.pkg.Version, "error", err)
		return fmt.Errorf("%s could not be installed: %w", t.name, err)
	}
	if err := r.link(t, commands); err != nil {
		r.logger.Warn("tool not installed: its commands could not be linked", "tool", t.name, "version", t.pkg.Version, "error", err)
		return fmt.Errorf("%s could not be installed: %w", t.name, err)
	}
	t.commands = commands
	r.logger.Info("tool installed", "tool", t.name, "version", t.pkg.Version, "asset", t.pkg.Asset,
		"gateway_cache", r.cache != nil && attempt == 0, "elapsed", time.Since(started).Round(time.Millisecond))
	return nil
}

// within runs f with installTimeout of its own, ending with ctx.
func within[T any](ctx context.Context, f func(context.Context) (T, error)) (T, error) {
	ctx, cancel := context.WithTimeout(ctx, installTimeout)
	defer cancel()
	return f(ctx)
}

// link links from bin each command t offers, at commands, or, should one
// fail, removes what it linked and t's directory.
func (r *repoTools) link(t *repoTool, commands map[string]string) error {
	var linked []string
	for name, tool := range r.byCommand {
		if tool != t {
			continue
		}
		link := filepath.Join(r.bin, name)
		if err := os.Symlink(commands[name], link); err != nil {
			for _, l := range linked {
				_ = os.Remove(l)
			}
			return errors.Join(err, os.RemoveAll(t.dir))
		}
		linked = append(linked, link)
	}
	return nil
}

// unlocked runs f, an install, with home writable. Once the run tool is
// built, home is read-only between the runner's own installs. A home that
// cannot be made read-only again is broken: f's error carries that too,
// so the command it was for does not run, and no command runs after.
func (r *repoTools) unlocked(ctx context.Context, f func() error) (err error) {
	if !r.locked || r.writable {
		return f()
	}
	if err := lockTree(ctx, r.home, false); err != nil {
		r.broken = fmt.Errorf("runner: home not made writable for an install: %w", err)
		_ = lockTree(ctx, r.home, true)
		return r.broken
	}
	r.writable = true
	defer func() {
		r.writable = false
		if lerr := lockTree(context.WithoutCancel(ctx), r.home, true); lerr != nil {
			r.broken = fmt.Errorf("runner: home not made read-only after an install: %w", lerr)
			r.logger.Warn("commands no longer run: home not made read-only after an install", "error", lerr)
			err = errors.Join(err, r.broken)
		}
	}()
	return f()
}

// lockTree takes the owner's write permission from everything under dir,
// dir included, or gives it back, until ctx ends. Symlinks are left
// alone: their targets are reached on their own.
func lockTree(ctx context.Context, dir string, lock bool) error {
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.Type()&fs.ModeSymlink != 0 {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		mode := info.Mode().Perm()
		if lock {
			mode &^= 0o222
		} else {
			mode |= 0o200
		}
		return os.Chmod(path, mode)
	})
}
