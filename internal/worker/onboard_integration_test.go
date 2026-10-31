//go:build integration

package worker

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/configfile/configfiletest"
	"github.com/home-operations/kritika/internal/store"
	"github.com/home-operations/kritika/internal/store/storetest"
)

// TestOnboarderKeepsToItsWindow makes sure that the feeder queues onboarding
// jobs up to its window and no further, and fills a place that a finished job
// leaves.
func TestOnboarderKeepsToItsWindow(t *testing.T) {
	ctx := t.Context()
	logger := slog.New(slog.DiscardHandler)
	st := storetest.Open(t)
	t.Setenv("TEST_PEM", "pem")
	t.Setenv("TEST_SECRET", "s3cret")
	// An account of its own: the feeder offers only repositories of the
	// accounts that the configuration serves. So the repositories that other
	// suites turned on in the shared database stay out of the count.
	file, err := configfiletest.Parse(t, strings.ReplaceAll(configYAML, "onedr0p", "onboard-window"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.ApplyConfig(ctx, file); err != nil {
		t.Fatal(err)
	}
	// The repositories have no dashboard choice, so the configuration's
	// enabled decides whether they run.
	account, _ := file.Account(configfile.ForgeGitHub, "onboard-window")
	if err := st.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
		for _, name := range []string{"onboard-window/a", "onboard-window/b", "onboard-window/c"} {
			id, _, err := store.EnsureRepository(ctx, tx, account.ID(), store.ReachedRepository{FullName: name})
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE repositories SET turned_on = NULL, turned_at = NULL WHERE id = $1`, id); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// The suites share one database: count from what is already there,
	// and leave none of this test's jobs behind.
	var lastJob int64
	if err := st.App().QueryRow(ctx, `SELECT coalesce(max(id), 0) FROM river_job`).Scan(&lastJob); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = st.App().Exec(context.Background(), `DELETE FROM river_job WHERE id > $1`, lastJob) })
	queue, err := river.NewClient(riverpgxv5.New(st.App()), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	before, err := st.OnboardingInFlight(ctx)
	if err != nil {
		t.Fatal(err)
	}
	window := before + 2
	// running is the test's configuration with the window, and emb as its
	// embedder: the feeder offers only repositories whose account it runs.
	running := func(emb *configfile.Embedding) *configfile.File {
		f := *file
		f.Run.OnboardWindow = window
		f.Embedding = emb
		return &f
	}
	current := configfile.NewCurrent(running(nil))
	o := &Onboarder{Store: st, Queue: queue, Current: current, Logger: logger}
	queued := func() (jobs, repos int) {
		t.Helper()
		if err := st.App().QueryRow(ctx, `SELECT count(*), count(DISTINCT args->>'repository_id') FROM river_job
			WHERE id > $1 AND kind = 'index' AND args->>'trigger' = 'onboard'`, lastJob).Scan(&jobs, &repos); err != nil {
			t.Fatal(err)
		}
		return jobs, repos
	}
	if err := o.Offer(ctx); err != nil {
		t.Fatalf("Offer: %v", err)
	}
	if jobs, _ := queued(); jobs != 0 {
		t.Fatalf("queued %d jobs with no embedder, want none", jobs)
	}
	current.Set(running(&configfile.Embedding{Model: "m", Dims: 8}))
	for range 2 {
		if err := o.Offer(ctx); err != nil {
			t.Fatalf("Offer: %v", err)
		}
		if n, err := st.OnboardingInFlight(ctx); err != nil || n != window {
			t.Fatalf("in flight = %d, %v; want the window, %d", n, err, window)
		}
	}
	if jobs, repos := queued(); jobs != 2 || repos != 2 {
		t.Fatalf("queued %d jobs for %d repositories, want 2 for 2", jobs, repos)
	}
	if _, err := st.App().Exec(ctx, `UPDATE river_job SET state = 'completed', finalized_at = now()
		WHERE id = (SELECT min(id) FROM river_job WHERE id > $1)`, lastJob); err != nil {
		t.Fatal(err)
	}
	if err := o.Offer(ctx); err != nil {
		t.Fatalf("Offer: %v", err)
	}
	if jobs, repos := queued(); jobs != 3 || repos != 3 {
		t.Fatalf("after one finished: queued %d jobs for %d repositories, want 3 for 3", jobs, repos)
	}

	// With repositories off by default, a place that frees up stays empty.
	off := running(&configfile.Embedding{Model: "m", Dims: 8})
	off.Defaults.Enabled = new(false)
	current.Set(off)
	if _, err := st.App().Exec(ctx, `UPDATE river_job SET state = 'completed', finalized_at = now()
		WHERE id = (SELECT min(id) FROM river_job WHERE id > $1 AND state <> 'completed')`, lastJob); err != nil {
		t.Fatal(err)
	}
	if err := o.Offer(ctx); err != nil {
		t.Fatalf("Offer: %v", err)
	}
	if jobs, _ := queued(); jobs != 3 {
		t.Fatalf("with repositories off: queued %d jobs, want still 3", jobs)
	}
}
