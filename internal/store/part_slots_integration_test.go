//go:build integration

package store

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"golang.org/x/sync/errgroup"
)

func TestPartSlots(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, twoAccounts)); err != nil {
		t.Fatal(err)
	}
	listRepos(t, s, "alpha", "alpha/one", "alpha/two")
	account := accountID(t, s, "alpha")
	var runID string
	if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO runner_runs (account_id, kind) VALUES ($1, 'review') RETURNING id`, account).Scan(&runID)
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = s.owner.Exec(context.Background(), `DELETE FROM runner_runs WHERE id = $1`, runID) })
	if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		remaining, err := RemainingParts(ctx, tx, runID)
		if remaining != nil {
			t.Errorf("initial remaining = %v", remaining)
		}
		if err != nil {
			return err
		}
		return SetPartSlots(ctx, tx, runID, 3)
	}); err != nil {
		t.Fatal(err)
	}
	token, err := s.MintRunnerToken(ctx, account, runID)
	if err != nil {
		t.Fatal(err)
	}
	for _, remaining := range []int{4, 2, 0} {
		if err := s.WithRunner(ctx, token, func(tx pgx.Tx, g RunnerGrant) error {
			slots, err := ReportRemainingParts(ctx, tx, g.RunID, remaining)
			if err == nil && slots != 3 {
				t.Errorf("slots = %d, want 3", slots)
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		remaining, err := RemainingParts(ctx, tx, runID)
		if err == nil && (remaining == nil || *remaining != 0) {
			t.Errorf("remaining = %v, want 0", remaining)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestExtraLeaseConcurrentLimit(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, twoAccounts)); err != nil {
		t.Fatal(err)
	}
	listRepos(t, s, "alpha", "alpha/one", "alpha/two")
	account := accountID(t, s, "alpha")
	const model = "test/concurrent-extras"
	t.Cleanup(func() {
		_, _ = s.owner.Exec(context.Background(), `DELETE FROM model_leases WHERE model_key = $1`, model)
	})
	base, err := s.TakeLease(ctx, account, model, 3, -100)
	if err != nil || base == nil {
		t.Fatalf("admission = %v, %v", base, err)
	}
	defer func() { _, _ = base.Release(context.Background()) }()
	leases := make(chan *Lease, 12)
	var g errgroup.Group
	for i := range 12 {
		g.Go(func() error {
			lease, err := s.TakeExtraLease(ctx, account, model, 3, int64(-200-i))
			if lease != nil {
				leases <- lease
			}
			return err
		})
	}
	err = g.Wait()
	close(leases)
	count := len(leases)
	for lease := range leases {
		if _, err := lease.Release(ctx); err != nil {
			t.Error(err)
		}
	}
	if err != nil || count < 1 || count > 2 {
		t.Fatalf("concurrent extra leases = %d, error = %v; want 1-2 with one admission slot held", count, err)
	}
}

func TestExtraLeaseWaiterPriority(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, twoAccounts)); err != nil {
		t.Fatal(err)
	}
	listRepos(t, s, "alpha", "alpha/one", "alpha/two")
	alpha, beta := accountID(t, s, "alpha"), accountID(t, s, "beta")
	const model = "test/extra-priority"
	t.Cleanup(func() {
		_, _ = s.owner.Exec(context.Background(), `DELETE FROM model_leases WHERE model_key = $1`, model)
	})
	for _, tt := range []struct {
		name, state, account, waitingModel string
		blocked                            bool
	}{
		{"snoozed", "scheduled", alpha, model, true},
		{"awakened", "available", alpha, model, true},
		{"admitting", "running", alpha, model, true},
		{"another account", "scheduled", beta, model, false},
		{"another model", "scheduled", alpha, "other", false},
		{"retry", "retryable", alpha, model, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var id int64
			if err := s.owner.QueryRow(ctx, `INSERT INTO river_job (kind, queue, args, state, max_attempts, metadata)
    VALUES ('review', 'review', jsonb_build_object('account_id', $1::text), $2::river_job_state, 8,
    jsonb_build_object('slot_wait', $3::text)) RETURNING id`, tt.account, tt.state, tt.waitingModel).Scan(&id); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _, _ = s.owner.Exec(context.Background(), `DELETE FROM river_job WHERE id = $1`, id) })
			lease, err := s.TakeExtraLease(ctx, alpha, model, 2, -10)
			if err != nil {
				t.Fatal(err)
			}
			if lease != nil {
				defer func() { _, _ = lease.Release(ctx) }()
			}
			if (lease == nil) != tt.blocked {
				t.Fatalf("blocked = %v, want %v", lease == nil, tt.blocked)
			}
			if !tt.blocked {
				return
			}
			first, err := s.TakeLease(ctx, alpha, model, 2, id)
			if err != nil || first == nil {
				t.Fatalf("waiter admission = %v, %v", first, err)
			}
			defer func() { _, _ = first.Release(ctx) }()
			extra, err := s.TakeExtraLease(ctx, alpha, model, 2, -10)
			if err != nil || extra == nil {
				t.Fatalf("extra after waiter admission = %v, %v", extra, err)
			}
			defer func() { _, _ = extra.Release(ctx) }()
			over, err := s.TakeExtraLease(ctx, alpha, model, 2, -10)
			if over != nil {
				defer func() { _, _ = over.Release(ctx) }()
			}
			if err != nil || over != nil {
				t.Fatalf("over limit = %v, %v", over, err)
			}
		})
	}
}
