//go:build integration

package store

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5"
	"golang.org/x/sync/errgroup"

	"github.com/home-operations/kritika/internal/runspec"
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
	parts := func(n int) map[string]int { return map[string]int{"default": n} }
	publish := func(slots int) PartProgress {
		t.Helper()
		var before PartProgress
		if err := s.WithAccount(ctx, account, func(tx pgx.Tx) (err error) {
			before, err = SetPartSlots(ctx, tx, runID, parts(slots))
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return before
	}
	if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		progress, err := ReadPartProgress(ctx, tx, runID)
		if progress.Remaining != nil || progress.Active != nil || progress.Seen != nil {
			t.Errorf("initial progress = %+v", progress)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if before := publish(3); before.Seen != nil {
		t.Errorf("progress before the first report = %+v", before)
	}
	token, err := s.MintRunnerToken(ctx, account, runID)
	if err != nil {
		t.Fatal(err)
	}
	report := func(sequence int64, remaining int) error {
		return s.WithRunner(ctx, token, func(tx pgx.Tx, g RunnerGrant) error {
			slots, err := ReportRemainingParts(ctx, tx, g.RunID,
				runspec.PartsReport{Sequence: sequence, Remaining: parts(remaining), Active: parts(min(remaining, 3))})
			if err == nil && (len(slots) != 1 || slots["default"] != 3) {
				t.Errorf("slots = %v, want default: 3", slots)
			}
			return err
		})
	}
	for i, remaining := range []int{4, 2, 0} {
		if err := report(int64(i+1), remaining); err != nil {
			t.Fatal(err)
		}
	}
	// A report that arrives after a later one, or again, is refused: the
	// runner went on with another answer than the slots it would read.
	for _, sequence := range []int64{2, 3} {
		if err := report(sequence, 4); !errors.Is(err, ErrStaleReport) {
			t.Errorf("report %d after report 3 = %v, want ErrStaleReport", sequence, err)
		}
	}
	// A new grant is returned with the report made before it, which read
	// the grant before.
	want := PartProgress{Remaining: parts(0), Active: parts(0), Seen: parts(3)}
	if before := publish(2); !reflect.DeepEqual(before, want) {
		t.Errorf("progress as the grant changed = %+v, want %+v", before, want)
	}
	if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		progress, err := ReadPartProgress(ctx, tx, runID)
		if err == nil && !reflect.DeepEqual(progress, want) {
			t.Errorf("progress = %+v, want %+v", progress, want)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		_, err := SetPartSlots(ctx, tx, "00000000-0000-0000-0000-000000000000", parts(1))
		return err
	}); !errors.Is(err, ErrNotFound) {
		t.Errorf("publishing to a missing run = %v, want ErrNotFound", err)
	}
}

// TestSlotsOwed: the reviews that wait for their first slot of a model are
// owed a slot each, less the slots that are free. A review that waits for
// another model or holds a slot already is owed none, and neither is the
// review that asks.
func TestSlotsOwed(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, twoAccounts)); err != nil {
		t.Fatal(err)
	}
	listRepos(t, s, "alpha", "alpha/one", "alpha/two")
	alpha, beta := accountID(t, s, "alpha"), accountID(t, s, "beta")
	const model = "test/slots-owed"
	t.Cleanup(func() {
		_, _ = s.owner.Exec(context.Background(), `DELETE FROM model_leases WHERE model_key = $1`, model)
	})
	waiter := func(account, waitingModel string) int64 {
		t.Helper()
		var id int64
		if err := s.owner.QueryRow(ctx, `INSERT INTO river_job (kind, queue, args, state, max_attempts, metadata)
    VALUES ('review', 'review', jsonb_build_object('account_id', $1::text), 'scheduled', 8,
    jsonb_build_object('slot_wait', $2::text)) RETURNING id`, account, waitingModel).Scan(&id); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = s.owner.Exec(context.Background(), `DELETE FROM river_job WHERE id = $1`, id) })
		return id
	}
	const holder = -10
	owed := func(want int) {
		t.Helper()
		if got, err := s.SlotsOwed(ctx, alpha, model, 3, holder); err != nil || got != want {
			t.Fatalf("SlotsOwed = %d, %v, want %d", got, err, want)
		}
	}
	var leases []*Lease
	for range 3 {
		lease, err := s.TakeLease(ctx, alpha, model, 3, holder)
		if err != nil || lease == nil {
			t.Fatalf("take lease = %v, %v", lease, err)
		}
		leases = append(leases, lease)
	}
	defer func() {
		for _, lease := range leases {
			_, _ = lease.Release(context.Background())
		}
	}()
	owed(0)
	waiter(beta, model)
	waiter(alpha, "test/another-model")
	owed(0)
	first, second := waiter(alpha, model), waiter(alpha, model)
	owed(2)
	if got, err := s.SlotsOwed(ctx, alpha, model, 3, first); err != nil || got != 1 {
		t.Fatalf("SlotsOwed to the others of a waiter = %d, %v, want 1", got, err)
	}
	// A slot let go is one of the two that the waiters are short of.
	if _, err := leases[2].Release(ctx); err != nil {
		t.Fatal(err)
	}
	leases = leases[:2]
	owed(1)
	admitted, err := s.TakeLease(ctx, alpha, model, 3, first)
	if err != nil || admitted == nil {
		t.Fatalf("waiter admission = %v, %v", admitted, err)
	}
	leases = append(leases, admitted)
	owed(1)
	if _, err := s.owner.Exec(ctx, `DELETE FROM river_job WHERE id = $1`, second); err != nil {
		t.Fatal(err)
	}
	owed(0)
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
