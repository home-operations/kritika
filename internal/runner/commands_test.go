package runner

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/home-operations/kritika/internal/agent"
)

func TestCommandEnv(t *testing.T) {
	for _, name := range []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(name, "")
	}
	t.Setenv("PATH", "/usr/local/bin:/usr/bin")
	t.Setenv("KRITIKA_GIT_TOKEN", "secret")

	env, proxied := commandEnv("/tmp/home")
	if proxied || !slices.Equal(env, []string{"PATH=/usr/local/bin:/usr/bin", "HOME=/tmp/home"}) {
		t.Fatalf("without a proxy: env = %q, proxied = %v", env, proxied)
	}

	t.Setenv("HTTPS_PROXY", "http://gateway:8082")
	t.Setenv("HTTP_PROXY", "http://gateway:8082")
	t.Setenv("no_proxy", "localhost")
	env, proxied = commandEnv("/tmp/home")
	want := []string{
		"PATH=/usr/local/bin:/usr/bin", "HOME=/tmp/home",
		"HTTP_PROXY=http://gateway:8082", "http_proxy=http://gateway:8082",
		"HTTPS_PROXY=http://gateway:8082", "https_proxy=http://gateway:8082",
		"NO_PROXY=localhost", "no_proxy=localhost",
	}
	if !proxied || !slices.Equal(env, want) {
		t.Fatalf("with the gateway: env = %q, proxied = %v", env, proxied)
	}
}

// TestKeptOutputRead: a cut output the run tool keeps beside the checkout
// is read by the commands at the path its note names, and by read_file,
// which reads the directory whether or not fetch_repo is offered.
func TestKeptOutputRead(t *testing.T) {
	catBin, err := exec.LookPath("cat")
	if err != nil {
		t.Skip(err)
	}
	headBin, err := exec.LookPath("head")
	if err != nil {
		t.Skip(err)
	}
	scratch := t.TempDir()
	checkout, up := filepath.Join(scratch, "checkout"), filepath.Join(scratch, upstreamDir)
	for _, d := range []string{checkout, up} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	render := strings.Repeat("a line of a long render\n", 400)
	if err := os.WriteFile(filepath.Join(checkout, "render.yaml"), []byte(render), 0o644); err != nil {
		t.Fatal(err)
	}
	run := agent.NewRunTool(agent.RunConfig{
		Dir: checkout, Commands: map[string]string{"cat": catBin, "head": headBin}, Timeout: 10 * time.Second, MaxOutputBytes: 1024,
		Keep: &agent.Kept{Dir: up, Rel: upstreamRel, FileBytes: fetchDiffBytes, Budget: agent.NewWriteBudget(fetchWriteBytes)},
	})
	out, err := run.Run(t.Context(), fetchInput(t, map[string]any{"command": "cat", "args": []string{"render.yaml"}}))
	if err != nil || !strings.HasSuffix(out, "\n[the whole output is in ../upstream/run-1.out]") {
		t.Fatalf("cat render.yaml = %q, %v", out, err)
	}
	if b, err := os.ReadFile(filepath.Join(up, "run-1.out")); err != nil || string(b) != render {
		t.Fatalf("run-1.out = %d bytes, %v; want the render", len(b), err)
	}

	out, err = run.Run(t.Context(), fetchInput(t, map[string]any{"command": "head", "args": []string{"-c", "24", "../upstream/run-1.out"}}))
	if err != nil || out != "exit code 0\na line of a long render\n" {
		t.Fatalf("head of the kept output through run = %q, %v", out, err)
	}

	head := tree(t, map[string]string{"main.go": "package main\n"})
	read := offeredTools(Spec{Agent: &AgentLimits{}, Prompt: &Prompt{}}, head, nil, []agent.Tool{run})[0]
	if d := read.Def(); d.Name != "read_file" || !strings.HasSuffix(d.Description, "at the paths fetch_repo and the run tool's notes name.") {
		t.Fatalf("def = %+v", d)
	}
	out, err = read.Run(t.Context(), fetchInput(t, map[string]any{"path": "../upstream/run-1.out", "start_line": 400, "end_line": 400}))
	if err != nil || out != "400\ta line of a long render" {
		t.Fatalf("read_file of the kept output = %q, %v", out, err)
	}
}
