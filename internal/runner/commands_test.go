package runner

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/home-operations/kritika/internal/agent"
	"github.com/pelletier/go-toml/v2"
)

func TestCommandEnv(t *testing.T) {
	for _, name := range []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(name, "")
	}
	t.Setenv("PATH", "/usr/local/bin:/usr/bin")
	t.Setenv("KRITIKA_GIT_TOKEN", "secret")

	env, proxied := commandEnv("/tmp/home", "/tmp/cache")
	if proxied || !slices.Equal(env, []string{"PATH=/usr/local/bin:/usr/bin", "HOME=/tmp/home", "XDG_CACHE_HOME=/tmp/cache"}) {
		t.Fatalf("without a proxy: env = %q, proxied = %v", env, proxied)
	}

	t.Setenv("HTTPS_PROXY", "http://gateway:8082")
	t.Setenv("HTTP_PROXY", "http://gateway:8082")
	t.Setenv("no_proxy", "localhost,gateway")
	env, proxied = commandEnv("/tmp/home", "/tmp/cache")
	// No NO_PROXY: curl reaches even those hosts through the gateway.
	want := []string{
		"PATH=/usr/local/bin:/usr/bin", "HOME=/tmp/home", "XDG_CACHE_HOME=/tmp/cache",
		"HTTP_PROXY=http://gateway:8082", "http_proxy=http://gateway:8082",
		"HTTPS_PROXY=http://gateway:8082", "https_proxy=http://gateway:8082",
	}
	if !proxied || !slices.Equal(env, want) {
		t.Fatalf("with the gateway: env = %q, proxied = %v", env, proxied)
	}
}

// TestCurlrcSchemes: the host's curl, given the runner's .curlrc, fetches
// an http:// URL and refuses one of any other scheme, given or redirected
// to, before connecting: curl's exit code 1, an unsupported protocol,
// where a connection to the closed port would fail with 7.
func TestCurlrcSchemes(t *testing.T) {
	bin, err := exec.LookPath("curl")
	if err != nil {
		t.Skip(err)
	}
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".curlrc"), []byte(curlrc), 0o644); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "ftp://"+closed+"/", http.StatusFound)
		}
	}))
	defer srv.Close()
	for _, tt := range []struct {
		url  string
		code int
	}{
		{srv.URL + "/", 0},
		{"telnet://" + closed, 1},
		{"ftp://" + closed + "/", 1},
		{"dict://" + closed + "/d:x", 1},
		{"tftp://" + closed + "/x", 1},
		{srv.URL + "/redirect", 1},
	} {
		cmd := exec.CommandContext(t.Context(), bin, "-sS", "--max-time", "5", tt.url)
		cmd.Env = []string{"HOME=" + home}
		out, err := cmd.CombinedOutput()
		code := 0
		if ee, ok := errors.AsType[*exec.ExitError](err); ok {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		if code != tt.code {
			t.Errorf("curl %s: exit code %d, want %d\n%s", tt.url, code, tt.code, out)
		}
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

// TestDefaultCommandsMatchImage: the commands a runner image without mise
// offers from its PATH are those the -tools image declares.
func TestDefaultCommandsMatchImage(t *testing.T) {
	doc, err := os.ReadFile("../../build/tools/mise.toml")
	if err != nil {
		t.Fatal(err)
	}
	var image struct {
		Tools map[string]any `toml:"tools"`
	}
	if err := toml.Unmarshal(doc, &image); err != nil {
		t.Fatal(err)
	}
	var commands []string
	for name, tool := range image.Tools {
		options, _ := tool.(map[string]any)
		if bins := binsOf(options["filter_bins"]); bins != nil {
			commands = append(commands, bins...)
		} else {
			commands = append(commands, name)
		}
	}
	slices.Sort(commands)
	if !slices.Equal(commands, defaultCommands) {
		t.Fatalf("image tools %v, defaultCommands %v", commands, defaultCommands)
	}
}
