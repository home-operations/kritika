package worker

import (
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/home-operations/kritika/internal/forge"
	"github.com/home-operations/kritika/internal/jobs"
	"github.com/home-operations/kritika/internal/repoconfig"
	"github.com/home-operations/kritika/internal/runner"
	"github.com/home-operations/kritika/internal/store"
)

func TestDedupesBotPatch(t *testing.T) {
	tests := []struct {
		name    string
		bot     bool
		trigger string
		want    bool
	}{
		{name: "a bot's push is deduped", bot: true, trigger: jobs.TriggerPush, want: true},
		{name: "a manual re-run of a bot's pull request is not", bot: true, trigger: jobs.TriggerManual, want: false},
		{name: "a human's push is not", bot: false, trigger: jobs.TriggerPush, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pr := &pullRequest{authorIsBot: tt.bot}
			if got := pr.dedupesBotPatch(tt.trigger); got != tt.want {
				t.Errorf("dedupesBotPatch(%q) with authorIsBot=%v = %v, want %v", tt.trigger, tt.bot, got, tt.want)
			}
		})
	}
}

func TestSnooze(t *testing.T) {
	tests := []struct {
		name     string
		metadata string
		min, max time.Duration
	}{
		{name: "the first snooze uses the base", metadata: `{}`, min: snoozeMin / 2, max: snoozeMin},
		{name: "each snooze doubles the base", metadata: `{"snoozes":3}`, min: snoozeMin * 8 / 2, max: snoozeMin * 8},
		{name: "a long run of snoozes is capped", metadata: `{"snoozes":40}`, min: snoozeMax / 2, max: snoozeMax},
		{name: "unreadable metadata snoozes as if for the first time", metadata: `not json`, min: snoozeMin / 2, max: snoozeMin},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := &Review{}
			e := earlyEnd{accountKey: "acct", logger: slog.New(slog.DiscardHandler)}
			job := &river.Job[jobs.ReviewArgs]{JobRow: &rivertype.JobRow{Metadata: []byte(tt.metadata)}}
			err := w.snooze(e, job, "review-model")
			snooze, ok := errors.AsType[*river.JobSnoozeError](err)
			if !ok {
				t.Fatalf("snooze returned %T (%v), want *river.JobSnoozeError", err, err)
			}
			if snooze.Duration < tt.min || snooze.Duration > tt.max {
				t.Errorf("Duration = %v, want within [%v, %v]", snooze.Duration, tt.min, tt.max)
			}
		})
	}
}

// TestEarlyEndStatus checks the commit status an early end reports: a skip
// states the reason recorded with it, the repository's or the runner's, and
// one with none, an admission's, the error it ends with.
func TestEarlyEndStatus(t *testing.T) {
	tests := []struct {
		name      string
		status    store.ReviewStatus
		skip      string
		reason    string
		wantState forge.StatusState
		want      string
	}{
		{name: "the repository's filter", status: store.ReviewSkipped, skip: string(repoconfig.SkipFiltered),
			wantState: forge.StatusSuccess, want: "kritika: skipped (filtered by " + repoconfig.FileName + ")"},
		{name: "a bot's unchanged patch", status: store.ReviewSkipped, skip: runner.SkipUnchangedPatch,
			wantState: forge.StatusSuccess, want: "kritika: skipped (patch unchanged since the last review)"},
		{name: "an admission's own reason", status: store.ReviewSkipped, reason: "no review model is configured for this repository",
			wantState: forge.StatusSuccess, want: "kritika: skipped (no review model is configured for this repository)"},
		{name: "a cap", status: store.ReviewCapped, reason: "reviewsPerDay (5) reached",
			wantState: forge.StatusSuccess, want: "kritika: capped (reviewsPerDay (5) reached)"},
		{name: "a superseded head reports nothing", status: store.ReviewSuperseded},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state, desc := earlyEnd{skip: tt.skip}.status(tt.status, tt.reason)
			if state != tt.wantState || desc != tt.want {
				t.Errorf("status = %q %q, want %q %q", state, desc, tt.wantState, tt.want)
			}
		})
	}
}
