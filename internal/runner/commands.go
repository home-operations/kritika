package runner

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/config"

	"github.com/home-operations/kritika/internal/agent"
)

// commandTool is the run tool for the commands p allows that this image
// has on its PATH, over a checkout of tree written to scratch space, and
// fetch_repo, which writes beside the checkout, when one of the commands
// already reaches the network. Both are nil, with a no-op cleanup, when
// there is nothing to offer or the commands cannot be kept from the
// runner's secrets; the review then goes on with the read-only tools
// alone. cleanup removes the scratch space.
func commandTool(
	ctx context.Context, p Spec, tree *agent.Tree, gitToken string, maxOutput int, logger *slog.Logger,
) (run *agent.RunTool, fetch *fetchRepoTool, cleanup func()) {
	cleanup = func() {}
	found := map[string]string{}
	for _, name := range p.Agent.Commands {
		path, err := exec.LookPath(name)
		if err != nil {
			logger.Info("command not offered: not on this image's PATH", "command", name)
			continue
		}
		found[name] = path
	}
	if len(found) == 0 {
		return nil, nil, cleanup
	}
	if err := hideEnviron(); err != nil {
		logger.Warn("commands not offered: the runner's environment cannot be hidden from them", "error", err)
		return nil, nil, cleanup
	}
	scratch, err := os.MkdirTemp("", "kritika-run-")
	if err != nil {
		logger.Warn("commands not offered: no scratch space", "error", err)
		return nil, nil, cleanup
	}
	cleanup = func() { _ = os.RemoveAll(scratch) }
	// HOME is apart from the checkout, or a repository could plant the
	// ~/.curlrc curl reads before its arguments.
	dir, home, up := filepath.Join(scratch, "checkout"), filepath.Join(scratch, "home"), filepath.Join(scratch, upstreamDir)
	for _, d := range []string{dir, home, up} {
		if err := os.Mkdir(d, 0o755); err != nil {
			logger.Warn("commands not offered: no scratch space", "error", err)
			return nil, nil, cleanup
		}
	}
	started := time.Now()
	stats, err := tree.Checkout(ctx, dir, agent.MaxCheckoutBytes)
	if err != nil {
		logger.Warn("commands not offered: checkout failed", "error", err)
		return nil, nil, cleanup
	}
	logger.Info("checkout written", "files", stats.Files, "bytes", stats.Bytes, "skipped", stats.Skipped,
		"truncated", stats.Truncated, "elapsed", time.Since(started).Round(time.Millisecond))
	if err := markRepository(dir, p.CloneURL); err != nil {
		logger.Warn("checkout not marked as a repository", "error", err)
	}
	note := fmt.Sprintf("The checkout leaves out ignored paths, symlinks and files over %d MiB.", agent.MaxBlobBytes>>20)
	if stats.Truncated {
		note += fmt.Sprintf(" It stopped at %d MiB, so the paths that sort last are missing.", agent.MaxCheckoutBytes>>20)
	}
	env, proxied := commandEnv(home)
	// gh reaches GitHub over HTTPS, which the gateway cannot add a
	// credential to, so it carries the run's read-only token.
	commandEnvs := map[string][]string{"gh": {"GH_TOKEN=" + gitToken, "GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1"}}
	run = agent.NewRunTool(agent.RunConfig{
		Dir: dir, Env: env, CommandEnv: commandEnvs, Commands: found, Timeout: time.Duration(p.Agent.CommandTimeoutSeconds) * time.Second,
		MaxOutputBytes: maxOutput, Proxied: proxied, Note: note,
		Mask: Secrets{GitToken: gitToken}.Mask,
	})
	// An operator who allows only rg or fd has not let the agent reach the
	// network, so fetch_repo comes with a command that does.
	if found["gh"] != "" || found["curl"] != "" {
		fetch = &fetchRepoTool{dir: up}
	}
	return run, fetch, cleanup
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
