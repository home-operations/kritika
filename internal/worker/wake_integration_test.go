//go:build integration

package worker

import (
	"context"
	"log/slog"
	"testing"

	"github.com/riverqueue/river/rivertype"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/configfile/configfiletest"
	"github.com/home-operations/kritika/internal/store/storetest"
)

// TestSlotWakeChecksSnoozedReview: a review saved as snoozed for a model
// slot is woken when a slot is free by then, and left to its snooze while
// every slot is held; a job snoozed for anything else, of another kind, or
// of an account that is not running, is left alone.
func TestSlotWakeChecksSnoozedReview(t *testing.T) {
	ctx := t.Context()
	appStore := storetest.Open(t)
	t.Setenv("TEST_PEM", "pem")
	t.Setenv("TEST_SECRET", "test-provider-key")
	file := configfiletest.Load(t, configYAML)
	if err := appStore.ApplyConfig(ctx, file); err != nil {
		t.Fatal(err)
	}
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
		{name: "a review snoozed for its settle time is left alone", kind: "review", account: account.ID(), metadata: `{"snoozes": 1}`},
		{name: "a review snoozed for its settle time after a slot is left alone", kind: "review", account: account.ID(), metadata: `{"snoozes": 2, "slot_wait": null}`},
		{name: "a job of another kind is left alone", kind: "index", account: account.ID(), metadata: waiting},
		{name: "a review of an account that is not running is left alone", kind: "review", account: "github:nobody", metadata: waiting},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.held {
				// The account's concurrency is 1, so one lease holds every slot.
				l, err := appStore.TakeLease(ctx, account.ID(), model, 1, -1)
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
}
