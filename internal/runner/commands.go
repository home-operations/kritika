package runner

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/config"

	"github.com/home-operations/kritika/internal/agent"
	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/runspec"
)

// defaultCommands are the commands the -tools image carries on its PATH.
// The run tool offers those of them on the runner's PATH, on any image.
var defaultCommands = []string{"curl", "find", "gh", "grep", "jq"}

// commandTool is the run tool for the commands that a run offers over a
// checkout of tree written to scratch space. It also returns fetch_repo,
// which writes beside the checkout, when one of the commands already reaches
// the network. The commands are the defaultCommands on PATH and, over them,
// those of the repository's own tools, p.Tools. Each is installed on first
// use.
//
// head, when not nil, is the lock that the parts of a split review share for
// reading the head (partLocks). The checkout holds that lock while it is
// written. Both tools are nil, with a no-op cleanup, when there is nothing to
// offer or the commands cannot be kept from the runner's secrets. The review
// then goes on with the read-only tools alone. notes say what was not
// offered, for the review's summary. cleanup removes the scratch space.
func commandTool(
	ctx context.Context, p runspec.Spec, tree *agent.Tree, head chan struct{}, secrets runspec.Secrets, maxOutput int, logger *slog.Logger,
) (run *agent.RunTool, fetch *fetchRepoTool, notes []string, cleanup func()) {
	cleanup = func() {}
	if err := hideEnviron(); err != nil {
		logger.Warn("commands not offered: the runner's environment cannot be hidden from them", "error", err)
		return nil, nil, notes, cleanup
	}
	scratch, err := os.MkdirTemp("", "kritika-run-")
	if err != nil {
		logger.Warn("commands not offered: no scratch space", "error", err)
		return nil, nil, notes, cleanup
	}
	// HOME is apart from the checkout. Otherwise, a repository can plant the
	// ~/.curlrc that curl reads before its arguments. The runner writes its
	// own, and keeps HOME read-only once the tool is built. So a command
	// cannot rewrite it, or the binaries that the repository's tools install
	// there. The cache directory is the commands' XDG_CACHE_HOME. So a tool
	// that keeps a cache there, as flate does, has somewhere to write it that
	// is not HOME.
	dir, home, up, cache := filepath.Join(scratch, "checkout"), filepath.Join(scratch, "home"),
		filepath.Join(scratch, upstreamDir), filepath.Join(scratch, "cache")
	// The scratch space goes however the run ended, so its removal does
	// not stop with ctx. Only home and the scratch directory itself were
	// made read-only. home possibly does not exist yet.
	cleanup = func() {
		err := lockTree(context.WithoutCancel(ctx), home, false)
		if err := errors.Join(os.Chmod(scratch, 0o755), err); err != nil && !errors.Is(err, fs.ErrNotExist) {
			logger.Warn("scratch not made writable for its removal", "error", err)
		}
		_ = os.RemoveAll(scratch)
	}
	for _, d := range []string{dir, home, up, cache} {
		if err := os.Mkdir(d, 0o755); err != nil {
			logger.Warn("commands not offered: no scratch space", "error", err)
			return nil, nil, notes, cleanup
		}
	}
	env, proxied := commandEnv(home, cache)
	admin := adminEnv(p.Agent.Env, secrets)
	commandEnvs, token := commandCredentials(p, secrets)
	found := map[string]string{}
	for _, name := range defaultCommands {
		if path, err := exec.LookPath(name); err == nil {
			found[name] = path
		}
	}
	var tools *repoTools
	if len(p.Tools) > 0 {
		var toolNotes []string
		var err error
		tools, toolNotes, err = newRepoTools(ctx, p.Tools, p.Model.GatewayURL, secrets.GatewayToken, home, env, p.Agent.CacheLocked,
			slices.Sorted(maps.Keys(commandEnvs)), token, logger)
		notes = append(notes, toolNotes...)
		switch {
		case err != nil:
			logger.Warn("repository tools not offered", "error", err)
			notes = append(notes, "tools not offered: "+err.Error())
		case tools != nil:
			// A repository's tool replaces a command of its name on PATH.
			maps.Copy(found, tools.offered())
			env = tools.withPath(env)
		}
	}
	found = offered(p, found)
	if len(found) == 0 {
		return nil, nil, notes, cleanup
	}
	_, curl := found["curl"]
	if curl {
		if err := os.WriteFile(filepath.Join(home, ".curlrc"), []byte(curlrc), 0o644); err != nil {
			logger.Warn("commands not offered: no scratch space", "error", err)
			return nil, nil, notes, cleanup
		}
	}
	// Neither HOME nor the scratch directory itself takes a command's
	// writes from here on: the checkout, the cache directory and the
	// upstream directory do.
	if err := lockTree(ctx, home, true); err != nil {
		logger.Warn("commands not offered: home cannot be made read-only", "error", err)
		return nil, nil, notes, cleanup
	}
	if err := os.Chmod(scratch, 0o555); err != nil {
		logger.Warn("commands not offered: scratch cannot be made read-only", "error", err)
		return nil, nil, notes, cleanup
	}
	if tools != nil {
		tools.locked = true
	}
	// The checkout is written on the first command, so a review that reads
	// the head through its tools alone never writes one. When given head, the
	// lock that a split review's parts share for reading the head, the checkout
	// reads the head while it holds that lock. A checkout that stopped at its
	// cap says so with each command's result, since the description was sent
	// before it was written.
	prepare := func(ctx context.Context) (string, error) {
		release, err := acquire(ctx, head)
		if err != nil {
			return "", err
		}
		defer release()
		started := time.Now()
		stats, err := tree.Checkout(ctx, dir, agent.MaxCheckoutBytes)
		if err != nil {
			logger.Warn("checkout not written", "error", err)
			// What a stopped checkout wrote will otherwise be in the way of
			// the next command's, which writes every path anew.
			if err := clearDir(dir); err != nil {
				logger.Warn("checkout not cleared", "error", err)
			}
			return "", checkoutFailure(err)
		}
		logger.Info("checkout written", "files", stats.Files, "bytes", stats.Bytes, "skipped", stats.Skipped,
			"truncated", stats.Truncated, "elapsed", time.Since(started).Round(time.Millisecond))
		if err := markRepository(dir, p.CloneURL); err != nil {
			logger.Warn("checkout not marked as a repository", "error", err)
		}
		if stats.Truncated {
			return fmt.Sprintf("[the checkout stopped at %d MiB, so the paths that sort last are missing]", agent.MaxCheckoutBytes>>20), nil
		}
		return "", nil
	}
	// Ignored paths are left out for the commands' sake, not the agent's: a
	// lockfile the change touches is still the head's and the diff's.
	note := fmt.Sprintf("The checkout leaves out ignored paths, lockfiles and vendored or generated trees among them, symlinks and "+
		"files over %d MiB; read_file still reads a left-out file at the head, and read_diff its part of the pull request's diff.",
		agent.MaxBlobBytes>>20)
	var resolve func(context.Context, string) (string, []string, error)
	if tools != nil {
		resolve = tools.resolveWith(found, append(slices.Clone(env), admin...))
		if tools.pending() {
			note += " A command whose tool is not installed yet is installed on first use, which can take a moment."
		}
	}
	// A cut output is kept beside the checkout, where fetch_repo writes,
	// within the budget that the two share. One file holds at most what
	// read_file reads of a diff, in line ranges.
	budget := agent.NewWriteBudget(fetchWriteBytes)
	run = agent.NewRunTool(agent.RunConfig{
		Dir: dir, Env: append(env, admin...), CommandEnv: commandEnvs, Commands: found, Resolve: resolve, Prepare: prepare,
		Timeout:        cmp.Or(time.Duration(p.Agent.CommandTimeoutSeconds)*time.Second, configfile.DefaultCommandTimeout),
		MaxOutputBytes: maxOutput, Proxied: proxied, Hosts: p.Agent.Hosts, Note: note, Logger: logger,
		Mask: secrets.Mask,
		Keep: &agent.Kept{Dir: up, Rel: upstreamRel, FileBytes: fetchDiffBytes, Budget: budget},
	})
	// An operator who allows only rg or fd has not let the agent reach the
	// network, so fetch_repo comes with a command that does.
	_, gh := found["gh"]
	if gh || curl {
		fetch = &fetchRepoTool{
			dir: up, budget: budget, fetchMax: cmp.Or(p.Agent.Fetches, configfile.DefaultFetches), maxBytes: maxOutput, hosts: p.Agent.Hosts,
		}
	}
	return run, fetch, notes, cleanup
}

