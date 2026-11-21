package gitfetch

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	git "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/format/pktline"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/go-git/go-git/v6/plumbing/transport"

	"github.com/home-operations/kritika/internal/gittest"
)

// repo builds a local repository with a base commit and two head commits. One
// changes a file. The other is a rebase of that same change on top of an
// unrelated commit that edits one file and adds another. So the test can make
// sure that the patch id is stable, and tell the base's movement from the
// change's.
type repo struct {
	dir                                 string
	base, head, other, rebased, renamed string
	// On a third branch, the base gains a file that the change then edits at
	// its end. long is the merge base that the change (prior) was reviewed
	// against. touched is the merge base after the base edited the file's
	// start. merged is the change brought up to date with it as it was. moved
	// is the change brought up to date and extended.
	long, prior, touched, merged, moved string
	// On a fourth branch from base, a change and its revert: a head
	// ahead of the base whose tree is the base's.
	reverted string
}

func TestTrees(t *testing.T) {
	r := build(t)
	for _, base := range []string{"", r.base} {
		t.Run(cmp.Or(base, "head only"), func(t *testing.T) {
			res, err := Trees(t.Context(), Fetch{CloneURL: r.dir, Head: r.head, Base: base})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = res.Close() })
			if res.Head.Hash.String() != r.head || (base != "" && (res.Base == nil || res.Base.Hash.String() != base)) {
				t.Fatalf("unexpected commits: %+v", res)
			}
			if res.Diff != "" || res.PatchID != "" || res.Changed != nil {
				t.Fatalf("patch was rendered: %+v", res)
			}
			tree, err := res.Head.Tree()
			if err != nil {
				t.Fatal(err)
			}
			f, err := tree.File("main.go")
			if err != nil {
				t.Fatal(err)
			}
			content, err := f.Contents()
			if err != nil || !strings.Contains(content, "func b()") {
				t.Fatalf("head content = %q, %v", content, err)
			}
			if err := res.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(res.Dir); !os.IsNotExist(err) {
				t.Fatalf("repository left behind: %v", err)
			}
		})
	}
}

func build(t *testing.T) repo {
	t.Helper()
	dir := t.TempDir()
	r, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	gittest.Unsigned(t, r)
	wt, _ := r.Worktree()
	commit := func(msg string, files map[string]string) string {
		t.Helper()
		for name, content := range files {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := wt.Add(name); err != nil {
				t.Fatal(err)
			}
		}
		h, err := wt.Commit(msg, &git.CommitOptions{Author: &object.Signature{Name: "t", Email: "t@x", When: time.Now()}})
		if err != nil {
			t.Fatal(err)
		}
		return h.String()
	}
	out := repo{dir: dir}
	out.base = commit("base", map[string]string{"main.go": "package main\n\nfunc a() {}\n", "README.md": "hi\n"})
	out.head = commit("change", map[string]string{"main.go": "package main\n\nfunc a() {}\n\nfunc b() {}\n"})
	// On a second branch from base, add an unrelated commit and re-apply the
	// same change. A branch rather than a reset keeps the first head
	// reachable, which real git's upload-pack needs to serve it by SHA.
	if err := wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName("rebased"), Create: true, Hash: plumbing.NewHash(out.base)}); err != nil {
		t.Fatal(err)
	}
	out.other = commit("unrelated", map[string]string{"README.md": "hi\nthere\n", "NOTES.md": "new\n"})
	out.rebased = commit("change again", map[string]string{"main.go": "package main\n\nfunc a() {}\n\nfunc b() {}\n"})
	// On top of the rebase, the changed file moves: a rename the delta
	// since rebased must keep under either of its names.
	if _, err := wt.Move("main.go", "lib.go"); err != nil {
		t.Fatal(err)
	}
	out.renamed = commit("rename", nil)
	branch := func(name, from string) {
		t.Helper()
		if err := wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName(name), Create: true, Hash: plumbing.NewHash(from)}); err != nil {
			t.Fatal(err)
		}
	}
	const svc = "package svc\n\nfunc one() {}\n\nfunc two() {}\n\nfunc three() {}\n\nfunc four() {}\n\nfunc five() {}\n"
	const touchedSvc = "package svc\n\n// svc is the service.\n\nfunc one() {}\n\nfunc two() {}\n\nfunc three() {}\n\nfunc four() {}\n\nfunc five() {}\n"
	branch("long", out.base)
	out.long = commit("base adds svc.go", map[string]string{"svc.go": svc})
	branch("prior", out.long)
	out.prior = commit("change edits svc.go", map[string]string{"svc.go": svc + "\nfunc b() {}\n"})
	branch("merged", out.long)
	out.touched = commit("base edits svc.go", map[string]string{"svc.go": touchedSvc})
	out.merged = commit("change brought up to date", map[string]string{"svc.go": touchedSvc + "\nfunc b() {}\n"})
	out.moved = commit("change brought up to date and extended", map[string]string{"svc.go": touchedSvc + "\nfunc b() {}\n\nfunc c() {}\n"})
	branch("reverted", out.base)
	commit("change", map[string]string{"main.go": "package main\n\nfunc a() {}\n\nfunc b() {}\n"})
	out.reverted = commit("revert change", map[string]string{"main.go": "package main\n\nfunc a() {}\n"})
	return out
}

