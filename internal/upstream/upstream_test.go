package upstream

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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
)

// source is the repository the tests fetch, as a bare repository under
// root named repo.git. At v1, an annotated tag that the annotated tag
// v1-again names, it has a README, a symlink to it, a file over the
// tests' blob limit and two packages; at v2, a lightweight tag that main
// also points at, pkg/a/a.go gains a function, pkg/a/old.go is renamed to
// pkg/a/new.go unchanged, and pkg/b/b.go and the large file change.
type source struct {
	root   string
	v1, v2 string
}

func newSource(t *testing.T) source {
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
	sig := &object.Signature{Name: "t", Email: "t@x", When: time.Now()}
	write := func(files map[string]string) {
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
	}
	commit := func(msg string) plumbing.Hash {
		t.Helper()
		h, err := wt.Commit(msg, &git.CommitOptions{Author: sig})
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	old := "package a\n\n// Old moves to new.go unchanged at v2.\nfunc Old() {}\n"
	write(map[string]string{
		"README.md":    "upstream\n",
		"big.bin":      strings.Repeat("x", 2048),
		"pkg/a/a.go":   "package a\n\nfunc A() {}\n",
		"pkg/a/old.go": old,
		"pkg/b/b.go":   "package b\n",
	})
	if err := os.Symlink("README.md", filepath.Join(work, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Add("link"); err != nil {
		t.Fatal(err)
	}
	v1 := commit("v1")
	tag, err := r.CreateTag("v1", v1, &git.CreateTagOptions{Tagger: sig, Message: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.CreateTag("v1-again", tag.Hash(), &git.CreateTagOptions{Tagger: sig, Message: "v1 again"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(work, "pkg/a/old.go")); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Remove("pkg/a/old.go"); err != nil {
		t.Fatal(err)
	}
	write(map[string]string{
		"big.bin":      strings.Repeat("y", 2048),
		"pkg/a/a.go":   "package a\n\nfunc A() {}\n\nfunc A2() {}\n",
		"pkg/a/new.go": old,
		"pkg/b/b.go":   "package b\n\nfunc B() {}\n",
	})
	v2 := commit("v2")
	for _, ref := range []plumbing.ReferenceName{"refs/tags/v2", "refs/heads/main"} {
		if err := r.Storer.SetReference(plumbing.NewHashReference(ref, v2)); err != nil {
			t.Fatal(err)
		}
	}
	root := t.TempDir()
	if _, err := git.PlainClone(filepath.Join(root, "repo.git"), &git.CloneOptions{URL: work, Mirror: true}); err != nil {
		t.Fatal(err)
	}
	return source{root: root, v1: v1.String(), v2: v2.String()}
}

// server is a source served over HTTPS, and the transport that trusts it.
type server struct {
	name      string
	url       string
	transport http.RoundTripper
	// filters says the server can send a tree without its blobs.
	filters bool
}

// servers serves src with go-git's own server, which cannot filter, and,
// where git is installed, with git http-backend, which can and which, as
// GitHub does, sends none of the blobs under a commit the client says it
// has.
func servers(t *testing.T, src source) []server {
	t.Helper()
	gogit := httptest.NewTLSServer(backend.New(transport.NewFilesystemLoader(osfs.New(src.root), false)))
	t.Cleanup(gogit.Close)
	goGit := server{name: "go-git", url: gogit.URL + "/repo.git", transport: gogit.Client().Transport}
	bin, err := exec.LookPath("git")
	if err != nil {
		t.Log("git http-backend not served: no git on PATH")
		return []server{goGit}
	}
	gitSrv := httptest.NewTLSServer(&cgi.Handler{
		Path: bin, Args: []string{"http-backend"}, InheritEnv: []string{"PATH"},
		Env: []string{
			"GIT_PROJECT_ROOT=" + src.root, "GIT_HTTP_EXPORT_ALL=1", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull,
			"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=uploadpack.allowFilter", "GIT_CONFIG_VALUE_0=true",
		},
	})
	t.Cleanup(gitSrv.Close)
	return []server{goGit, {name: "git", url: gitSrv.URL + "/repo.git", transport: gitSrv.Client().Transport, filters: true}}
}

var testLimits = Limits{WireBytes: 1 << 20, WriteBytes: 1 << 20, BlobBytes: 1 << 10, DiffBytes: 1 << 20}

// written lists the files under dir, slash-separated, with their contents.
func written(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		out[filepath.ToSlash(rel)] = string(b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func names(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func TestFetch(t *testing.T) {
	src := newSource(t)
	for _, srv := range servers(t, src) {
		t.Run(srv.name, func(t *testing.T) {
			f := &Fetcher{Transport: srv.transport, Limits: testLimits}
			fetch := func(t *testing.T, req Request) (*Result, map[string]string) {
				t.Helper()
				dest := filepath.Join(t.TempDir(), "out")
				req.URL = srv.url
				res, err := f.Fetch(t.Context(), req, dest)
				if err != nil {
					t.Fatal(err)
				}
				return res, written(t, dest)
			}

			t.Run("the whole tree at an annotated tag", func(t *testing.T) {
				res, files := fetch(t, Request{Ref: "v1"})
				if res.Commit != src.v1 || res.Filtered || res.Skipped != 2 || res.Truncated || res.Files != 4 ||
					res.Diff != "" || res.Changed != nil {
					t.Fatalf("result = %+v", res)
				}
				if got := names(files); !slices.Equal(got, []string{"README.md", "pkg/a/a.go", "pkg/a/old.go", "pkg/b/b.go"}) {
					t.Fatalf("written = %q; the symlink and the file over the blob limit are left out", got)
				}
				if files["README.md"] != "upstream\n" || res.Bytes != int64(len("upstream\n")+len(files["pkg/a/a.go"])+
					len(files["pkg/a/old.go"])+len(files["pkg/b/b.go"])) {
					t.Fatalf("files = %q, bytes = %d", files, res.Bytes)
				}
			})

			t.Run("paths at a branch, filtered where the server can", func(t *testing.T) {
				res, files := fetch(t, Request{Ref: "main", Paths: []string{"pkg/a/"}})
				if res.Commit != src.v2 || res.Filtered != srv.filters || res.Skipped != 0 {
					t.Fatalf("result = %+v", res)
				}
				if got := names(files); !slices.Equal(got, []string{"pkg/a/a.go", "pkg/a/new.go"}) {
					t.Fatalf("written = %q", got)
				}
			})

			t.Run("a diff kept to the paths, with the rename found", func(t *testing.T) {
				res, files := fetch(t, Request{Ref: "v2", From: "v1", Paths: []string{"pkg/a"}})
				if res.Commit != src.v2 || res.FromCommit != src.v1 || res.DiffCut != 0 ||
					!slices.Equal(res.Changed, []string{"pkg/a/a.go", "pkg/a/new.go"}) || len(files) != 2 {
					t.Fatalf("result = %+v, written = %q", res, names(files))
				}
				for _, want := range []string{"+func A2() {}", "rename from pkg/a/old.go", "rename to pkg/a/new.go"} {
					if !strings.Contains(res.Diff, want) {
						t.Fatalf("diff lacks %q:\n%s", want, res.Diff)
					}
				}
				if strings.Contains(res.Diff, "pkg/b") {
					t.Fatalf("diff reaches past the paths:\n%s", res.Diff)
				}
			})

			t.Run("a full SHA and HEAD", func(t *testing.T) {
				if res, _ := fetch(t, Request{Ref: src.v1, Paths: []string{"README.md"}}); res.Commit != src.v1 || res.Files != 1 {
					t.Fatalf("by SHA: %+v", res)
				}
				if res, _ := fetch(t, Request{Ref: "HEAD", Paths: []string{"README.md"}}); res.Commit != src.v2 || res.Files != 1 {
					t.Fatalf("HEAD: %+v", res)
				}
			})
		})
	}
}

// TestFetchMissing: a fetch names the paths the tree has nothing under,
// each with the tree's paths that end in it.
func TestFetchMissing(t *testing.T) {
	src := newSource(t)
	for _, srv := range servers(t, src) {
		t.Run(srv.name, func(t *testing.T) {
			dest := filepath.Join(t.TempDir(), "out")
			req := Request{URL: srv.url, Ref: "main", Paths: []string{"a", "pkg/b", "b.go", "nope"}}
			res, err := (&Fetcher{Transport: srv.transport, Limits: testLimits}).Fetch(t.Context(), req, dest)
			if err != nil {
				t.Fatal(err)
			}
			want := []MissingPath{{Path: "a", Near: []string{"pkg/a"}}, {Path: "b.go", Near: []string{"pkg/b/b.go"}}, {Path: "nope"}}
			if files := names(written(t, dest)); !reflect.DeepEqual(res.Missing, want) || !slices.Equal(files, []string{"pkg/b/b.go"}) {
				t.Fatalf("missing = %+v, written = %q; want %+v", res.Missing, files, want)
			}
		})
	}
}

func TestFetchLimits(t *testing.T) {
	src := newSource(t)
	srv := servers(t, src)[0]
	fetch := func(l Limits, req Request) (*Result, error) {
		req.URL = srv.url
		return (&Fetcher{Transport: srv.transport, Limits: l}).Fetch(t.Context(), req, filepath.Join(t.TempDir(), "out"))
	}

	if _, err := fetch(Limits{WireBytes: 100, WriteBytes: 1 << 20, BlobBytes: 1 << 10, DiffBytes: 1 << 20}, Request{Ref: "v1"}); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("over the wire cap: err = %v, want ErrTooLarge", err)
	}
	// README.md sorts first and fits; the next file does not.
	res, err := fetch(Limits{WireBytes: 1 << 20, WriteBytes: 12, BlobBytes: 1 << 10, DiffBytes: 1 << 20}, Request{Ref: "v1"})
	if err != nil || !res.Truncated || res.Files != 1 || res.Bytes != int64(len("upstream\n")) {
		t.Fatalf("over the write cap: %+v, %v", res, err)
	}
	res, err = fetch(Limits{WireBytes: 1 << 20, WriteBytes: 1 << 20, BlobBytes: 1 << 10, DiffBytes: 200}, Request{Ref: "v2", From: "v1"})
	if err != nil || res.DiffCut < 2 || len(res.Diff) > 200 || len(res.Changed) != 4 {
		t.Fatalf("over the diff cap: %+v, %v", res, err)
	}
	res, err = fetch(testLimits, Request{Ref: "v2", From: "v1"})
	if err != nil || res.DiffCut != 1 || !slices.Equal(res.Changed, []string{"big.bin", "pkg/a/a.go", "pkg/a/new.go", "pkg/b/b.go"}) ||
		strings.Contains(res.Diff, "big.bin") || !strings.Contains(res.Diff, "rename to pkg/a/new.go") {
		t.Fatalf("a changed file over the blob limit is listed but not diffed: %+v, %v", res, err)
	}
	if _, err := fetch(testLimits, Request{Ref: "v9"}); !errors.Is(err, ErrNoRef) || !strings.Contains(err.Error(), "no such tag or branch") ||
		!strings.Contains(err.Error(), "full SHA") {
		t.Fatalf("an unknown ref: err = %v", err)
	}
	dest := filepath.Join(t.TempDir(), "taken")
	if err := os.Mkdir(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := (&Fetcher{Transport: srv.transport, Limits: testLimits}).Fetch(t.Context(), Request{URL: srv.url, Ref: "v1"}, dest); !errors.Is(err, os.ErrExist) {
		t.Fatalf("into a directory that exists: err = %v", err)
	}
}

func TestFetchTagOfTag(t *testing.T) {
	src := newSource(t)
	for _, srv := range servers(t, src) {
		t.Run(srv.name, func(t *testing.T) {
			res, err := (&Fetcher{Transport: srv.transport, Limits: testLimits}).Fetch(t.Context(),
				Request{URL: srv.url, Ref: "v1-again", From: "v1-again", Paths: []string{"README.md"}}, filepath.Join(t.TempDir(), "out"))
			if err != nil || res.Commit != src.v1 || res.FromCommit != src.v1 || res.Files != 1 {
				t.Fatalf("fetch = %+v, %v", res, err)
			}
		})
	}
}

func TestRequestCheck(t *testing.T) {
	const url = "https://github.com/a/b"
	for _, tt := range []struct {
		name string
		req  Request
		want []string
		err  string
	}{
		{"a tag", Request{URL: url, Ref: "v1.2.3"}, []string{}, ""},
		{"a branch with a slash and a from", Request{URL: url + ".git", Ref: "release/1.x", From: "v1.0.0"}, []string{}, ""},
		{"HEAD and a full SHA", Request{URL: url, Ref: "HEAD", From: strings.Repeat("a", 40)}, []string{}, ""},
		{"paths cleaned", Request{URL: url, Ref: "main", Paths: []string{"pkg/a/", "./docs"}}, []string{"pkg/a", "docs"}, ""},
		{"http", Request{URL: "http://github.com/a/b", Ref: "main"}, nil, "not an https:// clone URL"},
		{"a local path", Request{URL: "/srv/repo.git", Ref: "main"}, nil, "not an https:// clone URL"},
		{"credentials", Request{URL: "https://x:y@github.com/a/b", Ref: "main"}, nil, "not an https:// clone URL"},
		{"no repository", Request{URL: "https://github.com/", Ref: "main"}, nil, "not an https:// clone URL"},
		{"a refspec", Request{URL: url, Ref: "main:refs/heads/x"}, nil, "ref: \"main:refs/heads/x\" is not"},
		{"a forced ref", Request{URL: url, Ref: "+main"}, nil, "ref: "},
		{"a flag", Request{URL: url, Ref: "-v"}, nil, "ref: "},
		{"no ref", Request{URL: url}, nil, "ref: "},
		{"a bad from", Request{URL: url, Ref: "main", From: "a..b"}, nil, "from: "},
		{"an absolute path", Request{URL: url, Ref: "main", Paths: []string{"/etc"}}, nil, "paths: "},
		{"an escaping path", Request{URL: url, Ref: "main", Paths: []string{"a/../../x"}}, nil, "paths: "},
		{"the root", Request{URL: url, Ref: "main", Paths: []string{"."}}, nil, "paths: "},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.req.check()
			if tt.err == "" {
				if err != nil || !slices.Equal(got, tt.want) {
					t.Fatalf("check = %q, %v; want %q", got, err, tt.want)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.err) {
				t.Fatalf("err = %v, want it to contain %q", err, tt.err)
			}
		})
	}
}

// recorder keeps the body of every request it carries.
type recorder struct {
	base   http.RoundTripper
	bodies []string
}

func (r *recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		b, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		_ = req.Body.Close()
		r.bodies = append(r.bodies, string(b))
		req.Body = io.NopCloser(bytes.NewReader(b))
	}
	return r.base.RoundTrip(req)
}

// TestBackfillSendsNoHaves checks what keeps GitHub sending the blobs a
// filtered fetch left out: a server told the client has a commit sends
// none of the blobs under it, so the fetch of the blobs claims to have
// nothing. git itself sends them either way, so the requests are what is
// checked.
func TestBackfillSendsNoHaves(t *testing.T) {
	srvs := servers(t, newSource(t))
	if len(srvs) < 2 {
		t.Skip("no git on PATH to serve a filtered fetch")
	}
	srv := srvs[1]
	rec := &recorder{base: srv.transport}
	res, err := (&Fetcher{Transport: rec, Limits: testLimits}).Fetch(t.Context(),
		Request{URL: srv.url, Ref: "v2", From: "v1", Paths: []string{"pkg/a"}}, filepath.Join(t.TempDir(), "out"))
	if err != nil || !res.Filtered || res.Files != 2 {
		t.Fatalf("fetch = %+v, %v", res, err)
	}
	var wants int
	for _, b := range rec.bodies {
		if strings.Contains(b, "have ") {
			t.Fatalf("a request claims to have objects:\n%q", b)
		}
		wants += strings.Count(b, "want ")
	}
	// The two refs, then three blobs: a.go at each tag, and old.go, which
	// is new.go unchanged.
	if wants != 5 {
		t.Fatalf("wanted %d objects across %q", wants, rec.bodies)
	}
}

func TestTags(t *testing.T) {
	src := newSource(t)
	mirror, err := git.PlainOpen(filepath.Join(src.root, "repo.git"))
	if err != nil {
		t.Fatal(err)
	}
	// GitHub lists a ref for every pull request beside the tags.
	if err := mirror.Storer.SetReference(plumbing.NewHashReference("refs/pull/1/head", plumbing.NewHash(src.v2))); err != nil {
		t.Fatal(err)
	}
	for _, srv := range servers(t, src) {
		t.Run(srv.name, func(t *testing.T) {
			rec := &recorder{base: srv.transport}
			tags, err := (&Fetcher{Transport: rec, Limits: testLimits}).Tags(t.Context(), srv.url)
			if err != nil || !slices.Equal(tags, []string{"v2", "v1-again", "v1"}) {
				t.Fatalf("tags = %q, %v; want the three tags, newest first, without peeled entries or other refs", tags, err)
			}
			if srv.filters && !slices.ContainsFunc(rec.bodies, func(b string) bool { return strings.Contains(b, "ref-prefix refs/tags/") }) {
				t.Fatalf("no request asked for refs/tags/ only: %q", rec.bodies)
			}
		})
	}

	srv := servers(t, src)[0]
	if _, err := (&Fetcher{Transport: srv.transport, Limits: Limits{WireBytes: 10}}).Tags(t.Context(), srv.url); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("over the wire cap: err = %v, want ErrTooLarge", err)
	}
	if _, err := (&Fetcher{Limits: testLimits}).Tags(t.Context(), "http://github.com/a/b"); err == nil ||
		!strings.Contains(err.Error(), "not an https:// clone URL") {
		t.Fatalf("an http URL: err = %v", err)
	}

	bare := t.TempDir()
	untagged, err := git.PlainClone(filepath.Join(bare, "repo.git"), &git.CloneOptions{URL: filepath.Join(src.root, "repo.git"), Mirror: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, tag := range []string{"v1", "v1-again", "v2"} {
		if err := untagged.Storer.RemoveReference(plumbing.NewTagReferenceName(tag)); err != nil {
			t.Fatal(err)
		}
	}
	plain := httptest.NewTLSServer(backend.New(transport.NewFilesystemLoader(osfs.New(bare), false)))
	t.Cleanup(plain.Close)
	if tags, err := (&Fetcher{Transport: plain.Client().Transport, Limits: testLimits}).Tags(t.Context(), plain.URL+"/repo.git"); err != nil || len(tags) != 0 {
		t.Fatalf("a repository without tags: %q, %v; want none and no error", tags, err)
	}
}

func TestNewerFirst(t *testing.T) {
	tags := []string{"v1.9.0", "0.0.7", "gpu-v0.12.0-chart", "v1.10.0", "gpu-v0.12.1", "v1.10.0-rc.1", "0.0.10", "gpu-v0.12.0", "latest"}
	slices.SortFunc(tags, newerFirst)
	want := []string{"v1.10.0-rc.1", "v1.10.0", "v1.9.0", "latest", "gpu-v0.12.1", "gpu-v0.12.0-chart", "gpu-v0.12.0", "0.0.10", "0.0.7"}
	if !slices.Equal(tags, want) {
		t.Fatalf("sorted = %q, want %q", tags, want)
	}
}