// commandCredentials are the variables, by command, that the commands that
// carry a credential get. It also returns the token that the repository's
// tools are looked up with in the GitHub API. gh reaches GitHub over HTTPS,
// which the gateway cannot add a credential to, so it carries the run's
// read-only token. A command that carries a credential is never a binary
// that the repository chose. On another forge, the token is that forge's: gh
// and the lookups get none.
func commandCredentials(p runspec.Spec, secrets runspec.Secrets) (map[string][]string, string) {
	if !onGitHub(p) {
		return map[string][]string{}, ""
	}
	return map[string][]string{"gh": {"GH_TOKEN=" + secrets.GitToken, "GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1"}}, secrets.GitToken
}

// offered is found less gh on another forge than GitHub, where gh,
// signed in nowhere, will only fail.
func offered(p runspec.Spec, found map[string]string) map[string]string {
	if !onGitHub(p) {
		delete(found, "gh")
	}
	return found
}

// onGitHub reports whether the run's repository is on GitHub, where
// runspec.Secrets.GitToken is a GitHub token.
func onGitHub(p runspec.Spec) bool { return p.Forge == "" || p.Forge == string(configfile.ForgeGitHub) }

// curlrc is the configuration that curl reads from HOME. Without a terminal,
// curl prints its progress meter to stderr, which the run tool keeps in the
// result. --silent drops its warnings and errors with the meter. The gateway
// hands redirects back to the client, so curl follows them itself. curl sends
// credentials only to the initial host.
//
// A host that the gateway refuses answers a plain http:// request with a 403.
// Unless it fails on the 403, curl reports that as a success, with exit code
// 0 and the refusal as the body. Failing on the 403 keeps the body, which
// names the reason. The proxy variables cover http and https alone. Without
// the proto line, a URL of any other scheme that curl speaks, given or
// redirected to, connects around the gateway.
const curlrc = "no-progress-meter\nlocation\nfail-with-body\nproto = \"=http,https\"\n"