func TestRunDiffsTwoCommitsAndPatchIDIsStable(t *testing.T) {
	r := build(t)
	ctx := t.Context()

	res, err := Run(ctx, Fetch{CloneURL: r.dir, Head: r.head, Base: r.base})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	defer func() { _ = res.Close() }()
	if !strings.Contains(res.Diff, "+func b() {}") || strings.Contains(res.Diff, "README") {
		t.Fatalf("diff = %q", res.Diff)
	}
	if len(res.Changed) != 1 || res.Changed[0] != "main.go" {
		t.Fatalf("changed = %v", res.Changed)
	}
	if _, err := os.Stat(res.Dir); err != nil {
		t.Fatal("bare repo should exist until Close")
	}

	rebased, err := Run(ctx, Fetch{CloneURL: r.dir, Head: r.rebased, Base: r.other})
	if err != nil {
		t.Fatalf("Run rebased: %v", err)
	}
	defer func() { _ = rebased.Close() }()
	if rebased.PatchID != res.PatchID {
		t.Fatalf("patch id changed across a rebase of the same change:\n%s\n%s", res.Diff, rebased.Diff)
	}

	different, err := Run(ctx, Fetch{CloneURL: r.dir, Head: r.other, Base: r.base})
	if err != nil {
		t.Fatalf("Run other: %v", err)
	}
	defer func() { _ = different.Close() }()
	if different.PatchID == res.PatchID {
		t.Fatal("a different change must have a different patch id")
	}
	if err := res.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(res.Dir); !os.IsNotExist(err) {
		t.Fatal("Close should remove the bare repo")
	}
}

func TestRunEmptyDiffChangesNoPaths(t *testing.T) {
	r := build(t)

	res, err := Run(t.Context(), Fetch{CloneURL: r.dir, Head: r.reverted, Base: r.base})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	defer func() { _ = res.Close() }()
	if res.Diff != "" {
		t.Fatalf("diff = %q", res.Diff)
	}
	if res.Changed == nil || len(res.Changed) != 0 {
		t.Fatalf("changed = %#v, want empty and non-nil", res.Changed)
	}
}

func TestRunRejectsNonSHA(t *testing.T) {
	if _, err := Run(t.Context(), Fetch{CloneURL: "x", Head: "main", Base: "HEAD~1"}); err == nil {
		t.Fatal("branch names must be rejected: only SHAs can be fetched at depth one")
	}
}

