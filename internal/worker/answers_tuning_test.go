package worker

import (
	"strings"
	"testing"

	"github.com/home-operations/kritika/internal/forge"
	"github.com/home-operations/kritika/internal/review"
	"github.com/home-operations/kritika/internal/tuning"
)

// TestThreadAnswersFollowTuning: how many of a thread's replies a re-review
// is shown, and how much of each, are the process's tuning.
func TestThreadAnswersFollowTuning(t *testing.T) {
	long := strings.Repeat("x", 100)
	f := &answerForge{
		comments: []forge.Comment{
			{ID: 1, Author: "kritika[bot]", AuthorIsBot: true, Body: review.FindingMarker("a")},
			{ID: 2, Author: "maint", InReplyTo: 1, Body: long},
			{ID: 3, Author: "maint", InReplyTo: 1, Body: long},
			{ID: 4, Author: "maint", InReplyTo: 1, Body: long},
		},
		perms: map[string]forge.Permission{"maint": forge.PermissionWrite},
	}
	answers := func() []review.Answer {
		t.Helper()
		got, err := threadAnswers(t.Context(), f, "o", "r", 7, "kritika[bot]", map[int64]bool{1: true})
		if err != nil {
			t.Fatal(err)
		}
		return got[1]
	}
	if got := answers(); len(got) != 3 || got[0].Text != long {
		t.Fatalf("answers at the defaults = %+v, want all 3 whole", got)
	}
	tuning.Override(t, func(v *tuning.Values) {
		v.Worker.MaxAnswers = 2
		v.Worker.AnswerBytes = 40
	})
	got := answers()
	if len(got) != 2 || got[0].ID != 3 || got[1].ID != 4 {
		t.Fatalf("answers = %+v, want the latest 2", got)
	}
	for _, a := range got {
		if len(a.Text) > 40 || !strings.HasPrefix(a.Text, "xxx") {
			t.Errorf("answer %d = %q, want it cut to 40 bytes", a.ID, a.Text)
		}
	}
}

// TestHeldOfAcrossATuningChange: publication reads a thread again under the
// caps the review was shown it under, so a reply that the caps changed
// since would cut, or leave out of the window, still holds its answer; and
// an answer kept before the caps were recorded is read under the caps of
// the build that kept it.
func TestHeldOfAcrossATuningChange(t *testing.T) {
	long := strings.Repeat("x", 100)
	f := &answerForge{
		comments: []forge.Comment{
			{ID: 1, Author: "kritika[bot]", AuthorIsBot: true, Body: review.FindingMarker("a")},
			{ID: 2, Author: "maint", InReplyTo: 1, Body: "first"},
			{ID: 3, Author: "maint", InReplyTo: 1, Body: long},
		},
		perms: map[string]forge.Permission{"maint": forge.PermissionWrite},
	}
	prior := []priorFinding{{Title: "a", commentID: 1}}
	id := review.PriorID(prior[0].Finding)
	answers, err := threadAnswers(t.Context(), f, "o", "r", 7, "kritika[bot]", map[int64]bool{1: true})
	if err != nil {
		t.Fatal(err)
	}
	findings, shown := withAnswers(reviewFindings(prior), prior, answers)
	if a := findings[0].Answers[0]; a.Cut != 0 || a.Window != 0 {
		t.Fatalf("the runner's answer %+v records caps", a)
	}
	if a := shown[id][0]; a.Cut != tuning.Get().Worker.AnswerBytes || a.Window != tuning.Get().Worker.MaxAnswers {
		t.Fatalf("the kept answer %+v does not record the caps", a)
	}
	tuning.Override(t, func(v *tuning.Values) {
		v.Worker.MaxAnswers = 1
		v.Worker.AnswerBytes = 40
	})
	held := func(shown map[string][]review.Answer) map[string]review.Answer {
		t.Helper()
		now, err := threadAnswersUnder(t.Context(), f, "o", "r", 7, "kritika[bot]", map[int64]bool{1: true}, shownCaps(shown))
		if err != nil {
			t.Fatal(err)
		}
		return heldOf(prior, []string{id}, shown, now)
	}
	if got := held(shown); len(got) != 1 || got[id].ID != 3 || got[id].Text != long {
		t.Fatalf("held across the change = %v, want the unchanged long reply", got)
	}
	now, err := threadAnswers(t.Context(), f, "o", "r", 7, "kritika[bot]", map[int64]bool{1: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := heldOf(prior, []string{id}, shown, now); len(got) != 0 {
		t.Fatalf("held under the new caps = %v; the test no longer crosses them", got)
	}
	legacy := map[string][]review.Answer{id: withoutCaps(shown[id])}
	if got := held(legacy); len(got) != 1 || got[id].ID != 3 {
		t.Fatalf("held of a legacy snapshot = %v, want the reply under the legacy caps", got)
	}
}
