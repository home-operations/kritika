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