// TestRunNotFoundNamesTheToken: a remote answers not found, as a forge does
// for a repository that the token cannot see. The failure is reported with
// the token as a possible cause, and go-git's sentinel stays in the chain.
func TestRunNotFoundNamesTheToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) }))
	t.Cleanup(srv.Close)
	_, err := Run(t.Context(), Fetch{CloneURL: srv.URL + "/acme/webapp.git", Token: "scoped", Head: strings.Repeat("a", 40)})
	if !errors.Is(err, transport.ErrRepositoryNotFound) || !strings.Contains(err.Error(), "the token cannot see it") {
		t.Fatalf("err = %v", err)
	}
}

// TestTreesCommitUnavailable: a commit that the remote does not serve fails
// the fetch with an UnavailableError that names it, and a remote that fails
// for any other reason does not.
func TestTreesCommitUnavailable(t *testing.T) {
	r := build(t)
	missing := "0123456789abcdef0123456789abcdef01234567"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	refusing := refusingUploadPack(t, r.head, missing)
	for _, tc := range []struct {
		name    string
		fetch   Fetch
		refused string
	}{
		{name: "base the remote does not have", fetch: Fetch{CloneURL: r.dir, Head: r.head, Base: missing}, refused: missing},
		{name: "head the remote does not have", fetch: Fetch{CloneURL: r.dir, Head: missing, Base: r.base}, refused: missing},
		{name: "base that smart HTTP refuses", fetch: Fetch{CloneURL: refusing + "/acme/webapp.git", Head: r.head, Base: missing}, refused: missing},
		{name: "head that smart HTTP refuses", fetch: Fetch{CloneURL: refusing + "/acme/webapp.git", Head: missing, Base: r.head}, refused: missing},
		{name: "remote unavailable", fetch: Fetch{CloneURL: srv.URL + "/acme/webapp.git", Head: r.head, Base: r.base}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Trees(t.Context(), tc.fetch)
			e, ok := errors.AsType[*UnavailableError](err)
			if err == nil || ok != (tc.refused != "") || errors.Is(err, ErrCommitUnavailable) != ok || ok && e.SHA != tc.refused {
				t.Fatalf("err = %v, want the commit %q unavailable", err, tc.refused)
			}
		})
	}
}

// refusingUploadPack serves smart HTTP as git's upload-pack does for a
// repository whose branch is at tip: it advertises the branch, and answers a
// fetch that wants refused with the error packet git sends, "not our ref". It
// returns the server's URL.
func refusingUploadPack(t *testing.T, tip, refused string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/info/refs") && r.URL.Query().Get("service") == "git-upload-pack":
			w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
			_, err = pktline.Writeln(w, "# service=git-upload-pack")
			err = errors.Join(err, pktline.WriteFlush(w))
			_, werr := pktline.WriteString(w, tip+" HEAD\x00multi_ack_detailed side-band-64k thin-pack ofs-delta shallow no-progress "+
				"allow-tip-sha1-in-want allow-reachable-sha1-in-want symref=HEAD:refs/heads/main\n")
			err = errors.Join(err, werr)
			_, werr = pktline.Writeln(w, tip+" refs/heads/main")
			err = errors.Join(err, werr, pktline.WriteFlush(w))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/git-upload-pack"):
			w.Header().Set("Content-Type", "application/x-git-upload-pack-result")
			err = (&pktline.ErrorLine{Text: "upload-pack: not our ref " + refused}).Encode(w)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
		if err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// TestRefusedCommit: the server's refusal to serve a commit by SHA, as git's
// upload-pack words it, counts as the commit being unavailable, and so does
// go-git's own server not finding a commit that was asked for. Each names
// the commit refused, as a whole SHA. Other errors that the server or the
// transport send do not count, nor a refusal or a missing object that names
// only a commit that was not asked for.
func TestRefusedCommit(t *testing.T) {
	head, base := strings.Repeat("a", 40), strings.Repeat("b", 40)
	for _, tc := range []struct {
		name string
		err  error
		want bool
		sha  string
	}{
		{name: "error line", err: fmt.Errorf("fetch: %w", &pktline.ErrorLine{Text: "upload-pack: not our ref " + head}), want: true, sha: head},
		{name: "remote error", err: transport.NewRemoteError("fatal: git upload-pack: not our ref " + base), want: true, sha: base},
		{name: "refusal of a commit not asked for", err: &pktline.ErrorLine{Text: "upload-pack: not our ref " + strings.Repeat("c", 40)}},
		{name: "refusal of a sha256 commit not asked for", err: &pktline.ErrorLine{Text: "upload-pack: not our ref " + strings.Repeat("c", 64)}},
		{name: "refusal naming a short hex word", err: &pktline.ErrorLine{Text: "upload-pack: not our ref cafe"}, want: true},
		{name: "refusal naming no commit", err: &pktline.ErrorLine{Text: "upload-pack: not our ref"}, want: true},
		{name: "wanted object not found", err: fmt.Errorf("getting wanted object %s: %w", base, plumbing.ErrObjectNotFound), want: true, sha: base},
		{name: "other object not found", err: fmt.Errorf("getting tree %s: %w", strings.Repeat("c", 40), plumbing.ErrObjectNotFound)},
		{name: "object not found within a longer id", err: fmt.Errorf("getting tree %s0: %w", base, plumbing.ErrObjectNotFound)},
		{name: "object not found unnamed", err: fmt.Errorf("decoding pack: %w", plumbing.ErrObjectNotFound)},
		{name: "other error line", err: &pktline.ErrorLine{Text: "upload-pack: internal error"}},
		{name: "repository not found", err: transport.ErrRepositoryNotFound},
		{name: "cut short", err: io.ErrUnexpectedEOF},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if sha, got := refusedCommit(tc.err, head, base); got != tc.want || sha != tc.sha {
				t.Fatalf("refusedCommit(%v) = %q, %v, want %q, %v", tc.err, sha, got, tc.sha, tc.want)
			}
		})
	}
}

