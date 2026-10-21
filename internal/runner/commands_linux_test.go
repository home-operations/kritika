package runner

import (
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

	"github.com/go-git/go-git/v6"

	"github.com/home-operations/kritika/internal/agent"
)

// environHelper names the role the test binary plays when TestHideEnviron
// runs it again: "keep" or "hide" start a reader and report what it saw,
// "read" is the reader.
const environHelper = "KRITIKA_TEST_ENVIRON"

// TestHideEnviron checks the property the run tool relies on: once the
// runner is non-dumpable, a child with the runner's uid cannot read the
// runner's environment from /proc.
func TestHideEnviron(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	switch mode := os.Getenv(environHelper); mode {
	case "read":
		if _, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", os.Getppid())); err != nil {
			fmt.Print("denied")
		} else {
			fmt.Print("readable")
		}
		os.Exit(0)
	case "keep", "hide":
		if mode == "hide" {
			if err := hideEnviron(); err != nil {
				fmt.Print("hideEnviron: ", err)
				os.Exit(0)
			}
		}
		reader := exec.Command(self, "-test.run=^TestHideEnviron$")
		reader.Env = append(os.Environ(), environHelper+"=read")
		out, _ := reader.Output()
		fmt.Print(string(out))
		os.Exit(0)
	}
	for mode, want := range map[string]string{"keep": "readable", "hide": "denied"} {
		cmd := exec.Command(self, "-test.run=^TestHideEnviron$")
		cmd.Env = append(os.Environ(), environHelper+"="+mode)
		out, err := cmd.Output()
		if err != nil || string(out) != want {
			t.Fatalf("%s: a child read the parent's environ: %q, %v; want %q", mode, out, err, want)
		}
	}
}

func TestCommandTool(t *testing.T) {
	// commandTool hides the environment of the process it runs in, this
	// test binary; make it dumpable again afterwards.
	t.Cleanup(func() { _, _, _ = syscall.RawSyscall(syscall.SYS_PRCTL, syscall.PR_SET_DUMPABLE, 1, 0) })
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.Symlink(self, filepath.Join(bin, "rg")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("HTTPS_PROXY", "http://gateway:8082")
	t.Setenv("TMPDIR", t.TempDir())
	was := miseBin
	miseBin = filepath.Join(t.TempDir(), "no-mise")
	t.Cleanup(func() { miseBin = was })
	logger := slog.New(slog.DiscardHandler)
	head := agent.NewTree(tree(t, map[string]string{"main.go": "package main\n", "vendor/x.go": "package x\n"}), []string{"vendor/**"})

	t.Run("offers the default commands on PATH, over a checkout", func(t *testing.T) {
		s := reviewSpec()
		s.Agent.CommandTimeoutSeconds = 5
		run, fetch, _, cleanup := commandTool(t.Context(), s, head, Secrets{GitToken: "ghs_run", Redacted: &Redactions{}}, 1024, logger)
		if run == nil {
			t.Fatal("no run tool")
		}
		if fetch != nil {
			t.Fatal("fetch_repo offered beside rg alone, which reaches no network")
		}
		if !slices.Equal(run.Names(), []string{"rg"}) {
			t.Fatalf("names = %v", run.Names())
		}
		def := run.Def()
		if !strings.Contains(def.Description, "leaves out ignored paths, lockfiles and vendored or generated trees among them, symlinks and files over 1 MiB; read_file still reads a left-out file at the head, and read_diff its part of the pull request's diff.") || !strings.Contains(def.Description, "after 5s") {
			t.Fatalf("description = %q", def.Description)
		}
		scratch, err := filepath.Glob(filepath.Join(os.Getenv("TMPDIR"), "kritika-run-*"))
		if err != nil || len(scratch) != 1 {
			t.Fatalf("scratch = %v, %v", scratch, err)
		}
		checkFirstCommandCheckout(t, run, filepath.Join(scratch[0], "checkout"), s.CloneURL)
		if _, err := os.Stat(filepath.Join(scratch[0], "home", ".curlrc")); !os.IsNotExist(err) {
			t.Fatalf(".curlrc written for a curl that is not offered: %v", err)
		}
		k := run.Kept()
		if k == nil || *k != (agent.Kept{Dir: filepath.Join(scratch[0], upstreamDir), Rel: upstreamRel, FileBytes: fetchDiffBytes, Budget: k.Budget}) {
			t.Fatalf("a cut output is kept at %+v, want beside the checkout in %s", k, scratch[0])
		}
		cleanup()
		if _, err := os.Stat(scratch[0]); !os.IsNotExist(err) {
			t.Fatalf("scratch left behind: %v", err)
		}
	})

	t.Run("fetch_repo beside a command that reaches the network", func(t *testing.T) {
		if err := os.Symlink(self, filepath.Join(bin, "gh")); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = os.Remove(filepath.Join(bin, "gh")) }()
		s := reviewSpec()
		s.Agent.CommandTimeoutSeconds = 5
		run, fetch, _, cleanup := commandTool(t.Context(), s, head, Secrets{GitToken: "ghs_run", Redacted: &Redactions{}}, 1024, logger)
		defer cleanup()
		if run == nil || fetch == nil || !slices.Equal(run.Names(), []string{"gh", "rg"}) {
			t.Fatalf("run tool %v, fetch_repo %v", run, fetch != nil)
		}
		scratch, err := filepath.Glob(filepath.Join(os.Getenv("TMPDIR"), "kritika-run-*"))
		if err != nil || len(scratch) != 1 {
			t.Fatalf("scratch = %v, %v", scratch, err)
		}
		if info, err := os.Stat(fetch.dir); err != nil || !info.IsDir() || fetch.dir != filepath.Join(scratch[0], upstreamDir) {
			t.Fatalf("fetch_repo writes to %s, not beside the checkout in %s: %v", fetch.dir, scratch[0], err)
		}
	})

}

