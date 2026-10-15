package runner

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/config"

	"github.com/home-operations/kritika/internal/agent"
	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/repoconfig"
)

// commandTool is the run tool for the commands p allows over a checkout of
// tree written to scratch space, and fetch_repo, which writes beside the
// checkout, when one of the commands already reaches the network. On an
// image with mise the commands are those of the tools the merged mise
// config declares, the image's system config under the instance's, each
// installed on first use; p.Agent.Commands narrows them when set, nil
// offering them all and an empty list none. A name it lists that no mise
// tool provides, and every name on an image without mise, is looked up on
// this image's PATH. With "*" for p.Agent.Commands the repository's own
// mise config among files, read from the merge base, supplies tools too,
// each installed at what its lock pins. Both tools are nil, with a no-op
// cleanup, when there is nothing to offer or the commands cannot be kept
// from the runner's secrets; the review then goes on with the read-only
// tools alone. notes say what was not offered, for the review's summary,
// and the values a mise config marks for redaction join secrets. cleanup
// removes the scratch space.
func commandTool(
	ctx context.Context, p Spec, tree *agent.Tree, files repoconfig.Files, secrets *Secrets, maxOutput int, logger *slog.Logger,
) (run *agent.RunTool, fetch *fetchRepoTool, notes []string, cleanup func()) {
	cleanup = func() {}
	_, err := os.Stat(miseBin)
	withMise := err == nil
	// An empty list offers nothing, so mise need not be asked.
	if len(p.Agent.Commands) == 0 && (!withMise || p.Agent.Commands != nil) {
		return nil, nil, nil, cleanup
	}
	allowed := p.Agent.Commands
	var repo *repoMiseConfig
	if RepositoryTools(allowed) {
		allowed = nil
		repo, notes = readRepoMise(files, withMise, logger)
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
	// rewrite it, or the binaries and config mise keeps there.
	dir, home, up := filepath.Join(scratch, "checkout"), filepath.Join(scratch, "home"), filepath.Join(scratch, upstreamDir)
	cleanup = func() {
		if err := lockTree(scratch, false); err != nil {
			logger.Warn("scratch not made writable for its removal", "error", err)
		}
		_ = os.RemoveAll(scratch)
	}
	for _, d := range []string{dir, home, up} {
		if err := os.Mkdir(d, 0o755); err != nil {
			logger.Warn("commands not offered: no scratch space", "error", err)
			return nil, nil, notes, cleanup
		}
	}
	env, proxied := commandEnv(home)
	found := map[string]string{}
	var mise *miseTools
	if withMise {
		var miseNotes []string
		mise, miseNotes, err = newMiseTools(ctx, miseBin, home, env, p.Agent.MiseConfig, repo, secrets, logger)
		notes = append(notes, miseNotes...)
		if err != nil {
			logger.Warn("mise tools not offered", "error", err)
			notes = append(notes, "mise tools not offered: "+err.Error())
		} else {
			maps.Copy(found, mise.offered(allowed))
			env = mise.commandEnv()
		}
	}
	for _, name := range allowed {
		if _, ok := found[name]; ok {
			continue
		}
		path, err := exec.LookPath(name)
		if err != nil {
			logger.Info("command not offered: no mise tool provides it and it is not on this image's PATH", "command", name)
			continue
		}
		found[name] = path
	}
	if len(found) == 0 {
		return nil, nil, notes, cleanup
	}
	if _, curl := found["curl"]; curl {
		if err := os.WriteFile(filepath.Join(home, ".curlrc"), []byte(curlrc), 0o644); err != nil {
			logger.Warn("commands not offered: no scratch space", "error", err)
			return nil, nil, notes, cleanup
		}
	}
	// Neither HOME nor the scratch directory itself takes a command's
	// writes from here on: the checkout and the upstream directory do.
	if err := lockTree(home, true); err != nil {
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
	started := time.Now()
	stats, err := tree.Checkout(ctx, dir, agent.MaxCheckoutBytes)
	if err != nil {
		logger.Warn("commands not offered: checkout failed", "error", err)
		return nil, nil, notes, cleanup
	}
	logger.Info("checkout written", "files", stats.Files, "bytes", stats.Bytes, "skipped", stats.Skipped,
		"truncated", stats.Truncated, "elapsed", time.Since(started).Round(time.Millisecond))
	if err := markRepository(dir, p.CloneURL); err != nil {
		logger.Warn("checkout not marked as a repository", "error", err)
	}
	// Ignored paths are left out for the commands' sake, not the agent's: a
	// lockfile the change touches is still the head's and the diff's.
	note := fmt.Sprintf("The checkout leaves out ignored paths, lockfiles and vendored or generated trees among them, symlinks and "+
		"files over %d MiB; read_file still reads a left-out file at the head, and read_diff its part of the pull request's diff.",
		agent.MaxBlobBytes>>20)
	if stats.Truncated {
		note += fmt.Sprintf(" It stopped at %d MiB, so the paths that sort last are missing.", agent.MaxCheckoutBytes>>20)
	}
	var resolve func(context.Context, string) (string, []string, error)
	if mise != nil {
		resolve = func(ctx context.Context, command string) (string, []string, error) {
			if mise.provides(command) {
				return mise.resolve(ctx, command)
			}
			if err := mise.usable(); err != nil {
				return "", nil, err
			}
			return found[command], mise.commandEnv(), nil
		}
		if mise.pending(found) {
			note += " A command whose tool is not installed yet is installed on first use, which can take a moment."
		}
	}
	// gh reaches GitHub over HTTPS, which the gateway cannot add a
	// credential to, so it carries the run's read-only token.
	commandEnvs := map[string][]string{"gh": {"GH_TOKEN=" + secrets.GitToken, "GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1"}}
	// A cut output is kept beside the checkout, where fetch_repo writes,
	// within the budget the two share; one file holds at most what
	// read_file reads of a diff, in line ranges.
	budget := agent.NewWriteBudget(fetchWriteBytes)
	run = agent.NewRunTool(agent.RunConfig{
		Dir: dir, Env: env, CommandEnv: commandEnvs, Commands: found, Resolve: resolve,
		Timeout:        time.Duration(p.Agent.CommandTimeoutSeconds) * time.Second,
		MaxOutputBytes: maxOutput, Proxied: proxied, Hosts: p.Agent.Hosts, Note: note, Logger: logger,
		Mask: secrets.Mask,
		Keep: &agent.Kept{Dir: up, Rel: upstreamRel, FileBytes: fetchDiffBytes, Budget: budget},
	})
	// An operator who allows only rg or fd has not let the agent reach the
	// network, so fetch_repo comes with a command that does.
	_, gh := found["gh"]
	_, curl := found["curl"]
	if gh || curl {
		fetch = &fetchRepoTool{
			dir: up, budget: budget, fetchMax: cmp.Or(p.Agent.Fetches, configfile.DefaultFetches), maxBytes: maxOutput, hosts: p.Agent.Hosts,
		}
	}
	return run, fetch, notes, cleanup
}

// curlrc is the configuration curl reads from HOME. Without a terminal
// curl prints its progress meter to stderr, which the run tool keeps in
// the result, and --silent would drop its warnings and errors with it.
// The gateway hands redirects back to the client, so curl follows them
// itself; it sends credentials only to the initial host. A host the
// gateway refuses answers a plain http:// request with a 403, which curl
// would otherwise report as a success, exit code 0 and the refusal as the
// body; failing on it keeps the body, which names the reason.
const curlrc = "no-progress-meter\nlocation\nfail-with-body\n"

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

// commandEnv is the whole environment of a command: PATH, home as HOME,
// and the pod's proxy settings under both spellings, since curl reads
// http_proxy only in lower case. proxied reports whether there is an
// HTTPS proxy, the egress gateway.
func commandEnv(home string) (env []string, proxied bool) {
	env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home}
	for _, name := range []string{"HTTP_PROXY", httpsProxyEnv, "NO_PROXY"} {
		lower := strings.ToLower(name)
		if v := cmp.Or(os.Getenv(name), os.Getenv(lower)); v != "" {
			env = append(env, name+"="+v, lower+"="+v)
			proxied = proxied || name == httpsProxyEnv
		}
	}
	return env, proxied
}
