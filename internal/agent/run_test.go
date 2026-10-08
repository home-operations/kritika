package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-billy/v6/memfs"
	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/go-git/go-git/v6/storage/memory"

	"github.com/home-operations/kritika/internal/gittest"
)

// helperEnv makes the test binary act as a command for the run tool, so
// the tests run a real process without depending on what the host has on
// its PATH.
const helperEnv = "KRITIKA_AGENT_TEST_HELPER"

func TestMain(m *testing.M) {
	if os.Getenv(helperEnv) == "1" {
		os.Exit(helper(os.Args[1:]))
	}
	os.Exit(m.Run())
}

// helper is the command: "exit N" exits N, "sleep" outlives any test
// timeout, "flood N" writes N bytes, "fail-after N" writes N bytes and then
// an error to stderr, "unknown X" fails as gh does for a command X it lacks
// and "quote X" prints the same and succeeds, "--fail-test ..." fails, and
// anything else prints the working directory, the environment and the
// arguments.
func helper(args []string) int {
	switch {
	case len(args) > 0 && args[0] == "--fail-test":
		return 22
	case len(args) == 2 && (args[0] == "unknown" || args[0] == "quote"):
		fmt.Fprintf(os.Stderr, "unknown command %q for \"gh\"\n\nUsage:  gh <command> <subcommand> [flags]\n\nAvailable commands:\n  api\n", args[1])
		if args[0] == "quote" {
			return 0
		}
		return 1
	case len(args) == 2 && args[0] == "fail-after":
		n, _ := strconv.Atoi(args[1])
		fmt.Print(strings.Repeat("x", n))
		// Long enough for the run tool to have read stdout first.
		time.Sleep(200 * time.Millisecond)
		fmt.Fprintln(os.Stderr, "failed: the end")
		return 1
	case len(args) == 2 && args[0] == "exit":
		n, _ := strconv.Atoi(args[1])
		fmt.Println("exiting")
		return n
	case len(args) == 1 && args[0] == "sleep":
		time.Sleep(time.Minute)
		return 0
	case len(args) == 2 && args[0] == "flood":
		n, _ := strconv.Atoi(args[1])
		fmt.Print(strings.Repeat("x", n))
		return 0
	}
	wd, _ := os.Getwd()
	fmt.Println("wd=" + wd)
	for _, e := range os.Environ() {
		fmt.Println("env=" + e)
	}
	for _, a := range args {
		fmt.Println("arg=" + a)
	}
	return 0
}

func newTestRunTool(t *testing.T, proxied bool) (*RunTool, string) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	return NewRunTool(RunConfig{
		// A coverage build of the helper warns on exit without GOCOVERDIR.
		Dir: dir, Env: []string{helperEnv + "=1", "HOME=/nowhere", "GOCOVERDIR=" + t.TempDir()},
		Commands: map[string]string{"rg": self, "curl": self},
		Timeout:  2 * time.Second, MaxOutputBytes: 4096, Proxied: proxied,
	}), dir
}