// checkFirstCommandCheckout: the checkout at dir waits for the first
// command, which finds the head's files there, less the ignored paths,
// marked as a repository whose origin is cloneURL.
func checkFirstCommandCheckout(t *testing.T, run *agent.RunTool, dir, cloneURL string) {
	t.Helper()
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatalf("checkout written before a command ran: %v, %v", entries, err)
	}
	out, err := run.Run(t.Context(), fetchInput(t, map[string]any{"command": "rg", "args": []string{"-test.run=^$"}}))
	if err != nil || !strings.HasPrefix(out, "exit code 0\n") {
		t.Fatalf("out = %q, err = %v", out, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "main.go")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "vendor")); !os.IsNotExist(err) {
		t.Fatalf("ignored path checked out: %v", err)
	}
	repo, err := git.PlainOpen(dir)
	if err != nil {
		t.Fatalf("checkout is not a repository: %v", err)
	}
	if origin, err := repo.Remote("origin"); err != nil || !slices.Equal(origin.Config().URLs, []string{cloneURL}) {
		t.Fatalf("origin = %v, %v; want %s", origin, err, cloneURL)
	}
}

// TestCommandToolAdminEnv: the admin's variables reach a command from the
// runner's own environment, where they are prefixed, never one of the
// runner's own of the same name, and what it prints of a Secret's is
// masked.
func TestCommandToolAdminEnv(t *testing.T) {
	// The run tool runs this binary as rg with the argument "adminenv":
	// report the variables it was given.
	if flag.Arg(0) == "adminenv" {
		for _, name := range []string{"KONFLATE_URL", "API_TOKEN", "UNSET_KEY", "RUNNER_ONLY"} {
			v, ok := os.LookupEnv(name)
			fmt.Printf("%s=%s %v\n", name, v, ok)
		}
		os.Exit(0)
	}
	t.Cleanup(func() { _, _, _ = syscall.RawSyscall(syscall.SYS_PRCTL, syscall.PR_SET_DUMPABLE, 1, 0) })
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.Symlink(self, filepath.Join(bin, "rg")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv(CommandEnvPrefix+"KONFLATE_URL", "https://konflate.internal.example")
	t.Setenv(CommandEnvPrefix+"API_TOKEN", "admin-secret-token")
	t.Setenv("KONFLATE_URL", "the runner's own")
	t.Setenv("RUNNER_ONLY", "not the commands'")
	was := miseBin
	miseBin = filepath.Join(t.TempDir(), "no-mise")
	t.Cleanup(func() { miseBin = was })
	head := agent.NewTree(tree(t, map[string]string{"main.go": "package main\n"}), nil)
	s := reviewSpec()
	s.Agent.CommandTimeoutSeconds = 5
	s.Agent.Env = []CommandEnvVar{{Name: "KONFLATE_URL"}, {Name: "API_TOKEN", Secret: true}, {Name: "UNSET_KEY", Secret: true}}
	run, _, _, cleanup := commandTool(t.Context(), s, head, Secrets{GitToken: "ghs_run", Redacted: &Redactions{}}, 1024, slog.New(slog.DiscardHandler))
	defer cleanup()
	if run == nil {
		t.Fatal("no run tool")
	}
	out, err := run.Run(t.Context(), fetchInput(t, map[string]any{"command": "rg", "args": []string{"-test.run=^TestCommandToolAdminEnv$", "adminenv"}}))
	if err != nil {
		t.Fatal(err)
	}
	// Stderr shares the output in arrival order, and a coverage build of
	// this binary warns there that GOCOVERDIR is unset, so each line is
	// looked for on its own.
	for _, want := range []string{
		"KONFLATE_URL=https://konflate.internal.example true\n", "API_TOKEN=*** true\n", "UNSET_KEY= false\n", "RUNNER_ONLY= false\n",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("out = %q; want a line %q", out, want)
		}
	}
}

