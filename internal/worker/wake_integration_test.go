//go:build integration

package worker

import (
	"context"
	"log/slog"
	"testing"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/configfile/configfiletest"
	"github.com/home-operations/kritika/internal/store"
	"github.com/home-operations/kritika/internal/store/storetest"
)

// TestSlotWakeChecksSnoozedReview: a review saved as snoozed for a model slot
// is woken when a slot is free by then. It is left to its snooze while every
// slot is held. A job snoozed for anything else, of another kind, or of an
// account that is not running, is left alone. Run returns only once the
// checks it started are done.
func TestSlotWakeChecksSnoozedReview(t *testing.T) {
	ctx := t.Context()
	appStore := storetest.Open(t)
	t.Setenv("TEST_PEM", "pem")
	t.Setenv("TEST_SECRET", "test-provider-key")
	file := configfiletest.Load(t, configYAML)
	if err := appStore.ApplyConfig(ctx, file); err != nil {
		t.Fatal(err)
	}
	storetest.Repositories(t, appStore, file, "onedr0p/home-ops")
	account, _ := file.Account(configfile.ForgeGitHub, "onedr0p")
	const model = "test/wake-model"
	t.Cleanup(func() {
		_, _ = appStore.App().Exec(context.Background(), `DELETE FROM model_leases WHERE model_key = $1`, model)
	})
	waiting := `{"snoozes": 1, "slot_wait": "` + model + `"}`
	insert := func(t *testing.T, kind, accountID, metadata string) *rivertype.JobRow {
		t.Helper()
		args := `{"account_id": "` + accountID + `"}`
		var id int64
		if err := appStore.App().QueryRow(ctx, `INSERT INTO river_job (kind, queue, args, state, max_attempts, scheduled_at, metadata)
			VALUES ($1, 'review', $2::jsonb, 'scheduled', 8, now() + interval '5 minutes', $3::jsonb) RETURNING id`,
			kind, args, metadata).Scan(&id); err != nil {
			t.Fatalf("insert river job: %v", err)
		}
		t.Cleanup(func() { _, _ = appStore.App().Exec(context.Background(), `DELETE FROM river_job WHERE id = $1`, id) })
		return &rivertype.JobRow{ID: id, Kind: kind, EncodedArgs: []byte(args), Metadata: []byte(metadata)}
	}
	jobState := func(t *testing.T, id int64) (state string, due bool) {
		t.Helper()
		if err := appStore.App().QueryRow(ctx, `SELECT state, scheduled_at <= now() FROM river_job WHERE id = $1`, id).Scan(&state, &due); err != nil {
			t.Fatal(err)
		}
		return state, due
	}
	wake := &SlotWake{Store: appStore, Current: configfile.NewCurrent(file), Logger: slog.New(slog.DiscardHandler)}
	for _, tc := range []struct {
		name, kind, account, metadata string
		held                          bool
		woken                         bool
	}{
		{name: "a review snoozed for a slot wakes when one is free", kind: "review", account: account.ID(), metadata: waiting, woken: true},
		{name: "a review snoozed for a slot sleeps while every slot is held", kind: "review", account: account.ID(), metadata: waiting, held: true},
		{name: "a review snoozed for something other than a slot is left alone", kind: "review", account: account.ID(), metadata: `{"snoozes": 1}`},
		{name: "a review snoozed for something else after a slot is left alone", kind: "review", account: account.ID(), metadata: `{"snoozes": 2, "slot_wait": null}`},
		{name: "a job of another kind is left alone", kind: "index", account: account.ID(), metadata: waiting},
		{name: "a review of an account that is not running is left alone", kind: "review", account: "github:nobody", metadata: waiting},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.held {
				// The provider's concurrency is 1, so one lease holds every slot.
				l, err := appStore.TakeLease(ctx, account.ID(), model, file.Slots(account, model), -1)
				if err != nil || l == nil {
					t.Fatalf("TakeLease = %v, %v", l, err)
				}
				t.Cleanup(func() { _, _ = l.Release(context.Background()) })
			}
			job := insert(t, tc.kind, tc.account, tc.metadata)
			wake.check(ctx, job)
			state, due := jobState(t, job.ID)
			if tc.woken && (state != "available" || !due) {
				t.Fatalf("job is %s and due = %v, want available now", state, due)
			}
			if !tc.woken && (state != "scheduled" || due) {
				t.Fatalf("job is %s and due = %v, want scheduled at its own time", state, due)
			}
		})
	}

	t.Run("Run returns once the checks it started have", func(t *testing.T) {
		job := insert(t, "review", account.ID(), waiting)
		events := make(chan *river.Event)
		ran := make(chan struct{})
		go func() {
			defer close(ran)
			wake.Run(ctx, events)
		}()
		events <- &river.Event{Kind: river.EventKindJobSnoozed, Job: job}
		close(events)
		<-ran
		if state, due := jobState(t, job.ID); state != "available" || !due {
			t.Fatalf("job is %s and due = %v once Run returned, want available now", state, due)
		}
	})
}

