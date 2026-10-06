package gitfetch

import (
	"cmp"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// repo builds a local repository with a base commit and two head commits:
// one that changes a file, and a rebase of that same change on top of an
// unrelated commit that edits one file and adds another, so the patch id
// can be checked for stability and the base's movement told from the
// change's.
type repo struct {
	dir                                 string
	base, head, other, rebased, renamed string
}

func build(t *testing.T) repo {
	t.Helper()
	dir := t.TempDir()
	r, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	// A depth-one fetch of a bare SHA needs the server to allow it. Real git
	// serves a local path and only advertises the capability when told to,
	// which is also what GitHub does server-side.
	allowSHAFetch(t, r)
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
	// reachable, which a SHA fetch requires.
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

func TestRunRejectsNonSHA(t *testing.T) {
	if _, err := Run(t.Context(), Fetch{CloneURL: "x", Head: "main", Base: "HEAD~1"}); err == nil {
		t.Fatal("branch names must be rejected: only SHAs can be fetched at depth one")
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

func allowSHAFetch(t *testing.T, r *git.Repository) {
	t.Helper()
	cfg, err := r.Config()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Raw.SetOption("uploadpack", "", "allowReachableSHA1InWant", "true")
	if err := r.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
}

func TestRunPriorDelta(t *testing.T) {
	r := build(t)
	cases := []struct {
		name            string
		head, prior     string
		priorChanged    []string
		wantPrior       bool
		wantChanged     []string
		wantInDelta     string
		wantHeadChanged string
	}{
		// head and rebased carry the same main.go; README.md moved between
		// them with the base, which is no change of the pull request's.
		{name: "rebased, the change as it was", prior: r.head, priorChanged: []string{"main.go"}, wantPrior: true},
		// The paths the change touched at the prior head count as its own,
		// even where the head no longer touches them.
		{name: "rebased, a path the change dropped", prior: r.head, priorChanged: []string{"main.go", "README.md"}, wantPrior: true, wantChanged: []string{"README.md"}, wantInDelta: "+there"},
		{name: "prior already fetched as the base", prior: r.other, wantPrior: true, wantChanged: []string{"main.go"}, wantInDelta: "+func b() {}"},
		{name: "the changed file renamed since", head: r.renamed, prior: r.rebased, priorChanged: []string{"main.go"}, wantPrior: true, wantChanged: []string{"lib.go"}, wantInDelta: "lib.go", wantHeadChanged: "lib.go"},
		{name: "prior is the head", prior: r.rebased, wantPrior: true},
		{name: "unknown prior", prior: "0123456789abcdef0123456789abcdef01234567"},
		{name: "no prior"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := Run(t.Context(), Fetch{CloneURL: r.dir, Head: cmp.Or(tc.head, r.rebased), Base: r.other, Prior: tc.prior, PriorChanged: tc.priorChanged})
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
			if !strings.Contains(res.DeltaDiff, tc.wantInDelta) || (!tc.wantPrior && res.DeltaDiff != "") {
				t.Fatalf("delta diff = %q", res.DeltaDiff)
			}
			if want := cmp.Or(tc.wantHeadChanged, "main.go"); len(res.Changed) != 1 || res.Changed[0] != want {
				t.Fatalf("the merge-base diff must not change: %v, want %s", res.Changed, want)
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
