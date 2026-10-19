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
	slots := &reviewSlots{worker: w, logger: logger,
		accountID: account.ID(), runID: runID, model: model, jobID: -1, limit: 3, maxParts: 4}
	defer slots.close(context.Background())
	check := func(want int) {
		t.Helper()
		if slots.published != want || len(slots.leases) != want-1 {
			t.Fatalf("grant=%d extras=%d, want %d", slots.published, len(slots.leases), want)
		}
		if err := st.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
			var granted, held int
			if err := tx.QueryRow(ctx, `SELECT part_slots, (SELECT count(*) FROM model_leases
    WHERE account_id=$2 AND model_key=$3 AND job_id=$4 AND expires_at>now()) FROM runner_runs WHERE id=$1`,
				runID, account.ID(), model, int64(-1)).Scan(&granted, &held); err != nil {
				return err
			}
			if granted != want || held != want {
				t.Errorf("database grant=%d leases=%d, want %d", granted, held, want)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := slots.resize(ctx, 4); err != nil {
		t.Fatal(err)
	}
	check(1)
	for _, lease := range rivals {
		if _, err := lease.Release(ctx); err != nil {
			t.Fatal(err)
		}
	}
	rivals = nil
	if err := slots.resize(ctx, 4); err != nil {
		t.Fatal(err)
	}
	check(3)
	slots.runID = "00000000-0000-0000-0000-000000000000"
	if err := slots.resize(ctx, 1); err == nil {
		t.Fatal("publishing to a missing run succeeded")
	}
	slots.runID = runID
	check(3)
	if err := slots.resize(ctx, 1); err != nil {
		t.Fatal(err)
	}
	check(1)
	if err := slots.resize(ctx, 4); err != nil {
		t.Fatal(err)
	}
	check(3)
	if err := st.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
		_, err := store.ReportRemainingParts(ctx, tx, runID, 1)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	stop := slots.watch(ctx)
	defer stop()
	waitPartGrant(t, ctx, st, account.ID(), runID, 1)
	stop()
	check(1)
	if err := slots.resize(ctx, 4); err != nil {
		t.Fatal(err)
	}
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

func waitPartGrant(t *testing.T, ctx context.Context, st *store.Store, accountID, runID string, want int) {
	t.Helper()
	waitCtx, stopWait := context.WithTimeout(ctx, 10*time.Second)
	defer stopWait()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		var granted int
		if err := st.WithAccount(waitCtx, accountID, func(tx pgx.Tx) error {
			return tx.QueryRow(waitCtx, `SELECT part_slots FROM runner_runs WHERE id=$1`, runID).Scan(&granted)
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
