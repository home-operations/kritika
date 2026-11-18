//go:build integration

package worker

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/configfile/configfiletest"
	"github.com/home-operations/kritika/internal/review"
	"github.com/home-operations/kritika/internal/runspec"
	"github.com/home-operations/kritika/internal/store"
	"github.com/home-operations/kritika/internal/store/storetest"
)

func TestReviewSlotsResizeAndCancel(t *testing.T) {
	st := storetest.Open(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	t.Setenv("TEST_PEM", "pem")
	t.Setenv("TEST_SECRET", "test-provider-key")
	file := configfiletest.Load(t, configYAML)
	if err := st.ApplyConfig(ctx, file); err != nil {
		t.Fatal(err)
	}
	storetest.Repositories(t, st, file, "onedr0p/home-ops")
	account, _ := file.Account(configfile.ForgeGitHub, "onedr0p")
	const model = "test/adaptive-slots"
	var runID string
	if err := st.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO runner_runs (account_id, kind) VALUES ($1, 'review') RETURNING id`, account.ID()).Scan(&runID)
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.WithAccount(context.Background(), account.ID(), func(tx pgx.Tx) error {
			if _, err := tx.Exec(context.Background(), `DELETE FROM model_leases WHERE model_key = $1`, model); err != nil {
				return err
			}
			_, err := tx.Exec(context.Background(), `DELETE FROM runner_runs WHERE id = $1`, runID)
			return err
		}); err != nil {
			t.Error(err)
		}
	})
	take := func(job int64) *store.Lease {
		t.Helper()
		lease, err := st.TakeLease(ctx, account.ID(), model, 3, job)
		if err != nil || lease == nil {
			t.Fatalf("take lease = %v, %v", lease, err)
		}
		return lease
	}
	base := take(-1)
	defer func() { _, _ = base.Release(context.Background()) }()
	rivals := []*store.Lease{take(-2), take(-3)}
	defer func() {
		for _, lease := range rivals {
			_, _ = lease.Release(context.Background())
		}
	}()
	logger := slog.New(slog.DiscardHandler)
	w := &Review{Store: st, Logger: logger}
	pool := &slotPool{model: model, limit: 3, reviewers: []string{review.DefaultReviewer}}
	slots := &reviewSlots{worker: w, logger: logger, accountID: account.ID(), runID: runID, jobID: -1,
		pools: []*slotPool{pool}, estimate: map[string]int{review.DefaultReviewer: 4}}
	defer slots.close(context.Background())
	parts := func(n int) map[string]int { return map[string]int{model: n} }
	// report is the runner's: remaining parts, active of which run. It reads
	// the grant as the runner does.
	var sequence int64
	report := func(remaining, active int) store.PartProgress {
		t.Helper()
		sequence++
		var progress store.PartProgress
		if err := st.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
			if _, err := store.ReportRemainingParts(ctx, tx, runID,
				runspec.PartsReport{Sequence: sequence, Remaining: parts(remaining), Active: parts(active)}); err != nil {
				return err
			}
			var err error
			progress, err = store.ReadPartProgress(ctx, tx, runID)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return progress
	}
	resize := func(progress store.PartProgress) {
		t.Helper()
		if err := slots.resize(ctx, progress); err != nil {
			t.Fatal(err)
		}
	}
	check := func(grant, held int) {
		t.Helper()
		if slots.published[model] != grant || len(pool.leases) != held-1 {
			t.Fatalf("grant=%v extras=%d, want grant %d and %d held", slots.published, len(pool.leases), grant, held)
		}
		if err := st.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
			var granted, leases int
			if err := tx.QueryRow(ctx, `SELECT (part_slots->>$3)::int, (SELECT count(*) FROM model_leases
    WHERE account_id=$2 AND model_key=$3 AND job_id=$4 AND expires_at>now()) FROM runner_runs WHERE id=$1`,
				runID, account.ID(), model, int64(-1)).Scan(&granted, &leases); err != nil {
				return err
			}
			if granted != grant || leases != held {
				t.Errorf("database grant=%d leases=%d, want %d and %d", granted, leases, grant, held)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	resize(store.PartProgress{})
	check(1, 1)
	for _, lease := range rivals {
		if _, err := lease.Release(ctx); err != nil {
			t.Fatal(err)
		}
	}
	rivals = nil
	// A grant of three that fails to publish keeps the slots taken for it.
	// It is then published after all, as when its commit was not
	// acknowledged: the runner reads three, so the grant of one that
	// follows is published again before a slot is let go.
	slots.runID = "00000000-0000-0000-0000-000000000000"
	if err := slots.resize(ctx, store.PartProgress{}); err == nil {
		t.Fatal("publishing to a missing run succeeded")
	}
	slots.runID = runID
	if slots.published != nil || len(pool.leases) != 2 {
		t.Fatalf("after a failed publication: grant=%v extras=%d, want no grant known and 2 extras", slots.published, len(pool.leases))
	}
	if err := st.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
		_, err := store.SetPartSlots(ctx, tx, runID, parts(3))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	resize(report(1, 1))
	check(1, 1)
	resize(report(4, 1))
	check(3, 3)

	yieldToWaiter(t, st, account.ID(), model, func(remaining, active, grant, held int) {
		t.Helper()
		resize(report(remaining, active))
		check(grant, held)
	}, take)

	report(1, 1)
	stop := slots.watch(ctx)
	defer stop()
	waitPartGrant(t, ctx, st, account.ID(), runID, model, 1)
	stop()
	check(1, 1)
	resize(report(4, 1))
	cancel()
	slots.close(ctx)
	slots.close(ctx)
	// Admission's first lease outlives the extra-slot manager.
	if err := st.WithAccount(context.Background(), account.ID(), func(tx pgx.Tx) error {
		var held int
		if err := tx.QueryRow(context.Background(), `SELECT count(*) FROM model_leases
   WHERE account_id=$1 AND model_key=$2 AND job_id=$3 AND expires_at>now()`, account.ID(), model, int64(-1)).Scan(&held); err != nil {
			return err
		}
		if held != 1 {
			t.Errorf("leases after cancellation = %d, want admission's one", held)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// yieldToWaiter has a review wait for its first slot of model while the
// review under test holds all three, with four parts left and three running.
// The grant drops at once, and the waiter gets a slot once the runner has
// read that grant and a part has ended. step makes the runner's report of
// its remaining and active parts, resizes, and checks the grant and the slots
// held.
func yieldToWaiter(
	t *testing.T, st *store.Store, accountID, model string, step func(remaining, active, grant, held int), take func(int64) *store.Lease,
) {
	t.Helper()
	ctx := t.Context()
	var waiter int64
	if err := st.App().QueryRow(ctx, `INSERT INTO river_job (kind, queue, args, state, max_attempts, scheduled_at, metadata)
		VALUES ('review', 'review', jsonb_build_object('account_id', $1::text), 'scheduled', 8, now() + interval '5 minutes',
			jsonb_build_object('slot_wait', $2::text)) RETURNING id`, accountID, model).Scan(&waiter); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = st.App().Exec(context.Background(), `DELETE FROM river_job WHERE id = $1`, waiter) })
	waiterIs := func(want, when string) {
		t.Helper()
		var state string
		if err := st.App().QueryRow(ctx, `SELECT state FROM river_job WHERE id = $1`, waiter).Scan(&state); err != nil || state != want {
			t.Fatalf("waiter is %s %s (%v), want %s", state, when, err, want)
		}
	}
	// The report that the resize reads was made under the grant of three.
	step(4, 3, 2, 3)
	// The runner has read the grant of two, and three parts still run.
	step(4, 3, 2, 3)
	waiterIs("scheduled", "while every slot is in use")
	step(3, 2, 2, 2)
	waiterIs("available", "after a slot was let go")
	// The slot let go is the waiter's: the review takes it back only once
	// the waiter is done with it.
	step(3, 2, 2, 2)
	admitted := take(waiter)
	step(3, 2, 2, 2)
	if _, err := admitted.Release(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := st.App().Exec(ctx, `DELETE FROM river_job WHERE id = $1`, waiter); err != nil {
		t.Fatal(err)
	}
	step(3, 2, 3, 3)
}

func waitPartGrant(t *testing.T, ctx context.Context, st *store.Store, accountID, runID, model string, want int) {
	t.Helper()
	waitCtx, stopWait := context.WithTimeout(ctx, 10*time.Second)
	defer stopWait()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		var granted int
		if err := st.WithAccount(waitCtx, accountID, func(tx pgx.Tx) error {
			return tx.QueryRow(waitCtx, `SELECT (part_slots->>$2)::int FROM runner_runs WHERE id=$1`, runID, model).Scan(&granted)
		}); err != nil {
			t.Fatal(err)
		}
		if granted == want {
			break
		}
		select {
		case <-waitCtx.Done():
			t.Fatal("watch did not release completed work's slots")
		case <-ticker.C:
		}
	}
}
