//go:build integration

package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/home-operations/kritika/internal/configfile"
)

// sharedFixture is two accounts and a function that queues a review of one
// of them, in state and with metadata.
func sharedFixture(t *testing.T, model string) (s *Store, alpha, beta string, insert func(account, state, metadata string) int64) {
	t.Helper()
	s = openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, twoAccounts)); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}
	listRepos(t, s, "alpha", "alpha/one", "alpha/two")
	alpha, beta = accountID(t, s, "alpha"), accountID(t, s, "beta")
	t.Cleanup(func() {
		_, _ = s.owner.Exec(context.Background(), `DELETE FROM model_leases WHERE model_key = $1`, model)
	})
	insert = func(account, state, metadata string) int64 {
		t.Helper()
		var id int64
		if err := s.owner.QueryRow(ctx, `INSERT INTO river_job (kind, queue, args, state, max_attempts, scheduled_at, metadata)
			VALUES ('review', 'review', jsonb_build_object('account_id', $1::text), $2::text::river_job_state, 8,
				now() + interval '5 minutes', $3::jsonb) RETURNING id`, account, state, metadata).Scan(&id); err != nil {
			t.Fatalf("insert river job: %v", err)
		}
		t.Cleanup(func() { _, _ = s.owner.Exec(context.Background(), `DELETE FROM river_job WHERE id = $1`, id) })
		return id
	}
	return s, alpha, beta, insert
}

