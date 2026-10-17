package runner

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/home-operations/kritika/internal/agent"
	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/repoconfig"
)

// TestCommandToolMise: on an image with mise, the run tool offers the
// merged config's commands, the repository's tools among them, and runs
// them in mise's environment.
func TestCommandToolMise(t *testing.T) {
	t.Cleanup(func() { _, _, _ = syscall.RawSyscall(syscall.SYS_PRCTL, syscall.PR_SET_DUMPABLE, 1, 0) })
	bin, dataDir := hostMise(t)
	t.Setenv("MISE_SYSTEM_DATA_DIR", dataDir)
	systemMiseConfig(t, testMiseConfig)
	t.Setenv("PATH", t.TempDir())
	t.Setenv("TMPDIR", t.TempDir())
	was := miseBin
	miseBin = bin
	t.Cleanup(func() { miseBin = was })
	logger := slog.New(slog.DiscardHandler)
	head := agent.NewTree(tree(t, map[string]string{
		"values.yaml": "image: x\n", RepoMiseConfig: "[tools]\nhelm = '99.0.0'\n", RepoMiseLock: "head lock",
	}), configfile.DefaultIgnore)
	files, _, err := repoFiles(tree(t, map[string]string{
		RepoMiseConfig: "[tools]\nhelm = '4.3.0'\n[env]\nREPO_MARKER = 'merge base'\n", RepoMiseLock: repoMiseLockDoc,
	}), []string{RepoMiseConfig, RepoMiseLock})
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name  string
		files repoconfig.Files
		want  []string
	}{
		{"the image's tools", nil, []string{"yq"}},
		{"the repository's tools merged", files, []string{"helm", "yq"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := reviewSpec()
			s.Agent.CommandTimeoutSeconds = 10
			run, fetch, notes, cleanup := commandTool(t.Context(), s, head, tt.files, Secrets{GitToken: "ghs_run", Redacted: &Redactions{}}, 4096, logger)
			defer cleanup()
			if fetch != nil {
				t.Fatal("fetch_repo offered without a command that reaches the network")
			}
			if run == nil || !slices.Equal(run.Names(), tt.want) {
				t.Fatalf("run tool %v, notes %v", run, notes)
			}
			out, err := run.Run(t.Context(), json.RawMessage(`{"command":"yq","args":[".image","values.yaml"]}`))
			if err != nil || !strings.HasPrefix(out, "exit code 0\nx\n") {
				t.Fatalf("out = %q, err = %v", out, err)
			}
			// What mise keeps under HOME, the config and the run's own
			// installs among it, is read-only while a command may run.
			scratch, err := filepath.Glob(filepath.Join(os.Getenv("TMPDIR"), "kritika-run-*"))
			if err != nil || len(scratch) != 1 {
				t.Fatalf("scratch = %v, %v", scratch, err)
			}
			home := filepath.Join(scratch[0], "home")
			if tt.files != nil {
				out, err := run.Run(t.Context(), json.RawMessage(`{"command":"yq","args":["env(REPO_MARKER)","values.yaml"]}`))
				if err != nil || !strings.HasPrefix(out, "exit code 0\nmerge base\n") {
					t.Fatalf("repository env = %q, %v", out, err)
				}
				config, err := os.ReadFile(filepath.Join(home, "mise.toml"))
				if err != nil || !strings.Contains(string(config), "4.3.0") || strings.Contains(string(config), "99.0.0") {
					t.Fatalf("mise config = %q, %v", config, err)
				}
			} else if _, err := os.Stat(filepath.Join(home, "mise.toml")); !os.IsNotExist(err) {
				t.Fatalf("repository config written without one: %v", err)
			}
			if _, err := os.Stat(filepath.Join(scratch[0], "checkout", RepoMiseLock)); !os.IsNotExist(err) {
				t.Fatalf("lock written to checkout: %v", err)
			}
			config, err := os.ReadFile(filepath.Join(scratch[0], "checkout", RepoMiseConfig))
			if err != nil || !strings.Contains(string(config), "99.0.0") {
				t.Fatalf("checkout config = %q, %v", config, err)
			}
			paths := []string{home}
			if tt.files != nil {
				paths = append(paths, filepath.Join(home, "mise.toml"), filepath.Join(home, "mise.lock"))
			}
			for _, path := range paths {
				if info, err := os.Stat(path); err != nil || info.Mode().Perm()&0o222 != 0 {
					t.Fatalf("%s: mode %v, %v; want read-only", path, info.Mode().Perm(), err)
				}
			}
			if os.Geteuid() != 0 {
				if err := os.WriteFile(filepath.Join(home, ".curlrc"), []byte("trace /tmp/x\n"), 0o644); err == nil {
					t.Fatal("a command could write HOME")
				}
			}
			cleanup()
			if _, err := os.Stat(scratch[0]); !os.IsNotExist(err) {
				t.Fatalf("scratch left behind: %v", err)
			}
		})
	}
}

// TestCommandToolMiseAdminEnv: on an image with mise, the admin's
// variables reach the commands over the repository's [env], whose value
// for the same name is dropped with a note.
func TestCommandToolMiseAdminEnv(t *testing.T) {
	t.Cleanup(func() { _, _, _ = syscall.RawSyscall(syscall.SYS_PRCTL, syscall.PR_SET_DUMPABLE, 1, 0) })
	bin, dataDir := hostMise(t)
	t.Setenv("MISE_SYSTEM_DATA_DIR", dataDir)
	systemMiseConfig(t, testMiseConfig)
	t.Setenv("PATH", t.TempDir())
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv(CommandEnvPrefix+"REPO_MARKER", "the admin's")
	was := miseBin
	miseBin = bin
	t.Cleanup(func() { miseBin = was })
	head := agent.NewTree(tree(t, map[string]string{"values.yaml": "image: x\n"}), configfile.DefaultIgnore)
	files, _, err := repoFiles(tree(t, map[string]string{
		RepoMiseConfig: "[tools]\nhelm = '4.3.0'\n[env]\nREPO_MARKER = 'merge base'\n", RepoMiseLock: repoMiseLockDoc,
	}), []string{RepoMiseConfig, RepoMiseLock})
	if err != nil {
		t.Fatal(err)
	}
	s := reviewSpec()
	s.Agent.CommandTimeoutSeconds = 10
	s.Agent.Env = []CommandEnvVar{{Name: "REPO_MARKER"}}
	run, _, notes, cleanup := commandTool(t.Context(), s, head, files, Secrets{GitToken: "ghs_run", Redacted: &Redactions{}}, 4096,
		slog.New(slog.DiscardHandler))
	defer cleanup()
	if run == nil {
		t.Fatalf("no run tool: %v", notes)
	}
	if !slices.Contains(notes, "mise env REPO_MARKER not set for the commands: the admin sets it") {
		t.Fatalf("notes = %v", notes)
	}
	out, err := run.Run(t.Context(), json.RawMessage(`{"command":"yq","args":["env(REPO_MARKER)","values.yaml"]}`))
	if err != nil || !strings.HasPrefix(out, "exit code 0\nthe admin's\n") {
		t.Fatalf("REPO_MARKER = %q, %v; want the admin's", out, err)
	}
}