// TestCommandToolCache: a command writes under XDG_CACHE_HOME, the cache
// directory of the run's scratch space, where a tool that keeps a cache
// there, as flate does, has it; HOME still takes no write.
func TestCommandToolCache(t *testing.T) {
	// The run tool runs this binary as rg with the argument "cache": a
	// bare environment carries no helper variable, so the role is an
	// argument. Report where a write lands.
	if flag.Arg(0) == "cache" {
		for _, name := range []string{"XDG_CACHE_HOME", "HOME"} {
			fmt.Printf("%s: %v\n", name, os.Mkdir(filepath.Join(os.Getenv(name), "flate"), 0o755))
		}
		os.Exit(0)
	}
	t.Cleanup(func() { _, _, _ = syscall.RawSyscall(syscall.SYS_PRCTL, syscall.PR_SET_DUMPABLE, 1, 0) })
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.Symlink(self, filepath.Join(bin, "rg")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("TMPDIR", t.TempDir())
	was := miseBin
	miseBin = filepath.Join(t.TempDir(), "no-mise")
	t.Cleanup(func() { miseBin = was })
	head := agent.NewTree(tree(t, map[string]string{"main.go": "package main\n"}), nil)
	s := reviewSpec()
	s.Agent.CommandTimeoutSeconds = 5
	run, _, _, cleanup := commandTool(t.Context(), s, head, Secrets{GitToken: "ghs_run", Redacted: &Redactions{}}, 1024, slog.New(slog.DiscardHandler))
	if run == nil {
		cleanup()
		t.Fatal("no run tool")
	}
	scratch, err := filepath.Glob(filepath.Join(os.Getenv("TMPDIR"), "kritika-run-*"))
	if err != nil || len(scratch) != 1 {
		cleanup()
		t.Fatalf("scratch = %v, %v", scratch, err)
	}
	out, err := run.Run(t.Context(), fetchInput(t, map[string]any{"command": "rg", "args": []string{"-test.run=^TestCommandToolCache$", "cache"}}))
	if err != nil || !strings.Contains(out, "XDG_CACHE_HOME: <nil>\n") {
		cleanup()
		t.Fatalf("writes = %q, %v; want one under XDG_CACHE_HOME", out, err)
	}
	// Root writes a read-only HOME regardless.
	if os.Geteuid() != 0 && !strings.Contains(out, "HOME: mkdir "+filepath.Join(scratch[0], "home", "flate")+": permission denied\n") {
		cleanup()
		t.Fatalf("writes = %q; want none under HOME", out)
	}
	if info, err := os.Stat(filepath.Join(scratch[0], "cache", "flate")); err != nil || !info.IsDir() {
		cleanup()
		t.Fatalf("XDG_CACHE_HOME is not the scratch space's cache directory: %v", err)
	}
	// What a command left in the cache goes with the scratch space.
	cleanup()
	if _, err := os.Stat(scratch[0]); !os.IsNotExist(err) {
		t.Fatalf("scratch left behind: %v", err)
	}
}

// TestCommandToolNothingToOffer: a PATH without a default command offers
// no run tool, and a repository config needs mise to be read.
func TestCommandToolNothingToOffer(t *testing.T) {
	t.Cleanup(func() { _, _, _ = syscall.RawSyscall(syscall.SYS_PRCTL, syscall.PR_SET_DUMPABLE, 1, 0) })
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.Symlink(self, filepath.Join(bin, "helm")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	was := miseBin
	miseBin = filepath.Join(t.TempDir(), "no-mise")
	t.Cleanup(func() { miseBin = was })
	head := agent.NewTree(tree(t, map[string]string{"main.go": "package main\n"}), nil)
	s := reviewSpec()
	s.Agent.CommandTimeoutSeconds = 5
	s.Tools = "[tools]\n"
	run, fetch, notes, cleanup := commandTool(t.Context(), s, head, Secrets{GitToken: "ghs_run"}, 1024, slog.New(slog.DiscardHandler))
	cleanup()
	if run != nil || fetch != nil || !slices.Equal(notes, []string{"tools not offered: the runner image has no mise"}) {
		t.Fatalf("run tool %v, fetch_repo %v, notes %v", run, fetch != nil, notes)
	}
}

func TestCommandToolCurlrc(t *testing.T) {
	t.Cleanup(func() { _, _, _ = syscall.RawSyscall(syscall.SYS_PRCTL, syscall.PR_SET_DUMPABLE, 1, 0) })
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.Symlink(self, filepath.Join(bin, "curl")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("HTTPS_PROXY", "http://gateway:8082")
	t.Setenv("TMPDIR", t.TempDir())
	head := agent.NewTree(tree(t, map[string]string{"main.go": "package main\n"}), nil)
	s := reviewSpec()
	s.Agent.CommandTimeoutSeconds, s.Agent.Hosts = 5, []string{"github.com", "ghcr.io"}
	run, fetch, _, cleanup := commandTool(t.Context(), s, head, Secrets{GitToken: "ghs_run", Redacted: &Redactions{}}, 1024, slog.New(slog.DiscardHandler))
	defer cleanup()
	if run == nil || fetch == nil {
		t.Fatalf("run tool %v, fetch_repo %v", run != nil, fetch != nil)
	}
	for _, d := range []string{run.Def().Description, fetch.Def().Description} {
		if !strings.Contains(d, "allows only these hosts: github.com, ghcr.io") {
			t.Fatalf("description = %q", d)
		}
	}
	scratch, err := filepath.Glob(filepath.Join(os.Getenv("TMPDIR"), "kritika-run-*"))
	if err != nil || len(scratch) != 1 {
		t.Fatalf("scratch = %v, %v", scratch, err)
	}
	curlrc := filepath.Join(scratch[0], "home", ".curlrc")
	if b, err := os.ReadFile(curlrc); err != nil || string(b) != "no-progress-meter\nlocation\nfail-with-body\nproto = \"=http,https\"\n" {
		t.Fatalf(".curlrc = %q, %v", b, err)
	}
	// A command may not rewrite it, with a trace that prints the proxy
	// credential, say: HOME is read-only once the tool is built.
	if info, err := os.Stat(curlrc); err != nil || info.Mode().Perm()&0o222 != 0 {
		t.Fatalf(".curlrc mode = %v, %v; want read-only", info.Mode().Perm(), err)
	}
}