// TestSharedSlotsAreOnePool: every account takes the shared slots of a model
// from one pool, where each account's own slots of the same key are apart
// from them and from each other's.
func TestSharedSlotsAreOnePool(t *testing.T) {
	const model = "test/shared-pool"
	s, alpha, beta, _ := sharedFixture(t, model)
	ctx := t.Context()
	take := func(account string, slots configfile.Slots, job int64, want int) *Lease {
		t.Helper()
		l, err := s.TakeLease(ctx, account, model, slots, job)
		if err != nil {
			t.Fatalf("TakeLease: %v", err)
		}
		got := 0
		if l != nil {
			got = l.slot
			t.Cleanup(func() { _, _ = l.Release(context.Background()) })
		}
		if got != want {
			t.Fatalf("job %d took slot %d, want %d", job, got, want)
		}
		return l
	}
	first := take(alpha, shared(2), 1, 1)
	take(beta, shared(2), 2, 2)
	take(alpha, shared(2), 3, 0)
	take(beta, shared(2), 4, 0)
	for _, account := range []string{alpha, beta} {
		if free, err := s.SlotFree(ctx, account, model, shared(2)); err != nil || free {
			t.Fatalf("SlotFree = %v, %v with both shared slots held", free, err)
		}
	}
	// The accounts' own slots of the key are free, each account's apart.
	take(alpha, own(2), 5, 1)
	take(beta, own(2), 6, 1)
	// A slot that one account lets go is the other's to take.
	if _, err := first.Release(ctx); err != nil {
		t.Fatalf("Release: %v", err)
	}
	take(beta, shared(2), 7, 1)
	var holders int
	if err := s.WithAccount(ctx, alpha, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM model_leases WHERE scope = '' AND model_key = $1 AND account_id = $2`,
			model, beta).Scan(&holders)
	}); err != nil || holders != 2 {
		t.Fatalf("alpha counts %d shared slots held by beta, %v, want 2", holders, err)
	}
}

// TestSharedReleaseWakesTheAccountWithFewestSlots: a shared slot let go wakes
// a waiting review of the account that holds the fewest slots of the model,
// before an older one of an account that holds more, and the oldest among
// accounts that hold as many.
func TestSharedReleaseWakesTheAccountWithFewestSlots(t *testing.T) {
	const model = "test/shared-wake"
	s, alpha, beta, insert := sharedFixture(t, model)
	ctx := t.Context()
	waiting := `{"snoozes": 1, "slot_wait": "` + model + `"}`
	take := func(account string, job int64) *Lease {
		t.Helper()
		l, err := s.TakeLease(ctx, account, model, shared(3), job)
		if err != nil || l == nil {
			t.Fatalf("TakeLease = %v, %v", l, err)
		}
		t.Cleanup(func() { _, _ = l.Release(context.Background()) })
		return l
	}
	release := func(l *Lease, want int64, why string) {
		t.Helper()
		if got, err := l.Release(ctx); err != nil || got != want {
			t.Fatalf("release woke job %d, %v, want %d: %s", got, err, want, why)
		}
	}
	held := []*Lease{take(alpha, -1), take(alpha, -2), take(alpha, -3)}
	older, newer := insert(alpha, "scheduled", waiting), insert(alpha, "scheduled", waiting)
	other, later := insert(beta, "scheduled", waiting), insert(beta, "scheduled", waiting)
	insert(beta, "scheduled", `{"snoozes": 1, "slot_wait": "test/another-model"}`)
	release(held[0], other, "beta holds no slot and alpha two")
	// A woken review takes the slot. It is due now, so no release wakes it
	// again.
	admitted := take(beta, other)
	release(held[1], older, "the accounts hold a slot each, and alpha's review waited longest")
	take(alpha, older)
	release(admitted, later, "beta holds no slot and alpha two, though a review of alpha waited longer")
	take(beta, later)
	// WakeIfSlotFree wakes the next in line, of whichever account, only
	// while a slot is free.
	if woken, err := s.WakeIfSlotFree(ctx, beta, model, shared(3), 0); err != nil || woken {
		t.Fatalf("WakeIfSlotFree = %v, %v with every slot held", woken, err)
	}
	if woken, err := s.WakeIfSlotFree(ctx, beta, model, shared(4), 0); err != nil || !woken {
		t.Fatalf("WakeIfSlotFree = %v, %v with a slot free and a review waiting", woken, err)
	}
	var state string
	if err := s.owner.QueryRow(ctx, `SELECT state FROM river_job WHERE id = $1 AND scheduled_at <= now()`, newer).Scan(&state); err != nil ||
		state != "available" {
		t.Fatalf("alpha's waiting review is %q, %v, want available now", state, err)
	}
}

// TestSharedExtraSlotsYieldToEveryAccount: a review takes no extra shared
// slot while a review of another account waits for its first, and owes that
// review a slot. Slots of its account's own are owed to its own reviews
// alone.
func TestSharedExtraSlotsYieldToEveryAccount(t *testing.T) {
	const model = "test/shared-extra"
	s, alpha, beta, insert := sharedFixture(t, model)
	ctx := t.Context()
	waiter := insert(beta, "scheduled", `{"slot_wait": "`+model+`"}`)
	extra := func(slots configfile.Slots) *Lease {
		t.Helper()
		l, err := s.TakeExtraLease(ctx, alpha, model, slots, -10)
		if err != nil {
			t.Fatalf("TakeExtraLease: %v", err)
		}
		if l != nil {
			t.Cleanup(func() { _, _ = l.Release(context.Background()) })
		}
		return l
	}
	if extra(shared(2)) != nil {
		t.Fatal("an extra shared slot was taken while another account's review waits for its first")
	}
	if extra(own(2)) == nil {
		t.Fatal("an extra slot of the account's own was not taken, though only another account's review waits")
	}
	first, err := s.TakeLease(ctx, alpha, model, shared(1), -10)
	if err != nil || first == nil {
		t.Fatalf("TakeLease = %v, %v", first, err)
	}
	t.Cleanup(func() { _, _ = first.Release(context.Background()) })
	if owed, err := s.SlotsOwed(ctx, alpha, model, shared(1), -10); err != nil || owed != 1 {
		t.Fatalf("SlotsOwed of the shared slots = %d, %v, want 1", owed, err)
	}
	if owed, err := s.SlotsOwed(ctx, alpha, model, own(1), -10); err != nil || owed != 0 {
		t.Fatalf("SlotsOwed of the account's own slots = %d, %v, want 0", owed, err)
	}
	// Once the other account's review has its slot, the extra one is free to
	// take.
	admitted, err := s.TakeLease(ctx, beta, model, shared(3), waiter)
	if err != nil || admitted == nil {
		t.Fatalf("waiter admission = %v, %v", admitted, err)
	}
	t.Cleanup(func() { _, _ = admitted.Release(context.Background()) })
	if extra(shared(3)) == nil {
		t.Fatal("an extra shared slot was not taken once no review waits")
	}
}

// TestSharedIndexUpdateComesBeforeEveryBuild: while an index update of one
// account waits for a shared slot, no build of another account takes one.
func TestSharedIndexUpdateComesBeforeEveryBuild(t *testing.T) {
	const model = "embed:test/shared-index-wait"
	s, alpha, beta, _ := sharedFixture(t, model)
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	// The update can take only the first slot, which this lease holds.
	held, err := s.TakeLease(ctx, alpha, model, shared(1), 1)
	if err != nil || held == nil {
		t.Fatalf("TakeLease = %v, %v", held, err)
	}
	t.Cleanup(func() { _, _ = held.Release(context.Background()) })
	var updated *Lease
	var updateErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		updated, updateErr = s.AcquireIndexLease(ctx, beta, model, shared(1), 2, true)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
		if updated != nil {
			_, _ = updated.Release(context.Background())
		}
	})
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	for {
		var waits bool
		if err := s.owner.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_locks l, (SELECT `+indexWaitLock+` AS k) w
			WHERE l.locktype = 'advisory' AND l.mode = 'ShareLock' AND l.granted AND l.objsubid = 1
				AND l.classid::bigint = (w.k >> 32) & 4294967295 AND l.objid::bigint = w.k & 4294967295)`, sharedScope, model).Scan(&waits); err != nil {
			t.Fatalf("the update's wait not seen: %v", err)
		}
		if waits {
			break
		}
		select {
		case <-done:
			t.Fatalf("the update ended its wait with %v, %v while the slot it can take is held", updated, updateErr)
		case <-ctx.Done():
			t.Fatalf("the update's wait not seen: %v", ctx.Err())
		case <-poll.C:
		}
	}
	build, err := s.TakeIndexBuildLease(ctx, alpha, model, shared(2), 3)
	if err != nil || build != nil {
		if build != nil {
			_, _ = build.Release(ctx)
		}
		t.Fatalf("a build took a shared slot while another account's update waits: %v, %v", build, err)
	}
	if _, err := held.Release(ctx); err != nil {
		t.Fatalf("Release: %v", err)
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatalf("the update did not take the slot let go: %v", ctx.Err())
	}
	if updateErr != nil || updated == nil {
		t.Fatalf("AcquireIndexLease = %v, %v", updated, updateErr)
	}
}