// clearDir empties dir, which itself stays: its parent, the scratch
// directory, is read-only by then.
func clearDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var errs []error
	for _, e := range entries {
		errs = append(errs, os.RemoveAll(filepath.Join(dir, e.Name())))
	}
	return errors.Join(errs...)
}

// checkoutFailure is a failed checkout as the model and the summary see it.
// The run tool shows a path error by its operation and cause alone, so that
// it never names a path under the scratch space. That loses the checkout
// from the message. Here the path is dropped the same way, but what failed
// to be written stays named.
func checkoutFailure(err error) error {
	if pe, ok := errors.AsType[*fs.PathError](err); ok {
		err = fmt.Errorf("%s: %w", pe.Op, pe.Err)
	}
	return fmt.Errorf("the checkout could not be written: %w", err)
}

// markRepository gives the checkout an empty .git whose origin is the
// repository's clone URL: no history, but enough for a tool that locates a
// repository by its working tree, such as flate, to find this one.
func markRepository(dir, cloneURL string) error {
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		return err
	}
	_, err = repo.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{cloneURL}})
	return err
}

// The admin's variables for the commands cannot set a name that the runner
// sets for a command or a name that carries a credential. They also cannot
// set a name that points a tool at a configuration file or the loader at a
// library. Otherwise, CURL_HOME trades the .curlrc that the runner writes for
// another. LD_PRELOAD loads a library into every command, gh with the run's
// token among them.
var (
	reservedEnv = []string{
		"HOME", "PATH", "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "GITHUB_TOKEN",
		"CURL_HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "RIPGREP_CONFIG_PATH", "DOCKER_CONFIG", "PAGER", "BROWSER", "EDITOR", "VISUAL",
	}
	reservedEnvPrefixes = []string{"GH_", "GIT_", "LD_"}
)

// ReservedEnvName reports whether a variable the admin gives the commands
// cannot be named name.
func ReservedEnvName(name string) bool {
	upper := strings.ToUpper(name)
	return slices.Contains(reservedEnv, upper) ||
		slices.ContainsFunc(reservedEnvPrefixes, func(p string) bool { return strings.HasPrefix(upper, p) })
}

// adminEnv is the admin's variables for the commands, read from the
// runner's own environment under CommandEnvPrefix, where its pod template
// put them. A value from a Secret joins the masked secrets. A variable
// from an optional Secret key that is not set is left out.
func adminEnv(vars []runspec.CommandEnvVar, secrets runspec.Secrets) []string {
	var env []string
	for _, v := range vars {
		value, ok := os.LookupEnv(runspec.CommandEnvPrefix + v.Name)
		if !ok {
			continue
		}
		if v.Secret {
			secrets.Redacted.Add(value)
		}
		env = append(env, v.Name+"="+value)
	}
	return env
}

// commandEnv is the whole environment of a command: PATH, home as HOME,
// cache as XDG_CACHE_HOME, and the pod's proxies under both spellings,
// since curl reads http_proxy only in lower case. The pod's NO_PROXY is
// left out, so curl reaches localhost and the gateway's own host through
// the gateway too. Otherwise, its --connect-to or an --alt-svc cache will
// send a direct connection to one anywhere. proxied reports whether there
// is an HTTPS proxy, the egress gateway.
func commandEnv(home, cache string) (env []string, proxied bool) {
	env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "XDG_CACHE_HOME=" + cache}
	for _, name := range []string{"HTTP_PROXY", httpsProxyEnv} {
		lower := strings.ToLower(name)
		if v := cmp.Or(os.Getenv(name), os.Getenv(lower)); v != "" {
			env = append(env, name+"="+v, lower+"="+v)
			proxied = proxied || name == httpsProxyEnv
		}
	}
	return env, proxied
}