func TestRunTool(t *testing.T) {
	rt, dir := newTestRunTool(t, false)

	t.Run("runs the binary directly in the checkout with only the given environment", func(t *testing.T) {
		out, err := rt.Run(t.Context(), json.RawMessage(`{"command":"rg","args":["a b","$HOME","*.go"]}`))
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSpace(out), "\n")
		if lines[0] != "exit code 0" || !slices.Contains(lines, "arg=a b") || !slices.Contains(lines, "arg=$HOME") ||
			!slices.Contains(lines, "arg=*.go") {
			t.Fatalf("out = %q", out)
		}
		// The temp dir may sit behind a symlink (macOS's /var).
		real, _ := filepath.EvalSymlinks(dir)
		if !slices.Contains(lines, "wd="+dir) && !slices.Contains(lines, "wd="+real) {
			t.Fatalf("out = %q, want wd=%s", out, dir)
		}
		var env []string
		for _, l := range lines {
			if v, ok := strings.CutPrefix(l, "env="); ok {
				env = append(env, v)
			}
		}
		if !slices.Equal(env, rt.cfg.Env) {
			t.Fatalf("env = %q", env)
		}
	})

	t.Run("a non-zero exit is output, not an error", func(t *testing.T) {
		out, err := rt.Run(t.Context(), json.RawMessage(`{"command":"rg","args":["exit","3"]}`))
		if err != nil || out != "exit code 3\nexiting\n" {
			t.Fatalf("out = %q, err = %v", out, err)
		}
	})

	t.Run("a command not on the list is refused", func(t *testing.T) {
		if _, err := rt.Run(t.Context(), json.RawMessage(`{"command":"sh","args":["-c","id"]}`)); err == nil ||
			!strings.Contains(err.Error(), "not one of curl, rg") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("output is capped", func(t *testing.T) {
		out, err := rt.Run(t.Context(), json.RawMessage(`{"command":"rg","args":["flood","5000"]}`))
		kept := strings.Count(strings.TrimPrefix(out, "exit code 0\n"), "x")
		if err != nil || len(out) > 4096 || !strings.HasPrefix(out, "exit code 0\n") ||
			!strings.HasSuffix(out, fmt.Sprintf("\n[truncated %d bytes]", 5000-kept)) {
			t.Fatalf("out = %d bytes ending %q, err = %v; want at most 4096 with the bytes dropped counted", len(out), out[max(len(out)-40, 0):], err)
		}
	})

	t.Run("the end of stderr outlives a cut", func(t *testing.T) {
		out, err := rt.Run(t.Context(), json.RawMessage(`{"command":"rg","args":["fail-after","8000"]}`))
		if err != nil || len(out) > 4096 || !strings.HasPrefix(out, "exit code 1\nxxx") ||
			!strings.HasSuffix(out, "]\n[the end of stderr, from the part cut above:]\nfailed: the end\n") {
			t.Fatalf("out = %d bytes ending %q, err = %v; want at most 4096 ending in the error", len(out), out[max(len(out)-120, 0):], err)
		}
		if n := strings.Count(out, "failed: the end"); n != 1 {
			t.Fatalf("the error is in the output %d times", n)
		}
	})

	t.Run("an uncut output shows its stderr in place", func(t *testing.T) {
		out, err := rt.Run(t.Context(), json.RawMessage(`{"command":"rg","args":["fail-after","10"]}`))
		if err != nil || out != "exit code 1\nxxxxxxxxxxfailed: the end\n" {
			t.Fatalf("out = %q, err = %v", out, err)
		}
	})

	t.Run("a command past its timeout is stopped", func(t *testing.T) {
		slow := NewRunTool(RunConfig{Dir: dir, Env: rt.cfg.Env, Commands: rt.cfg.Commands, Timeout: 100 * time.Millisecond, MaxOutputBytes: 64})
		start := time.Now()
		out, err := slow.Run(t.Context(), json.RawMessage(`{"command":"rg","args":["sleep"]}`))
		if err != nil || !strings.HasPrefix(out, "stopped after 100ms") || time.Since(start) > 5*time.Second {
			t.Fatalf("out = %q, err = %v", out, err)
		}
	})
}

func TestRunToolRecordsCurlSources(t *testing.T) {
	for _, tt := range []struct {
		name    string
		proxied bool
		want    []string
	}{
		{"direct", false, []string{
			"https://api.github.com/repos/a/b/releases/tags/v1",
			"http://example.com/a%20b?q=%3Cx%3E",
		}},
		{"through the gateway, fetched over https", true, []string{
			"https://api.github.com/repos/a/b/releases/tags/v1",
			"https://example.com/a%20b?q=%3Cx%3E",
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rt, _ := newTestRunTool(t, tt.proxied)
			for _, input := range []string{
				`{"command":"curl","args":["-sSL","https://api.github.com/repos/a/b/releases/tags/v1","-H","Accept: application/json"]}`,
				`{"command":"curl","args":["http://user:secret@example.com/a b?q=<x>","https://api.github.com/repos/a/b/releases/tags/v1"]}`,
				`{"command":"curl","args":["file:///proc/self/environ","ftp://example.com/x","not a url"]}`,
				// Only curl's arguments are sources.
				`{"command":"rg","args":["https://other.example.com/"]}`,
				// A call that failed read nothing.
				`{"command":"curl","args":["--fail-test","https://failed.example.com/"]}`,
			} {
				if _, err := rt.Run(t.Context(), json.RawMessage(input)); err != nil {
					t.Fatal(err)
				}
			}
			if got := rt.Sources(); !slices.Equal(got, tt.want) {
				t.Fatalf("sources = %q, want %q", got, tt.want)
			}
		})
	}
	rt, _ := newTestRunTool(t, false)
	if got := rt.Sources(); got == nil || len(got) != 0 {
		t.Fatalf("sources of an unused tool = %#v", got)
	}
}

// TestRunToolRecordsRan: the commands run are listed once each in first-use
// order, and one the tool does not offer or refuses is not among them.
func TestRunToolRecordsRan(t *testing.T) {
	rt, _ := newTestRunTool(t, false)
	if got := rt.Ran(); got == nil || len(got) != 0 {
		t.Fatalf("ran of an unused tool = %#v", got)
	}
	for _, input := range []string{
		`{"command":"rg","args":["x"]}`,
		`{"command":"curl","args":["https://example.com/"]}`,
		`{"command":"rg","args":["--pre","cat","y"]}`,
		`{"command":"fd","args":["x"]}`,
	} {
		// The unoffered and the refused calls fail, which the loop reports to
		// the model; only what ran is recorded.
		_, _ = rt.Run(t.Context(), json.RawMessage(input))
	}
	if got := rt.Ran(); !slices.Equal(got, []string{"rg", "curl"}) {
		t.Fatalf("ran = %q, want [rg curl]", got)
	}
}

// TestRunToolGH: only gh is given its extra environment, what it reads is
// a source, and the tool steers GitHub lookups to it.
func TestRefusedArgs(t *testing.T) {
	tests := []struct {
		command string
		args    []string
		refused bool
	}{
		{"gh", []string{"api", "repos/a/b"}, false},
		{"gh", []string{"release", "view", "auth", "-R", "a/b"}, false},
		{"gh", []string{"auth", "token"}, true},
		{"gh", []string{"--help", "auth", "status", "--show-token"}, true},
		{"gh", []string{"--hostname", "github.com", "auth", "token"}, true},
		{"gh", []string{"-R", "a/b", "auth", "status"}, true},
		{"gh", []string{"--pin", "v1", "extension", "install", "o/r"}, true},
		{"gh", []string{"--repo=a/b", "api", "auth"}, false},
		{"gh", []string{"--hostname", "auth", "token"}, false},
		{"gh", []string{"alias", "set", "x", "!env"}, true},
		{"gh", []string{"extension", "exec", "x"}, true},
		{"gh", []string{"config", "list"}, true},
		{"rg", []string{"-C", "3", "pattern"}, false},
		{"rg", []string{"--pre-glob", "*.pdf", "pattern"}, false},
		{"rg", []string{"--", "--pre"}, false},
		{"rg", []string{"--pre", "sh", "pattern"}, true},
		{"rg", []string{"--pre=sh", "pattern"}, true},
		{"rg", []string{"--hostname-bin=sh", "pattern"}, true},
		{"fd", []string{"-e", "yaml", "values"}, false},
		{"fd", []string{"-exml"}, false},
		{"fd", []string{"-HI", "-t", "x", "name"}, false},
		{"fd", []string{"--", "-x"}, false},
		{"fd", []string{".", "-x", "sh", "-c", "id"}, true},
		{"fd", []string{"-HIX", "sh"}, true},
		{"fd", []string{"--exec", "sh"}, true},
		{"fd", []string{"--exec-batch=sh"}, true},
		{"jq", []string{"-x", "."}, false},
	}
	for _, tt := range tests {
		t.Run(tt.command+" "+strings.Join(tt.args, " "), func(t *testing.T) {
			if why := refusedArgs(tt.command, tt.args); (why != "") != tt.refused {
				t.Fatalf("refusedArgs = %q, want refused %v", why, tt.refused)
			}
		})
	}
}

func TestRunToolRefusesAndMasks(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	rt := NewRunTool(RunConfig{
		Dir: t.TempDir(), Env: []string{helperEnv + "=1", "GOCOVERDIR=" + t.TempDir()},
		CommandEnv: map[string][]string{"gh": {"GH_TOKEN=ghs_run"}},
		Commands:   map[string]string{"gh": self}, Timeout: 2 * time.Second, MaxOutputBytes: 4096,
		Mask: func(s string) string { return strings.ReplaceAll(s, "ghs_run", "***") },
	})
	if out, err := rt.Run(t.Context(), json.RawMessage(`{"command":"gh","args":["auth","token"]}`)); err == nil {
		t.Fatalf("gh auth token ran: %q", out)
	}
	out, err := rt.Run(t.Context(), json.RawMessage(`{"command":"gh","args":["api","user"]}`))
	if err != nil || strings.Contains(out, "ghs_run") || !strings.Contains(out, "env=GH_TOKEN=***") {
		t.Fatalf("out = %q, %v; want the token masked", out, err)
	}
}

func TestRunToolGH(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	rt := NewRunTool(RunConfig{
		Dir: t.TempDir(), Env: []string{helperEnv + "=1", "GOCOVERDIR=" + t.TempDir()},
		CommandEnv: map[string][]string{"gh": {"GH_TOKEN=ghs_run"}},
		Commands:   map[string]string{"gh": self, "rg": self}, Timeout: 2 * time.Second, MaxOutputBytes: 4096,
	})
	gh, err := rt.Run(t.Context(), json.RawMessage(`{"command":"gh","args":["api","repos/a/b/compare/v1...v2"]}`))
	if err != nil || !strings.Contains(gh, "env=GH_TOKEN=ghs_run") {
		t.Fatalf("gh out = %q, %v", gh, err)
	}
	if rg, _ := rt.Run(t.Context(), json.RawMessage(`{"command":"rg","args":["x"]}`)); strings.Contains(rg, "GH_TOKEN") {
		t.Fatalf("rg was given gh's environment: %q", rg)
	}
	if got := rt.Sources(); !slices.Equal(got, []string{"https://api.github.com/repos/a/b/compare/v1...v2"}) {
		t.Fatalf("sources = %q", got)
	}
	// gh's list of every command gives way to the call it likely meant;
	// another command's output is its own.
	hint, err := rt.Run(t.Context(), json.RawMessage(`{"command":"gh","args":["unknown","repos/a/b"]}`))
	if err != nil || hint != `exit code 1`+"\n"+`unknown command "repos/a/b" for gh: its first argument is one of its commands, such as api, `+
		"release, pr, issue or repo. A REST path is read with api before it: gh api repos/a/b." {
		t.Fatalf("gh without its command = %q, %v", hint, err)
	}
	if out, _ := rt.Run(t.Context(), json.RawMessage(`{"command":"rg","args":["unknown","repos/a/b"]}`)); !strings.Contains(out, "Available commands") {
		t.Fatalf("rg's output was rewritten: %q", out)
	}
	// A call that succeeded printed what it read, which may quote the error.
	if out, _ := rt.Run(t.Context(), json.RawMessage(`{"command":"gh","args":["quote","repos/a/b"]}`)); !strings.Contains(out, "Available commands") {
		t.Fatalf("a successful gh call's output was rewritten: %q", out)
	}
	// A version bump's diff is fetch_repo's, which the review's prompt
	// describes: the compare view counts from where the branches split.
	if d := rt.Def().Description; !strings.Contains(d, "Use gh, not curl, for anything on GitHub") ||
		!strings.Contains(d, "gh release view <tag>") || strings.Contains(d, "compare") {
		t.Fatalf("description = %q", d)
	}
}

func TestRunToolDef(t *testing.T) {
	proxied, _ := newTestRunTool(t, true)
	def := proxied.Def()
	if def.Name != "run" || !strings.Contains(def.Description, "curl, rg") || !strings.Contains(def.Description, "Give curl http:// URLs") {
		t.Fatalf("def = %+v", def)
	}
	var schema struct {
		Properties struct {
			Command struct {
				Enum []string `json:"enum"`
			} `json:"command"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(def.InputSchema, &schema); err != nil || !slices.Equal(schema.Properties.Command.Enum, []string{"curl", "rg"}) {
		t.Fatalf("schema = %s, err = %v", def.InputSchema, err)
	}
	direct, _ := newTestRunTool(t, false)
	if d := direct.Def().Description; !strings.Contains(d, "Give curl https:// URLs") {
		t.Fatalf("description = %q", d)
	}
	noCurl := NewRunTool(RunConfig{Commands: map[string]string{"fd": "/bin/fd"}, Timeout: time.Second, Note: "The checkout is partial."})
	if d := noCurl.Def().Description; strings.Contains(d, "curl") || strings.Contains(d, "rg searches") ||
		!strings.Contains(d, "fd finds files and directories by name") || !strings.HasSuffix(d, " The checkout is partial.") {
		t.Fatalf("description = %q", d)
	}
	if d := proxied.Def().Description; !strings.Contains(d, "rg searches file contents where the grep tool falls short") ||
		strings.Contains(d, "fd finds") || strings.Contains(d, "jq reads") || strings.Contains(d, "yq does") {
		t.Fatalf("description = %q", d)
	}
	structured := NewRunTool(RunConfig{Commands: map[string]string{"jq": "/bin/jq", "yq": "/bin/yq"}, Timeout: time.Second})
	if d := structured.Def().Description; !strings.Contains(d, "jq reads one path of a JSON file") ||
		!strings.Contains(d, "yq does the same for a YAML file") {
		t.Fatalf("description = %q", d)
	}
}

func TestCheckout(t *testing.T) {
	t.Run("writes what the commands may read", func(t *testing.T) {
		dir := t.TempDir()
		stats, err := NewTree(testTree(t), []string{"generated/**"}).Checkout(t.Context(), dir, MaxCheckoutBytes)
		if err != nil {
			t.Fatal(err)
		}
		// generated/gen.go is ignored and huge.txt is over the size cap.
		if stats.Files != 6 || stats.Skipped != 2 || stats.Truncated {
			t.Fatalf("stats = %+v", stats)
		}
		b, err := os.ReadFile(filepath.Join(dir, "main.go"))
		if err != nil || !strings.HasPrefix(string(b), "package main") {
			t.Fatalf("main.go = %q, %v", b, err)
		}
		for _, name := range []string{"generated/gen.go", "huge.txt"} {
			if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
				t.Fatalf("%s written: %v", name, err)
			}
		}
		info, err := os.Stat(filepath.Join(dir, "alpha.go"))
		if err != nil || info.Mode().Perm() != 0o644 {
			t.Fatalf("alpha.go: %v, %v", info, err)
		}
	})

	t.Run("symlinks are skipped", func(t *testing.T) {
		dir := t.TempDir()
		stats, err := NewTree(symlinkTree(t), nil).Checkout(t.Context(), dir, MaxCheckoutBytes)
		if err != nil || stats.Files != 1 || stats.Skipped != 1 {
			t.Fatalf("stats = %+v, err = %v", stats, err)
		}
		if _, err := os.Lstat(filepath.Join(dir, "passwd")); !os.IsNotExist(err) {
			t.Fatalf("symlink written: %v", err)
		}
	})

	t.Run("stops at the total cap", func(t *testing.T) {
		stats, err := NewTree(testTree(t), nil).Checkout(t.Context(), t.TempDir(), 100)
		if err != nil || !stats.Truncated || stats.Bytes > 100 {
			t.Fatalf("stats = %+v, err = %v", stats, err)
		}
	})
}

// symlinkTree is a commit holding a file and a symlink out of the tree.
func symlinkTree(t *testing.T) *object.Tree {
	t.Helper()
	fs := memfs.New()
	r, err := git.Init(memory.NewStorage(), git.WithWorkTree(fs))
	if err != nil {
		t.Fatal(err)
	}
	gittest.Unsigned(t, r)
	wt, err := r.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	f, err := fs.Create("main.go")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("package main\n")); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := fs.Symlink("/etc/passwd", "passwd"); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Add("."); err != nil {
		t.Fatal(err)
	}
	hash, err := wt.Commit("initial", &git.CommitOptions{Author: &object.Signature{Name: "t", Email: "t@t", When: time.Now()}})
	if err != nil {
		t.Fatal(err)
	}
	c, err := r.CommitObject(hash)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := c.Tree()
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

func TestStderrTail(t *testing.T) {
	type write struct {
		stderr bool
		text   string
	}
	for _, tt := range []struct {
		name   string
		writes []write
		want   string
	}{
		{"all of stderr fits", []write{{false, "out\n"}, {true, "err\n"}}, ""},
		{"stderr after a full buffer", []write{{false, "0123456789"}, {true, "failed\n"}}, "failed\n"},
		{"after a whole line", []write{{true, "line 1\n"}, {false, "0123"}, {true, "line 2\n"}}, "line 2\n"},
		{"a line cut at the buffer's end", []write{{false, "0123456"}, {true, "TOKEN123\nnext\n"}}, "next\n"},
		{"a cut line and nothing after", []write{{false, "0123456"}, {true, "TOKEN123"}}, ""},
		{"a line cut at the tail's start", []write{{false, "0123456789"}, {true, "TOKEN123456789\nab\n"}}, "ab\n"},
		{"whole lines past the tail's length", []write{{false, "0123456789"}, {true, "1\n2\n3\n4\n5\n6\n7\n"}}, "4\n5\n6\n7\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			out := &cappedBuffer{max: 8}
			errs := &stderrTail{out: out, max: 9, last: '\n'}
			for _, w := range tt.writes {
				if w.stderr {
					_, _ = errs.Write([]byte(w.text))
				} else {
					_, _ = out.Write([]byte(w.text))
				}
			}
			if got := errs.String(); got != tt.want {
				t.Fatalf("end of stderr = %q, want %q", got, tt.want)
			}
		})
	}
}