// TestSharedSlotsOfAnotherAccount: an account reads a shared slot that
// another account holds, and can neither take it, let it go, delete it nor
// write the other account as a slot's holder. It takes the slot once its
// holder lets it go.
func TestSharedSlotsOfAnotherAccount(t *testing.T) {
	const model = "test/shared-foreign"
	s, alpha, beta, _ := sharedFixture(t, model)
	ctx := t.Context()
	held, err := s.TakeLease(ctx, beta, model, shared(1), 7)
	if err != nil || held == nil {
		t.Fatalf("TakeLease = %v, %v", held, err)
	}
	t.Cleanup(func() { _, _ = held.Release(context.Background()) })
	for name, stmt := range map[string]string{
		"take":   `UPDATE model_leases SET job_id = 8, account_id = $2 WHERE scope = '' AND model_key = $1`,
		"let go": `UPDATE model_leases SET job_id = NULL, account_id = NULL, expires_at = NULL WHERE scope = '' AND model_key = $1 AND $2::uuid IS NOT NULL`,
		"delete": `DELETE FROM model_leases WHERE scope = '' AND model_key = $1 AND $2::uuid IS NOT NULL`,
	} {
		var changed int64
		if err := s.WithAccount(ctx, alpha, func(tx pgx.Tx) error {
			tag, err := tx.Exec(ctx, stmt, model, alpha)
			changed = tag.RowsAffected()
			return err
		}); err != nil || changed != 0 {
			t.Fatalf("%s: changed %d rows, %v, want none", name, changed, err)
		}
	}
	err = s.WithAccount(ctx, alpha, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO model_leases (scope, model_key, slot, account_id) VALUES ('', $1, 2, $2)`, model, beta)
		return err
	})
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); !ok || pgErr.Code != "42501" {
		t.Fatalf("insert of a shared slot held for another account: err = %v, want the WITH CHECK policy's refusal", err)
	}
	if free, err := s.SlotFree(ctx, alpha, model, shared(1)); err != nil || free {
		t.Fatalf("SlotFree = %v, %v with the slot held by another account", free, err)
	}
	if _, err := held.Release(ctx); err != nil {
		t.Fatal(err)
	}
	taken, err := s.TakeLease(ctx, alpha, model, shared(1), 8)
	if err != nil || taken == nil {
		t.Fatalf("TakeLease of the slot let go = %v, %v", taken, err)
	}
	t.Cleanup(func() { _, _ = taken.Release(context.Background()) })
}
