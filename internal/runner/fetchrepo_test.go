package runner

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-billy/v6/osfs"
	git "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/backend"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/go-git/go-git/v6/plumbing/transport"

	"github.com/home-operations/kritika/internal/gittest"
	"github.com/home-operations/kritika/internal/upstream"
)

// servedRepo serves, over HTTPS, a repository with two tags: v1, with
// README.md and pkg/a.go, and v2, where pkg/a.go gains a function. It
// returns the clone URL and a fetch_repo tool that trusts the server.
func servedRepo(t *testing.T) (string, *fetchRepoTool) {
	t.Helper()
	work := t.TempDir()
	r, err := git.PlainInit(work, false)
	if err != nil {
		t.Fatal(err)
	}
	gittest.Unsigned(t, r)
	wt, err := r.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	commit := func(tag string, files map[string]string) {
		t.Helper()
		for name, content := range files {
			p := filepath.Join(work, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := wt.Add(name); err != nil {
				t.Fatal(err)
			}
		}
		h, err := wt.Commit(tag, &git.CommitOptions{Author: &object.Signature{Name: "t", Email: "t@x", When: time.Now()}})
		if err != nil {
			t.Fatal(err)
		}
		if err := r.Storer.SetReference(plumbing.NewHashReference(plumbing.NewTagReferenceName(tag), h)); err != nil {
			t.Fatal(err)
		}
	}
	commit("v1", map[string]string{"README.md": "upstream\n", "pkg/a.go": "package pkg\n"})
	commit("v2", map[string]string{"pkg/a.go": "package pkg\n\nfunc A() {}\n"})
	root := t.TempDir()
	if _, err := git.PlainClone(filepath.Join(root, "repo.git"), &git.CloneOptions{URL: work, Mirror: true}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewTLSServer(backend.New(transport.NewFilesystemLoader(osfs.New(root), false)))
	t.Cleanup(srv.Close)
	return srv.URL + "/repo.git", &fetchRepoTool{dir: t.TempDir(), transport: srv.Client().Transport}
}

func fetchInput(t *testing.T, in map[string]any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestFetchRepoTool(t *testing.T) {
	url, tool := servedRepo(t)

	out, err := tool.Run(t.Context(), fetchInput(t, map[string]any{"url": url, "ref": "v2", "from": "v1", "paths": []string{"pkg"}}))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Fetched " + url + " at v2", "under ../upstream/1-repo@v2, from the checkout", "1 files",
		"The diff from v1", "changes 1 files", "It is in ../upstream/1-repo@v2.diff", "+func A() {}"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output lacks %q:\n%s", want, out)
		}
	}
	if b, err := os.ReadFile(filepath.Join(tool.dir, "1-repo@v2", "pkg", "a.go")); err != nil || string(b) != "package pkg\n\nfunc A() {}\n" {
		t.Fatalf("pkg/a.go = %q, %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(tool.dir, "1-repo@v2", "README.md")); !os.IsNotExist(err) {
		t.Fatalf("a file outside the paths was written: %v", err)
	}
	diff, err := os.ReadFile(filepath.Join(tool.dir, "1-repo@v2.diff"))
	if err != nil || !strings.Contains(string(diff), "+func A() {}") {
		t.Fatalf("diff file = %q, %v", diff, err)
	}
	if want := int64(len("package pkg\n\nfunc A() {}\n") + len(diff)); tool.written != want {
		t.Fatalf("written = %d, want %d", tool.written, want)
	}

	if out, err := tool.Run(t.Context(), fetchInput(t, map[string]any{"url": url, "ref": "v1"})); err != nil ||
		!strings.Contains(out, "under ../upstream/2-repo@v1") || !strings.Contains(out, "2 files") || strings.Contains(out, "diff") {
		t.Fatalf("second fetch = %q, %v", out, err)
	}
	if got := tool.Sources(); !slices.Equal(got, []string{strings.TrimSuffix(url, ".git")}) {
		t.Fatalf("sources = %q", got)
	}

	_, err = tool.Run(t.Context(), fetchInput(t, map[string]any{"url": url, "ref": "v9", "paths": []string{"pkg"}}))
	if err == nil || !strings.Contains(err.Error(), "agent: fetch_repo: upstream: no such tag or branch") {
		t.Fatalf("unknown ref: err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(tool.dir, "3-repo@v9")); !os.IsNotExist(err) {
		t.Fatalf("a failed fetch's directory was kept: %v", err)
	}

	tool.fetches = fetchMax
	if _, err := tool.Run(t.Context(), fetchInput(t, map[string]any{"url": url, "ref": "v1"})); err == nil ||
		!strings.Contains(err.Error(), "made its 8 fetches") {
		t.Fatalf("past the fetch count: err = %v", err)
	}
	tool.fetches, tool.written = 0, fetchWriteBytes
	if _, err := tool.Run(t.Context(), fetchInput(t, map[string]any{"url": url, "ref": "v1"})); err == nil ||
		!strings.Contains(err.Error(), "written its 256 MiB") {
		t.Fatalf("past the write budget: err = %v", err)
	}
}

// TestFetchRepoDiffPastBudget: a diff that would take the files written
// past the review's budget is returned but not kept in a file.
func TestFetchRepoDiffPastBudget(t *testing.T) {
	url, tool := servedRepo(t)
	tool.written = fetchWriteBytes - int64(len("package pkg\n\nfunc A() {}\n"))
	out, err := tool.Run(t.Context(), fetchInput(t, map[string]any{"url": url, "ref": "v2", "from": "v1", "paths": []string{"pkg"}}))
	if err != nil || !strings.Contains(out, "It is not kept in a file") || !strings.Contains(out, "+func A() {}") {
		t.Fatalf("fetch = %q, %v", out, err)
	}
	if _, err := os.Stat(filepath.Join(tool.dir, "1-repo@v2.diff")); !os.IsNotExist(err) || tool.written != fetchWriteBytes {
		t.Fatalf("the diff file was written past the budget: written = %d, %v", tool.written, err)
	}
}

func TestFetchRepoSources(t *testing.T) {
	tool := &fetchRepoTool{}
	for _, req := range []upstream.Request{
		{URL: "https://github.com/a/b.git", Ref: "v2"},
		{URL: "https://github.com/a/b", Ref: "v2", From: "v1"},
		{URL: "https://github.com/a/b/", Ref: "v2"},
		{URL: "https://gitlab.com/g/p.git", Ref: "v2"},
	} {
		tool.record(req)
	}
	want := []string{"https://github.com/a/b/tree/v2", "https://github.com/a/b/compare/v1...v2", "https://gitlab.com/g/p"}
	if got := tool.Sources(); !slices.Equal(got, want) {
		t.Fatalf("sources = %q, want %q", got, want)
	}
}

func TestDirName(t *testing.T) {
	for _, tt := range []struct {
		n         int
		url, ref  string
		want      string
		wantLimit bool
	}{
		{1, "https://github.com/cert-manager/cert-manager", "v1.21.2", "1-cert-manager@v1.21.2", false},
		{2, "https://gitlab.com/g/p.git", "release/1.x", "2-p@release-1.x", false},
		{3, "https://example.com/", "HEAD", "3-repo@HEAD", false},
		{4, "https://example.com/r", strings.Repeat("a", 200), "", true},
	} {
		got := dirName(tt.n, tt.url, tt.ref)
		if tt.wantLimit {
			if len(got) != dirNameMax || !strings.HasPrefix(got, "4-r@aaa") {
				t.Errorf("dirName(%q) = %q, want it cut to %d", tt.ref, got, dirNameMax)
			}
			continue
		}
		if got != tt.want {
			t.Errorf("dirName(%d, %q, %q) = %q, want %q", tt.n, tt.url, tt.ref, got, tt.want)
		}
	}
}