// TestAdmissionWakesNextWaiter: a review that has its slots clears its mark
// of the slot it waited for. It wakes the review that is next in line for
// each of its models, and for the model of the mark, when the model has a
// slot free, and leaves it asleep when every slot is held.
func TestAdmissionWakesNextWaiter(t *testing.T) {
	ctx := t.Context()
	st := storetest.Open(t)
	t.Setenv("TEST_PEM", "pem")
	t.Setenv("TEST_SECRET", "test-provider-key")
	file := configfiletest.Load(t, configYAML)
	if err := st.ApplyConfig(ctx, file); err != nil {
		t.Fatal(err)
	}
	storetest.Repositories(t, st, file, "onedr0p/home-ops")
	account, _ := file.Account(configfile.ForgeGitHub, "onedr0p")
	// Models of a provider that the configuration does not list have the
	// default two slots. The review waited for the third model before a
	// change of the configuration took that model's agent from it.
	const reviewModel, agentModel, staleModel = "unlisted/next-review", "unlisted/next-agent", "unlisted/next-stale"
	t.Cleanup(func() {
		_, _ = st.App().Exec(context.Background(), `DELETE FROM model_leases WHERE model_key IN ($1, $2, $3)`,
			reviewModel, agentModel, staleModel)
	})
	job := func(state, model string) int64 {
		t.Helper()
		var id int64
		if err := st.App().QueryRow(ctx, `INSERT INTO river_job (kind, queue, args, state, max_attempts, scheduled_at, metadata)
			VALUES ('review', 'review', jsonb_build_object('account_id', $1::text), $2::text::river_job_state, 8,
				now() + interval '5 minutes', jsonb_build_object('slot_wait', $3::text)) RETURNING id`,
			account.ID(), state, model).Scan(&id); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = st.App().Exec(context.Background(), `DELETE FROM river_job WHERE id = $1`, id) })
		return id
	}
	review := job("running", staleModel)
	take := func(model string, jobID int64) *store.Lease {
		t.Helper()
		l, err := st.TakeReviewLease(ctx, account.ID(), model, file.Slots(account, configfile.ModelRef(model)), jobID)
		if err != nil || l == nil {
			t.Fatalf("TakeReviewLease = %v, %v", l, err)
		}
		t.Cleanup(func() { _, _ = l.Release(context.Background()) })
		return l
	}
	// The review holds one of the two slots of each of its models. The
	// agent's model has its other slot held too.
	admitted := admission{lease: take(reviewModel, review), reviewerLeases: map[string]*store.Lease{agentModel: take(agentModel, review)}}
	take(agentModel, -1)
	next, behind, stale := job("scheduled", reviewModel), job("scheduled", agentModel), job("scheduled", staleModel)
	logger := slog.New(slog.DiscardHandler)
	(&Review{Store: st, Logger: logger}).wakeNext(ctx, logger, file, account, admitted, reviewModel, review)
	for id, want := range map[int64]string{next: "available", behind: "scheduled", stale: "available"} {
		var state string
		if err := st.App().QueryRow(ctx, `SELECT state FROM river_job WHERE id = $1`, id).Scan(&state); err != nil || state != want {
			t.Errorf("waiter %d is %s (%v), want %s", id, state, err, want)
		}
	}
	var marked bool
	if err := st.App().QueryRow(ctx, `SELECT metadata ? 'slot_wait' FROM river_job WHERE id = $1`, review).Scan(&marked); err != nil || marked {
		t.Errorf("the admitted review is marked as waiting = %v, %v", marked, err)
	}
}
