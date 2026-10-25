package runner

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
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

// TestClearDir: a checkout directory is emptied in place, under a scratch
// directory that no longer lets it be removed.
func TestClearDir(t *testing.T) {
	scratch := t.TempDir()
	dir := filepath.Join(scratch, "checkout")
	if err := os.MkdirAll(filepath.Join(dir, "pkg", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"main.go", filepath.Join("pkg", "sub", "x.go")} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("package x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(scratch, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(scratch, 0o755) })
	if err := clearDir(dir); err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatalf("checkout = %v, %v; want an empty directory", entries, err)
	}
}

// TestCheckoutFailure: a failed checkout names itself to the model and the
// summary, and a path error among its causes by its operation and cause
// alone, never the path, which the run tool would otherwise do by dropping
// the checkout from the message.
func TestCheckoutFailure(t *testing.T) {
	t.Parallel()
	full := &fs.PathError{Op: "write", Path: "/tmp/kritika-run-1/checkout/main.go", Err: syscall.ENOSPC}
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "path error", err: fmt.Errorf("agent: checkout: main.go: %w", full), want: "the checkout could not be written: write: no space left on device"},
		{name: "other", err: errors.New("agent: checkout: context canceled"), want: "the checkout could not be written: agent: checkout: context canceled"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := checkoutFailure(tt.err)
			if err.Error() != tt.want {
				t.Fatalf("checkoutFailure = %q, want %q", err, tt.want)
			}
			if _, ok := errors.AsType[*fs.PathError](err); ok {
				t.Fatal("the path error is still in the chain, for the run tool to show instead")
			}
			if !errors.Is(err, syscall.ENOSPC) && tt.name == "path error" {
				t.Fatal("the cause was lost")
			}
		})
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

// TestDefaultCommandsMatchImage: the commands the run tool looks for on
// PATH are those the -tools image carries: the packages the Dockerfile's dnf
// installs into its root, each by its command, findutils' being find, less
// the library and data packages, and gh from its release.
func TestDefaultCommandsMatchImage(t *testing.T) {
	dockerfile, err := os.ReadFile("../../Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	_, install, ok := strings.Cut(string(dockerfile), "--installroot /rootfs")
	if !ok {
		t.Fatal("the Dockerfile installs no packages into the tools root")
	}
	install, _, _ = strings.Cut(install, "&&")
	var commands []string
	for field := range strings.FieldsSeq(install) {
		switch field {
		case `\`, "libstdc++", "tzdata":
		case "findutils":
			commands = append(commands, "find")
		default:
			if !strings.HasPrefix(field, "-") {
				commands = append(commands, field)
			}
		}
	}
	if strings.Contains(string(dockerfile), "depName=cli/cli") {
		commands = append(commands, "gh")
	}
	slices.Sort(commands)
	if !slices.Equal(commands, defaultCommands) {
		t.Fatalf("image tools %v, defaultCommands %v", commands, defaultCommands)
	}
}
