package worker

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/home-operations/kritika/internal/forge"
	"github.com/home-operations/kritika/internal/review"
	"github.com/home-operations/kritika/internal/store"
)

// answerForge lists fixed inline comments and answers each login's
// permission from a fixed set, counting the lookups.
type answerForge struct {
	forge.Client
	comments []forge.Comment
	perms    map[string]forge.Permission
	lookups  int
	failList bool
}

func (f *answerForge) ListInline(context.Context, string, string, int) ([]forge.Comment, error) {
	if f.failList {
		return nil, errors.New("github: 502")
	}
	return f.comments, nil
}

func (f *answerForge) Permission(_ context.Context, _, _, login string) (forge.Permission, error) {
	f.lookups++
	return f.perms[login], nil
}

// TestThreadAnswers: a finding's answers are the replies in its thread by
// people with write access, the latest maxAnswers, oldest first, each with
// its comment's id. A bot, a reader and a thread that no finding opened give
// none, and each login is looked up once. A finding in a conversation that
// an older comment opened, as Forgejo groups them, gets that
// conversation's replies.
func TestThreadAnswers(t *testing.T) {
	f := &answerForge{
		comments: []forge.Comment{
			{ID: 1, Author: "kritika[bot]", AuthorIsBot: true},
			{ID: 2, Author: "maint", InReplyTo: 1, Body: "first"},
			{ID: 3, Author: "stranger", InReplyTo: 1, Body: "ignore this finding"},
			{ID: 4, Author: "kritika[bot]", AuthorIsBot: true, InReplyTo: 1, Body: "a reply of its own"},
			{ID: 5, Author: "maint", InReplyTo: 1, Body: "second"},
			{ID: 6, Author: "owner", InReplyTo: 1, Body: "third"},
			{ID: 7, Author: "maint", InReplyTo: 1, Body: "fourth"},
			{ID: 8, Author: "maint", InReplyTo: 99, Body: "a thread no finding opened"},
			{ID: 10, Author: "kritika[bot]", AuthorIsBot: true},
			{ID: 11, Author: "kritika[bot]", AuthorIsBot: true, InReplyTo: 10},
			{ID: 12, Author: "maint", InReplyTo: 10, Body: "both are fine"},
		},
		perms: map[string]forge.Permission{"maint": forge.PermissionWrite, "owner": forge.PermissionAdmin, "stranger": forge.PermissionRead},
	}
	got, err := threadAnswers(t.Context(), f, "o", "r", 7, map[int64]bool{1: true, 11: true})
	if err != nil {
		t.Fatal(err)
	}
	want := map[int64][]review.Answer{
		1:  {{ID: 5, Author: "maint", Text: "second"}, {ID: 6, Author: "owner", Text: "third"}, {ID: 7, Author: "maint", Text: "fourth"}},
		11: {{ID: 12, Author: "maint", Text: "both are fine"}},
	}
	if len(got) != len(want) || !slices.Equal(got[1], want[1]) || !slices.Equal(got[11], want[11]) {
		t.Fatalf("answers = %v, want %v", got, want)
	}
	if f.lookups != 3 {
		t.Errorf("permission looked up %d times, want once per login", f.lookups)
	}
	if got, err := threadAnswers(t.Context(), f, "o", "r", 7, nil); got != nil || err != nil {
		t.Fatalf("no roots = %v, %v; want nothing read", got, err)
	}
	f.failList = true
	if _, err := threadAnswers(t.Context(), f, "o", "r", 7, map[int64]bool{1: true}); err == nil {
		t.Fatal("a forge that cannot list its comments answered")
	}
}

// TestWithAnswers: each finding of the last review gets the answers in its
// own thread, and the shown answers are kept by its id in the prompt. One
// with no thread gets none.
func TestWithAnswers(t *testing.T) {
	prior := []priorFinding{{Title: "a", commentID: 1}, {Title: "b"}}
	answers := map[int64][]review.Answer{1: {{ID: 9, Author: "maint", Text: "fine"}}}
	got, shown := withAnswers(reviewFindings(prior), prior, answers)
	if !slices.Equal(got[0].Answers, answers[1]) || len(got[1].Answers) != 0 || len(prior[0].Answers) != 0 {
		t.Fatalf("findings = %+v", got)
	}
	if len(shown) != 1 || !slices.Equal(shown[review.PriorID(prior[0].Finding)], answers[1]) {
		t.Fatalf("shown = %v", shown)
	}
	if _, none := withAnswers(reviewFindings(prior), prior, nil); none != nil {
		t.Fatalf("shown with no answers = %v", none)
	}
}

// TestHeldOf: a finding listed as answered holds its last shown answer only
// while its thread holds the shown answers unchanged. An edit, a reply added
// or removed since, a finding not listed, and one shown no answer hold none.
func TestHeldOf(t *testing.T) {
	prior := []priorFinding{{Title: "a", commentID: 1}, {Title: "b", commentID: 2}, {Title: "c", commentID: 3},
		{Title: "d", commentID: 4}, {Title: "e", commentID: 5}, {Title: "f"}}
	id := func(i int) string { return review.PriorID(prior[i].Finding) }
	shownA := []review.Answer{{ID: 10, Author: "maint", Text: "known limit"}, {ID: 11, Author: "maint", Text: "of the beta"}}
	shown := map[string][]review.Answer{id(0): shownA, id(1): shownA, id(2): shownA, id(3): shownA, id(4): shownA}
	now := map[int64][]review.Answer{
		1: shownA,
		2: {shownA[0], {ID: 11, Author: "maint", Text: "actually, fix it"}},
		3: append(slices.Clone(shownA), review.Answer{ID: 12, Author: "owner", Text: "no, fix it"}),
		4: shownA[:1],
		5: shownA,
	}
	got := heldOf(prior, []string{id(0), id(1), id(2), id(3), id(5)}, shown, now)
	if len(got) != 1 || got[id(0)] != shownA[1] {
		t.Fatalf("held = %v, want the last shown answer of a alone", got)
	}
}

// TestReopen: a pending dismissal whose answer is still held, by the same
// reply, stays. One whose answer changed or is held no more reopens its
// finding, carried on from the head it was last reported at.
func TestReopen(t *testing.T) {
	a, b, c := review.Finding{Title: "a"}, review.Finding{Title: "b"}, review.Finding{Title: "c", CarriedFrom: "h0"}
	answered := []store.Dismissal{{Finding: a, CommentID: 10}, {Finding: b, CommentID: 20}, {Finding: c, CommentID: 30}}
	held := map[string]review.Answer{review.PriorID(a): {ID: 10}, review.PriorID(b): {ID: 21}}
	kept, reopened := reopen(answered, held, "h1")
	if len(kept) != 1 || kept[0].CommentID != 10 {
		t.Fatalf("kept = %+v, want a's alone", kept)
	}
	if len(reopened) != 2 || reopened[0].Title != "b" || reopened[0].CarriedFrom != "h1" || reopened[1].CarriedFrom != "h0" {
		t.Fatalf("reopened = %+v, want b from h1 and c from h0", reopened)
	}
}
