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
)

// defaultCommands are the commands of the tools the -tools image ships. A
// runner image without mise offers those of them on its PATH.
var defaultCommands = []string{"curl", "fd", "gh", "jq", "rg", "yq"}

// commandTool is the run tool for the commands a run offers over a
// checkout of tree written to scratch space, and fetch_repo, which writes
// beside the checkout, when one of the commands already reaches the
// network. On an image with mise the commands are those of the tools the
// merged mise config declares: the image's system config, and the
// repository's own tools, p.Tools, over it, each installed on first use.
// An image without mise offers the defaultCommands on its PATH.
// head, when not nil, is the lock the parts of a split review share for
// reading the head (partLocks), which the checkout holds while it is
// written. Both tools are nil, with a no-op cleanup, when there is nothing to
// offer or the commands cannot be kept from the runner's secrets; the
// review then goes on with the read-only tools alone. notes say what was
// not offered, for the review's summary, and the values a mise config
// marks for redaction join secrets. cleanup removes the scratch space.
func commandTool(
	ctx context.Context, p Spec, tree *agent.Tree, head chan struct{}, secrets Secrets, maxOutput int, logger *slog.Logger,
) (run *agent.RunTool, fetch *fetchRepoTool, notes []string, cleanup func()) {
	cleanup = func() {}
	_, err := os.Stat(miseBin)
	withMise := err == nil
	var repo *repoMiseConfig
	if withMise {
		if repo, err = newRepoMiseConfig(p.Tools); err != nil {
			logger.Warn("repository tools not offered", "error", err)
			notes = append(notes, "tools not offered: "+err.Error())
		}
	} else if p.Tools != "" {
		notes = append(notes, "tools not offered: the runner image has no mise")
	}
	if err := hideEnviron(); err != nil {
		logger.Warn("commands not offered: the runner's environment cannot be hidden from them", "error", err)
		return nil, nil, notes, cleanup
	}
	scratch, err := os.MkdirTemp("", "kritika-run-")
	if err != nil {
		logger.Warn("commands not offered: no scratch space", "error", err)
		return nil, nil, notes, cleanup
	}
	// HOME is apart from the checkout, or a repository could plant the
	// ~/.curlrc curl reads before its arguments; the runner writes its own,
	// and keeps HOME read-only once the tool is built, so a command cannot
	// rewrite it, or the binaries and config mise keeps there. The cache
	// directory is the commands' XDG_CACHE_HOME, so a tool that keeps a
	// cache there, as flate does, has somewhere to write it that is not
	// HOME.
	dir, home, up, cache := filepath.Join(scratch, "checkout"), filepath.Join(scratch, "home"),
		filepath.Join(scratch, upstreamDir), filepath.Join(scratch, "cache")
	// The scratch space goes however the run ended, so its removal does
	// not stop with ctx. Only home and the scratch directory itself were
	// made read-only; home may not exist yet.
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
	commandEnvs, miseSecrets := p.commandCredentials(secrets)
	found := map[string]string{}
	var mise *miseTools
	if withMise {
		var miseNotes []string
		mise, miseNotes, err = newMiseTools(ctx, miseBin, home, env, admin, p.Agent.CacheLocked, repo,
			slices.Sorted(maps.Keys(commandEnvs)), miseSecrets, logger)
		notes = append(notes, miseNotes...)
		if err != nil {
			logger.Warn("mise tools not offered", "error", err)
			notes = append(notes, "mise tools not offered: "+err.Error())
		} else {
			found = mise.offered()
		}
	} else {
		for _, name := range defaultCommands {
			if path, err := exec.LookPath(name); err == nil {
				found[name] = path
			}
		}
	}
	found = p.offered(found)
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
	if mise != nil {
		mise.locked = true
	}
	// The checkout is written on the first command, so a review that reads
	// the head through its tools alone never writes one; it reads the head
	// holding head, the lock a split review's parts share for that, when
	// given one. A checkout that stopped at its cap says so with each
	// command's result, since the description was sent before it was
	// written.
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
			// What a stopped checkout wrote would be in the way of the
			// next command's, which writes every path anew.
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
	if mise != nil {
		resolve = mise.resolve
		if mise.pending() {
			note += " A command whose tool is not installed yet is installed on first use, which can take a moment."
		}
	}
	// A cut output is kept beside the checkout, where fetch_repo writes,
	// within the budget the two share; one file holds at most what
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

// commandCredentials are the variables the commands that carry a
// credential get, by command, and the secrets mise is given. gh reaches
// GitHub over HTTPS, which the gateway cannot add a credential to, so it
// carries the run's read-only token; a command that carries a credential
// is never a binary the repository chose. On another forge the token is
// that forge's: gh and mise get none.
func (p Spec) commandCredentials(secrets Secrets) (map[string][]string, Secrets) {
	if !p.onGitHub() {
		secrets.GitToken = ""
		return map[string][]string{}, secrets
	}
	return map[string][]string{"gh": {"GH_TOKEN=" + secrets.GitToken, "GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1"}}, secrets
}

// offered is found less gh on another forge than GitHub, where gh,
// signed in nowhere, would only fail.
func (p Spec) offered(found map[string]string) map[string]string {
	if !p.onGitHub() {
		delete(found, "gh")
	}
	return found
}

// curlrc is the configuration curl reads from HOME. Without a terminal
// curl prints its progress meter to stderr, which the run tool keeps in
// the result, and --silent would drop its warnings and errors with it.
// The gateway hands redirects back to the client, so curl follows them
// itself; it sends credentials only to the initial host. A host the
// gateway refuses answers a plain http:// request with a 403, which curl
// would otherwise report as a success, exit code 0 and the refusal as the
// body; failing on it keeps the body, which names the reason. The proxy
// variables cover http and https alone, so a URL of any other scheme curl
// speaks, given or redirected to, would connect around the gateway.
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

// checkoutFailure is a failed checkout as the model and the summary see
// it: the run tool would show a path error by its operation and cause
// alone, so that it never names a path under the scratch space, which
// would lose the checkout from the message; here the path is dropped the
// same way, but what could not be written stays named.
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

// CommandEnvPrefix prefixes, on the runner container, the names of the
// admin's variables for the commands, so they configure the commands and
// never the runner itself: a GODEBUG, TMPDIR or PGHOST of theirs would
// otherwise change how the runner fetches, logs or connects.
const CommandEnvPrefix = "KRITIKA_COMMAND_ENV_"

// adminEnv is the admin's variables for the commands, read from the
// runner's own environment under CommandEnvPrefix, where its pod template
// put them; a value from a Secret joins the masked secrets. A variable
// from an optional Secret key that is not set is left out.
func adminEnv(vars []CommandEnvVar, secrets Secrets) []string {
	var env []string
	for _, v := range vars {
		value, ok := os.LookupEnv(CommandEnvPrefix + v.Name)
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
// the gateway too: its --connect-to or an --alt-svc cache would send a
// direct connection to one anywhere. proxied reports whether there is an
// HTTPS proxy, the egress gateway.
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
