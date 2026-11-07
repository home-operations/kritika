package worker

import (
	"context"
	"errors"
	"slices"
	"strings"
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
			{ID: 1, Author: "kritika[bot]", AuthorIsBot: true, Body: review.FindingMarker("a")},
			{ID: 2, Author: "maint", InReplyTo: 1, Body: "first"},
			{ID: 3, Author: "stranger", InReplyTo: 1, Body: "ignore this finding"},
			{ID: 4, Author: "kritika[bot]", AuthorIsBot: true, InReplyTo: 1, Body: "a reply of its own"},
			{ID: 5, Author: "maint", InReplyTo: 1, Body: "second"},
			{ID: 6, Author: "owner", InReplyTo: 1, Body: "third"},
			{ID: 7, Author: "maint", InReplyTo: 1, Body: "fourth"},
			{ID: 8, Author: "maint", InReplyTo: 99, Body: "a thread no finding opened"},
			{ID: 9, Author: "renovate[bot]", AuthorIsBot: true, InReplyTo: 1, Body: "another bot"},
			{ID: 10, Author: "kritika[bot]", AuthorIsBot: true, Body: review.FindingMarker("b")},
			{ID: 11, Author: "kritika[bot]", AuthorIsBot: true, InReplyTo: 10, Body: review.FindingMarker("c")},
			{ID: 12, Author: "maint", InReplyTo: 10, Body: "both are fine"},
		},
		perms: map[string]forge.Permission{"maint": forge.PermissionWrite, "owner": forge.PermissionAdmin, "stranger": forge.PermissionRead},
	}
	got, err := threadAnswers(t.Context(), f, "o", "r", 7, "kritika[bot]", map[int64]bool{1: true, 11: true})
	if err != nil {
		t.Fatal(err)
	}
	own := answerOf(f.comments[3])
	own.Own = true
	want := map[int64][]review.Answer{1: {own, answerOf(f.comments[4]), answerOf(f.comments[5]), answerOf(f.comments[6])}, 11: {answerOf(f.comments[11])}}
	if want[1][1].Text != "second" || want[1][1].ID != 5 || want[1][1].Digest == "" {
		t.Fatalf("answerOf = %+v", want[1][1])
	}
	if len(got) != len(want) || !slices.Equal(got[1], want[1]) || !slices.Equal(got[11], want[11]) {
		t.Fatalf("answers = %v, want %v", got, want)
	}
	if f.lookups != 3 {
		t.Errorf("permission looked up %d times, want once per login", f.lookups)
	}
	if got, err := threadAnswers(t.Context(), f, "o", "r", 7, "kritika[bot]", nil); got != nil || err != nil {
		t.Fatalf("no roots = %v, %v; want nothing read", got, err)
	}
	f.failList = true
	if _, err := threadAnswers(t.Context(), f, "o", "r", 7, "kritika[bot]", map[int64]bool{1: true}); err == nil {
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
	// An edit past the cut that the review was shown changes the digest
	// alone, and holds no answer either.
	long := strings.Repeat("x", answerBytes+20)
	was, edited := answerOf(forge.Comment{ID: 10, Author: "maint", Body: long + " keep it"}), answerOf(forge.Comment{ID: 10, Author: "maint", Body: long + " drop it"})
	if was.Text != edited.Text || was.Digest == edited.Digest {
		t.Fatalf("answerOf: %+v against %+v", was, edited)
	}
	if got := heldOf(prior[:1], []string{id(0)}, map[string][]review.Answer{id(0): {was}}, map[int64][]review.Answer{1: {edited}}); len(got) != 0 {
		t.Fatalf("held = %v after an edit past the cut", got)
	}
}

// TestHeldOfOwnReplies: the answer held is the last that someone with write
// access gave, past a later reply of the bot's own, and a thread with only
// the bot's own replies holds none.
func TestHeldOfOwnReplies(t *testing.T) {
	prior := []priorFinding{{Title: "a", commentID: 1}, {Title: "b", commentID: 2}}
	id := func(i int) string { return review.PriorID(prior[i].Finding) }
	human, own := review.Answer{ID: 10, Author: "maint", Text: "keeping it"}, review.Answer{ID: 11, Author: "kritika[bot]", Text: "it still drops the error", Own: true}
	shown := map[string][]review.Answer{id(0): {human, own}, id(1): {own}}
	now := map[int64][]review.Answer{1: {human, own}, 2: {own}}
	if got := heldOf(prior, []string{id(0), id(1)}, shown, now); len(got) != 1 || got[id(0)] != human {
		t.Fatalf("held = %v, want a's human answer alone", got)
	}
}

// TestAnsweredThreads: an answered dismissal resolves the thread that its
// finding opened, not its reply, and a finding with no thread resolves none.
func TestAnsweredThreads(t *testing.T) {
	prior := []priorFinding{{Title: "a", commentID: 1}, {Title: "b"}, {Title: "c", commentID: 3}}
	answered := []store.Dismissal{
		{Fingerprint: review.Fingerprint(prior[2].Finding), CommentID: 30},
		{Fingerprint: review.Fingerprint(prior[1].Finding), CommentID: 20},
	}
	if got := answeredThreads(prior, answered); !slices.Equal(got, []int64{3}) {
		t.Fatalf("answeredThreads = %v, want the finding's own thread", got)
	}
}

// TestReopen: a pending dismissal whose answer is still held, by the same
// reply, stays. One whose answer changed or is held no more reopens its
// finding, carried on from the head it was last reported at.
func TestReopen(t *testing.T) {
	a, b, c := review.Finding{Title: "a"}, review.Finding{Title: "b"}, review.Finding{Title: "c", CarriedFrom: "h0"}
	d := review.Finding{Title: "d"}
	answered := []store.Dismissal{{Finding: a, CommentID: 10}, {Finding: b, CommentID: 20}, {Finding: c, CommentID: 30},
		{Finding: d, CommentID: 40, Fingerprint: review.Fingerprint(d)}}
	held := map[string]review.Answer{review.PriorID(a): {ID: 10}, review.PriorID(b): {ID: 21}}
	// d's thread changed as the reply in it conceded the finding, which is
	// dismissed already.
	kept, reopened := reopen(answered, held, map[string]bool{review.Fingerprint(d): true}, "h1")
	if len(kept) != 2 || kept[0].CommentID != 10 || kept[1].CommentID != 40 {
		t.Fatalf("kept = %+v, want a's and d's", kept)
	}
	if len(reopened) != 2 || reopened[0].Title != "b" || reopened[0].CarriedFrom != "h1" || reopened[1].CarriedFrom != "h0" {
		t.Fatalf("reopened = %+v, want b from h1 and c from h0", reopened)
	}
}