func TestPatchIDIgnoresPositions(t *testing.T) {
	a := "diff --git a/f b/f\nindex 111..222 100644\n--- a/f\n+++ b/f\n@@ -1,3 +1,4 @@\n a\n+b\n c\n"
	b := "diff --git a/f b/f\nindex 333..444 100644\n--- a/f\n+++ b/f\n@@ -10,3 +10,4 @@\n a\n+b\n c\n"
	c := "diff --git a/f b/f\nindex 333..444 100644\n--- a/f\n+++ b/f\n@@ -10,3 +10,4 @@\n a\n+bb\n c\n"
	if PatchID(a) != PatchID(b) {
		t.Fatal("hunk headers and index lines must not affect the patch id")
	}
	if PatchID(a) == PatchID(c) {
		t.Fatal("changed content must affect the patch id")
	}
}

func TestRunPriorDelta(t *testing.T) {
	r := build(t)
	cases := []struct {
		name            string
		head, base      string
		prior           string
		priorChanged    []string
		priorBase       string
		wantPrior       bool
		wantChanged     []string
		wantInDelta     string
		wantNotInDelta  string
		wantBaseErr     bool
		wantHeadChanged string
		wantRenamedFrom []string
	}{
		// head and rebased carry the same main.go. README.md moved between
		// them with the base, which is no change of the pull request's.
		{name: "rebased, the change as it was", prior: r.head, priorChanged: []string{"main.go"}, wantPrior: true},
		// The paths the change touched at the prior head count as its own,
		// even where the head no longer touches them.
		{name: "rebased, a path the change dropped", prior: r.head, priorChanged: []string{"main.go", "README.md"}, wantPrior: true, wantChanged: []string{"README.md"}, wantInDelta: "+there"},
		{name: "prior already fetched as the base", prior: r.other, wantPrior: true, wantChanged: []string{"main.go"}, wantInDelta: "+func b() {}"},
		{name: "the changed file renamed since", head: r.renamed, prior: r.rebased, priorChanged: []string{"main.go"}, wantPrior: true, wantChanged: []string{"lib.go"}, wantInDelta: "lib.go", wantHeadChanged: "lib.go",
			wantRenamedFrom: []string{"main.go"}},
		{name: "prior is the head", prior: r.rebased, wantPrior: true},
		// The base edited the start of the file the change edits at its end,
		// and the change was brought up to date: what the base gained in the
		// file is no change of the pull request's.
		{name: "brought up to date, the change as it was", head: r.merged, base: r.touched, prior: r.prior, priorBase: r.long,
			priorChanged: []string{"svc.go"}, wantPrior: true, wantHeadChanged: "svc.go"},
		{name: "brought up to date, the change extended", head: r.moved, base: r.touched, prior: r.prior, priorBase: r.long,
			priorChanged: []string{"svc.go"}, wantPrior: true, wantChanged: []string{"svc.go"}, wantInDelta: "+func c() {}",
			wantNotInDelta: "svc is the service", wantHeadChanged: "svc.go"},
		// Without the merge base the last review saw, what the base gained
		// cannot be told from the change's and stays in the delta.
		{name: "brought up to date, the prior merge base unreachable", head: r.merged, base: r.touched, prior: r.prior,
			priorBase: "0123456789abcdef0123456789abcdef01234567", priorChanged: []string{"svc.go"}, wantPrior: true,
			wantChanged: []string{"svc.go"}, wantInDelta: "+// svc is the service.", wantBaseErr: true, wantHeadChanged: "svc.go"},
		// With no delta there is no hunk of the base's to leave out, so the
		// prior merge base is not fetched and cannot fail.
		{name: "nothing moved, the prior merge base not fetched", prior: r.rebased,
			priorBase: "0123456789abcdef0123456789abcdef01234567", wantPrior: true},
		{name: "unknown prior", prior: "0123456789abcdef0123456789abcdef01234567"},
		{name: "no prior"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := Run(t.Context(), Fetch{CloneURL: r.dir, Head: cmp.Or(tc.head, r.rebased), Base: cmp.Or(tc.base, r.other), Prior: tc.prior,
				PriorChanged: tc.priorChanged, PriorBase: tc.priorBase})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			defer func() { _ = res.Close() }()
			if (res.Prior != nil) != tc.wantPrior {
				t.Fatalf("prior = %v, want present %v", res.Prior, tc.wantPrior)
			}
			if wantErr := tc.prior != "" && !tc.wantPrior; (res.PriorErr != nil) != wantErr {
				t.Fatalf("prior error = %v, want one %v", res.PriorErr, wantErr)
			}
			if tc.wantPrior && res.Prior.Hash.String() != tc.prior {
				t.Fatalf("prior = %s", res.Prior.Hash)
			}
			if strings.Join(res.DeltaChanged, ",") != strings.Join(tc.wantChanged, ",") {
				t.Fatalf("delta changed = %v, want %v", res.DeltaChanged, tc.wantChanged)
			}
			if !strings.Contains(res.DeltaDiff, tc.wantInDelta) || (!tc.wantPrior && res.DeltaDiff != "") ||
				(tc.wantNotInDelta != "" && strings.Contains(res.DeltaDiff, tc.wantNotInDelta)) {
				t.Fatalf("delta diff = %q", res.DeltaDiff)
			}
			if (res.PriorBaseErr != nil) != tc.wantBaseErr {
				t.Fatalf("prior base error = %v, want one %v", res.PriorBaseErr, tc.wantBaseErr)
			}
			if want := cmp.Or(tc.wantHeadChanged, "main.go"); len(res.Changed) != 1 || res.Changed[0] != want {
				t.Fatalf("the merge-base diff must not change: %v, want %s", res.Changed, want)
			}
			if res.RenamedFrom == nil || strings.Join(res.RenamedFrom, ",") != strings.Join(tc.wantRenamedFrom, ",") {
				t.Fatalf("renamed from = %#v, want %v", res.RenamedFrom, tc.wantRenamedFrom)
			}
		})
	}
}

func TestRunRejectsNonSHAPrior(t *testing.T) {
	r := build(t)
	if _, err := Run(t.Context(), Fetch{CloneURL: r.dir, Head: r.head, Base: r.base, Prior: "main"}); err == nil {
		t.Fatal("a prior head that is not a SHA must be rejected")
	}
}
