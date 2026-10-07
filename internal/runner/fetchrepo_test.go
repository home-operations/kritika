package runner

import (
	"encoding/json"
	"fmt"
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

	"github.com/home-operations/kritika/internal/agent"
	"github.com/home-operations/kritika/internal/gittest"
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

// TestFetchRepoMissingPaths: a fetch names the paths it found nothing
// under, and where in the tree their last segments are.
func TestFetchRepoMissingPaths(t *testing.T) {
	url, tool := servedRepo(t)
	out, err := tool.Run(t.Context(), fetchInput(t, map[string]any{"url": url, "ref": "v1", "paths": []string{"a.go", "README.md", "nope"}}))
	if err != nil || !strings.HasSuffix(out, "1 files, 1 KiB. Nothing is under a.go; these paths end in it: pkg/a.go. Nothing is under nope.") {
		t.Fatalf("a fetch of paths the tree lacks = %q, %v", out, err)
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
	for _, read := range [][2]string{
		{"https://github.com/a/b.git", "tree/v2"},
		{"https://github.com/a/b", "compare/v1...v2"},
		{"https://github.com/a/b/", "tree/v2"},
		{"https://github.com/a/b", "tags"},
		{"https://gitlab.com/g/p.git", "tree/v2"},
	} {
		tool.record(read[0], read[1])
	}
	want := []string{"https://github.com/a/b/tree/v2", "https://github.com/a/b/compare/v1...v2", "https://github.com/a/b/tags", "https://gitlab.com/g/p"}
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

func TestFetchRepoTags(t *testing.T) {
	url, tool := servedRepo(t)
	run := func(in map[string]any) (string, error) {
		t.Helper()
		in["url"] = url
		return tool.Run(t.Context(), fetchInput(t, in))
	}

	if out, err := run(map[string]any{"tags": ""}); err != nil || out != "The 2 tags of "+url+", newest first:\nv2\nv1" {
		t.Fatalf("every tag = %q, %v", out, err)
	}
	if out, err := run(map[string]any{"tags": "V2"}); err != nil || out != "1 of the 2 tags of "+url+` contain "V2", newest first:`+"\nv2" {
		t.Fatalf("tags with V2 = %q, %v", out, err)
	}
	if out, err := run(map[string]any{"tags": "9.9"}); err != nil || !strings.HasPrefix(out, "None of the 2 tags of "+url+` contain "9.9". The newest: v2, v1.`) {
		t.Fatalf("no tag with 9.9 = %q, %v", out, err)
	}
	if _, err := run(map[string]any{"tags": "v", "ref": "v2"}); err == nil || !strings.Contains(err.Error(), "not both") {
		t.Fatalf("tags with a ref: err = %v", err)
	}
	if _, err := run(map[string]any{"tags": "", "from": "v1"}); err == nil || !strings.Contains(err.Error(), "give ref to fetch") {
		t.Fatalf("tags with from and no ref: err = %v", err)
	}
	if tool.fetches != 0 || tool.written != 0 {
		t.Fatalf("a listing counted as a fetch: fetches = %d, written = %d", tool.fetches, tool.written)
	}

	_, err := run(map[string]any{"ref": "release-2", "paths": []string{"pkg"}})
	if err == nil || !strings.Contains(err.Error(), "no such tag or branch") || !strings.HasSuffix(err.Error(), ". Tags with 2: v2.") {
		t.Fatalf("a missing ref with a version: err = %v", err)
	}
	if _, err := run(map[string]any{"ref": "v1", "from": "nightly"}); err == nil || !strings.HasSuffix(err.Error(), ". Its newest tags: v2, v1.") {
		t.Fatalf("a missing ref without one: err = %v", err)
	}

	if tool.listings != 5 {
		t.Fatalf("listings = %d, want 5: three asked for and two after a missing ref", tool.listings)
	}

	tool.listings = listMax
	if _, err := run(map[string]any{"tags": ""}); err == nil || !strings.Contains(err.Error(), "listed tags 8 times") {
		t.Fatalf("past the listing count: err = %v", err)
	}
	if _, err := run(map[string]any{"ref": "release-2"}); err == nil || !strings.HasSuffix(err.Error(), "a commit's full SHA.") {
		t.Fatalf("a missing ref past the listing count names no tags: err = %v", err)
	}
}

func TestTagList(t *testing.T) {
	tags := make([]string, 300)
	for i := range tags {
		tags[i] = fmt.Sprintf("v1.%d.0", 299-i)
	}
	out := tagList("https://x/r", "", tags)
	if !strings.HasPrefix(out, "The 300 tags of https://x/r, newest first; the first 200, so narrow tags for the rest:\nv1.299.0\n") ||
		strings.Count(out, "\n") != 200 || !strings.HasSuffix(out, "\nv1.100.0") {
		t.Fatalf("300 tags = %.120q…", out)
	}
	if got := tagList("https://x/r", "", nil); got != "https://x/r has no tags." {
		t.Fatalf("no tags = %q", got)
	}
}

func TestVersionOf(t *testing.T) {
	for ref, want := range map[string]string{
		"v0.37.0": "0.37.0", "descheduler-0.37.0": "0.37.0", "gpu-v0.12.1": "0.12.1", "0.0.6": "0.0.6",
		"main": "", "HEAD": "", strings.Repeat("a1", 20): "",
	} {
		if got := versionOf(ref); got != want {
			t.Errorf("versionOf(%q) = %q, want %q", ref, got, want)
		}
	}
}

func TestFetchedReadFile(t *testing.T) {
	url, fetch := servedRepo(t)
	if _, err := fetch.Run(t.Context(), fetchInput(t, map[string]any{"url": url, "ref": "v2", "paths": []string{"pkg"}})); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(fetch.dir, "link")); err != nil {
		t.Fatal(err)
	}
	// A fetch keeps a diff larger than any file it writes.
	big := strings.Repeat("+a line of a long diff\n", (fetchDiffBytes-1)/len("+a line of a long diff\n"))
	if err := os.WriteFile(filepath.Join(fetch.dir, "9-repo@v9.diff"), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fetch.dir, "huge.diff"), []byte(big+big), 0o644); err != nil {
		t.Fatal(err)
	}
	head := tree(t, map[string]string{"main.go": "package main\n"})

	plain := offeredTools(Spec{Agent: &AgentLimits{}, Prompt: &Prompt{}}, head, nil, nil)[0]
	if _, err := plain.Run(t.Context(), fetchInput(t, map[string]any{"path": "../upstream/1-repo@v2/pkg/a.go"})); err == nil ||
		!strings.Contains(err.Error(), "escapes the tree") || strings.Contains(plain.Def().Description, "fetch_repo") {
		t.Fatalf("without fetch_repo, read_file reads the head alone: %v", err)
	}
	read := offeredTools(Spec{Agent: &AgentLimits{}, Prompt: &Prompt{}}, head, nil, []agent.Tool{fetch})[0]
	if d := read.Def(); d.Name != "read_file" || !strings.HasSuffix(d.Description, "It also reads the files fetch_repo wrote, at the paths it names under ../upstream/.") {
		t.Fatalf("def = %+v", d)
	}
	for _, tt := range []struct {
		name      string
		in        map[string]any
		want, err string
	}{
		{"a fetched file", map[string]any{"path": "../upstream/1-repo@v2/pkg/a.go"}, "1\tpackage pkg\n2\t\n3\tfunc A() {}", ""},
		{"lines of it", map[string]any{"path": "../upstream/1-repo@v2/pkg/a.go", "start_line": 3, "end_line": 3}, "3\tfunc A() {}", ""},
		{"a file of the head", map[string]any{"path": "main.go"}, "1\tpackage main", ""},
		{"a file the fetch left out", map[string]any{"path": "../upstream/1-repo@v2/README.md"}, "", "no such file"},
		{"a directory", map[string]any{"path": "../upstream/1-repo@v2/pkg"}, "", "a directory"},
		{"a path out of the fetch directory", map[string]any{"path": "../upstream/../secret"}, "", "escapes the tree"},
		{"a link out of the fetch directory", map[string]any{"path": "../upstream/link"}, "", "agent: read_file: ../upstream/link"},
		{"lines of a diff over a file's limit", map[string]any{"path": "../upstream/9-repo@v9.diff", "start_line": 90000, "end_line": 90000},
			"90000\t+a line of a long diff", ""},
		{"a file over a diff's limit", map[string]any{"path": "../upstream/huge.diff", "start_line": 1, "end_line": 1}, "", "over the 4194304 byte limit"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := read.Run(t.Context(), fetchInput(t, tt.in))
			if tt.err != "" {
				if err == nil || !strings.Contains(err.Error(), tt.err) || strings.Contains(got, "secret") {
					t.Fatalf("read = %q, %v; want an error holding %q", got, err, tt.err)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("read = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}
