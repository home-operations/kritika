package runner

import (
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/home-operations/kritika/internal/agent"
	"github.com/home-operations/kritika/internal/configfile"
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
	head := agent.NewTree(tree(t, map[string]string{"values.yaml": "image: x\n"}), configfile.DefaultIgnore)
	for _, tt := range []struct {
		name, tools string
		want        []string
	}{
		{"the image's tools", "", []string{"yq"}},
		{"the repository's tools merged", "[tools]\nhelm = '4.3.0'\n", []string{"helm", "yq"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := reviewSpec()
			s.Agent.CommandTimeoutSeconds = 10
			s.Tools = tt.tools
			run, fetch, notes, cleanup := commandTool(t.Context(), s, head, nil, Secrets{GitToken: "ghs_run", Redacted: &Redactions{}}, 4096, logger)
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
			paths := []string{home}
			if tt.tools != "" {
				config, err := os.ReadFile(filepath.Join(home, "mise.toml"))
				if err != nil || string(config) != tt.tools {
					t.Fatalf("mise config = %q, %v", config, err)
				}
				paths = append(paths, filepath.Join(home, "mise.toml"))
			} else if _, err := os.Stat(filepath.Join(home, "mise.toml")); !os.IsNotExist(err) {
				t.Fatalf("repository config written without one: %v", err)
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

// TestCommandToolMisePATH: on an image with mise, a default command on
// PATH is offered beside the mise tools and runs in mise's environment,
// and a mise tool replaces a default command of its name.
func TestCommandToolMisePATH(t *testing.T) {
	// The run tool runs this binary as grep with the argument "misepath":
	// report what mise's environment sets.
	if flag.Arg(0) == "misepath" {
		fmt.Printf("REPO_MARKER=%s\n", os.Getenv("REPO_MARKER"))
		os.Exit(0)
	}
	t.Cleanup(func() { _, _, _ = syscall.RawSyscall(syscall.SYS_PRCTL, syscall.PR_SET_DUMPABLE, 1, 0) })
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// The yq on PATH prints nothing, where mise's prints the value read.
	quiet, err := exec.LookPath("true")
	if err != nil {
		t.Fatal(err)
	}
	bin, dataDir := hostMise(t)
	t.Setenv("MISE_SYSTEM_DATA_DIR", dataDir)
	systemMiseConfig(t, "[env]\nREPO_MARKER = \"the image's\"\n\n[tools]\nyq = \""+testYqVersion+"\"\n")
	path := t.TempDir()
	for name, target := range map[string]string{"grep": self, "yq": quiet} {
		if err := os.Symlink(target, filepath.Join(path, name)); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", path)
	t.Setenv("TMPDIR", t.TempDir())
	was, defaults := miseBin, defaultCommands
	miseBin, defaultCommands = bin, []string{"grep", "yq"}
	t.Cleanup(func() { miseBin, defaultCommands = was, defaults })
	head := agent.NewTree(tree(t, map[string]string{"values.yaml": "image: x\n"}), configfile.DefaultIgnore)
	s := reviewSpec()
	s.Agent.CommandTimeoutSeconds = 10
	run, _, notes, cleanup := commandTool(t.Context(), s, head, nil, Secrets{GitToken: "ghs_run", Redacted: &Redactions{}}, 4096,
		slog.New(slog.DiscardHandler))
	defer cleanup()
	if run == nil || !slices.Equal(run.Names(), []string{"grep", "yq"}) {
		t.Fatalf("run tool %v, notes %v", run, notes)
	}
	out, err := run.Run(t.Context(), json.RawMessage(`{"command":"grep","args":["-test.run=^TestCommandToolMisePATH$","misepath"]}`))
	if err != nil || !strings.Contains(out, "REPO_MARKER=the image's\n") {
		t.Fatalf("grep's out = %q, %v; want it run in mise's environment", out, err)
	}
	out, err = run.Run(t.Context(), json.RawMessage(`{"command":"yq","args":[".image","values.yaml"]}`))
	if err != nil || !strings.HasPrefix(out, "exit code 0\nx\n") {
		t.Fatalf("yq's out = %q, %v; want mise's yq", out, err)
	}
}

// TestCommandToolMiseAdminEnv: on an image with mise, the admin's
// variables reach the commands over the image config's [env], whose value
// for the same name is dropped with a note.
func TestCommandToolMiseAdminEnv(t *testing.T) {
	t.Cleanup(func() { _, _, _ = syscall.RawSyscall(syscall.SYS_PRCTL, syscall.PR_SET_DUMPABLE, 1, 0) })
	bin, dataDir := hostMise(t)
	t.Setenv("MISE_SYSTEM_DATA_DIR", dataDir)
	systemMiseConfig(t, "[env]\nREPO_MARKER = \"the image's\"\n\n[tools]\nyq = \""+testYqVersion+"\"\n")
	t.Setenv("PATH", t.TempDir())
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv(CommandEnvPrefix+"REPO_MARKER", "the admin's")
	was := miseBin
	miseBin = bin
	t.Cleanup(func() { miseBin = was })
	head := agent.NewTree(tree(t, map[string]string{"values.yaml": "image: x\n"}), configfile.DefaultIgnore)
	s := reviewSpec()
	s.Agent.CommandTimeoutSeconds = 10
	s.Agent.Env = []CommandEnvVar{{Name: "REPO_MARKER"}}
	run, _, notes, cleanup := commandTool(t.Context(), s, head, nil, Secrets{GitToken: "ghs_run", Redacted: &Redactions{}}, 4096,
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
