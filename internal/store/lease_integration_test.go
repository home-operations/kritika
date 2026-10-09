//go:build integration

package store

import (
	"context"
	"testing"
)

// TestReleaseWakesSnoozedReview: letting a slot go wakes the oldest review
// snoozed for one on that model of that account, one per release, and
// leaves every other queued job to its own time.
func TestReleaseWakesSnoozedReview(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, twoAccounts)); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}
	alpha, beta := accountID(t, s, "alpha"), accountID(t, s, "beta")
	const model = "test/wake-model"
	t.Cleanup(func() {
		_, _ = s.owner.Exec(context.Background(), `DELETE FROM model_leases WHERE model_key = $1`, model)
	})
	insert := func(account, state, metadata string) int64 {
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
	waiting := `{"snoozes": 2, "slot_wait": "` + model + `"}`
	// River keeps a snooze shorter than its scheduler interval available
	// with a scheduled_at ahead, as the third waiter is.
	first, second, third := insert(alpha, "scheduled", waiting), insert(alpha, "scheduled", waiting), insert(alpha, "available", waiting)
	others := []struct {
		name, state string
		id          int64
	}{
		{name: "a review of another account", state: "scheduled", id: insert(beta, "scheduled", waiting)},
		{name: "a review waiting on another model", state: "scheduled",
			id: insert(alpha, "scheduled", `{"snoozes": 1, "slot_wait": "test/other-model"}`)},
		{name: "a review snoozed for its settle time", state: "scheduled", id: insert(alpha, "scheduled", `{"snoozes": 1}`)},
		{name: "a review snoozed for its settle time after a slot", state: "scheduled",
			id: insert(alpha, "scheduled", `{"snoozes": 2, "slot_wait": null}`)},
		{name: "a review waiting out a failed attempt", state: "retryable", id: insert(alpha, "retryable", waiting)},
		{name: "a review retried soon after a failed attempt", state: "available",
			id: insert(alpha, "available", `{"snoozes": 1, "slot_wait": null}`)},
	}
	jobState := func(id int64) (state string, due bool) {
		t.Helper()
		if err := s.owner.QueryRow(ctx, `SELECT state, scheduled_at <= now() FROM river_job WHERE id = $1`, id).Scan(&state, &due); err != nil {
			t.Fatal(err)
		}
		return state, due
	}
	release := func() int64 {
		t.Helper()
		l, err := s.TakeLease(ctx, alpha, model, 1, 7)
		if err != nil || l == nil {
			t.Fatalf("TakeLease = %v, %v", l, err)
		}
		woken, err := l.Release(ctx)
		if err != nil {
			t.Fatalf("Release: %v", err)
		}
		return woken
	}
	for i, want := range []int64{first, second, third, 0} {
		if got := release(); got != want {
			t.Fatalf("release %d woke job %d, want %d", i+1, got, want)
		}
	}
	for _, id := range []int64{first, second, third} {
		if state, due := jobState(id); state != "available" || !due {
			t.Errorf("woken job %d is %s and due = %v, want available now", id, state, due)
		}
	}
	for _, o := range others {
		if state, due := jobState(o.id); state != o.state || due {
			t.Errorf("%s is %s and due = %v, want %s at its own time", o.name, state, due, o.state)
		}
	}
}

// TestWakeIfSlotFree: a review snoozed for a model slot is woken when a
// slot of the model is free, and only that review; it is left asleep while
// every slot is held, as is one not snoozed for a slot.
func TestWakeIfSlotFree(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, twoAccounts)); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}
	alpha := accountID(t, s, "alpha")
	const model = "test/wake-free-model"
	t.Cleanup(func() {
		_, _ = s.owner.Exec(context.Background(), `DELETE FROM model_leases WHERE model_key = $1`, model)
	})
	insert := func(metadata string) int64 {
		t.Helper()
		var id int64
		if err := s.owner.QueryRow(ctx, `INSERT INTO river_job (kind, queue, args, state, max_attempts, scheduled_at, metadata)
			VALUES ('review', 'review', jsonb_build_object('account_id', $1::text), 'scheduled', 8,
				now() + interval '5 minutes', $2::jsonb) RETURNING id`, alpha, metadata).Scan(&id); err != nil {
			t.Fatalf("insert river job: %v", err)
		}
		t.Cleanup(func() { _, _ = s.owner.Exec(context.Background(), `DELETE FROM river_job WHERE id = $1`, id) })
		return id
	}
	jobState := func(id int64) (state string, due bool) {
		t.Helper()
		if err := s.owner.QueryRow(ctx, `SELECT state, scheduled_at <= now() FROM river_job WHERE id = $1`, id).Scan(&state, &due); err != nil {
			t.Fatal(err)
		}
		return state, due
	}
	waiting := `{"snoozes": 1, "slot_wait": "` + model + `"}`
	older, waiter, settling := insert(waiting), insert(waiting), insert(`{"snoozes": 1}`)
	l, err := s.TakeLease(ctx, alpha, model, 1, 7)
	if err != nil || l == nil {
		t.Fatalf("TakeLease = %v, %v", l, err)
	}
	// The only slot is held.
	if woken, err := s.WakeIfSlotFree(ctx, alpha, model, 1, waiter); err != nil || woken {
		t.Fatalf("WakeIfSlotFree with every slot held = %v, %v, want false", woken, err)
	}
	// One of two slots is free, but the job named is not waiting for one.
	if woken, err := s.WakeIfSlotFree(ctx, alpha, model, 2, settling); err != nil || woken {
		t.Fatalf("WakeIfSlotFree of a review not snoozed for a slot = %v, %v, want false", woken, err)
	}
	if woken, err := s.WakeIfSlotFree(ctx, alpha, model, 2, waiter); err != nil || !woken {
		t.Fatalf("WakeIfSlotFree with a slot free = %v, %v, want true", woken, err)
	}
	if state, due := jobState(waiter); state != "available" || !due {
		t.Errorf("woken job is %s and due = %v, want available now", state, due)
	}
	for name, id := range map[string]int64{"an older waiter": older, "a review snoozed for its settle time": settling} {
		if state, due := jobState(id); state != "scheduled" || due {
			t.Errorf("%s is %s and due = %v, want scheduled at its own time", name, state, due)
		}
	}
	// Waking the same job again finds it awake already.
	if woken, err := s.WakeIfSlotFree(ctx, alpha, model, 2, waiter); err != nil || woken {
		t.Fatalf("WakeIfSlotFree of a woken review = %v, %v, want false", woken, err)
	}
	if woken, err := l.Release(ctx); err != nil || woken != older {
		t.Fatalf("Release woke job %d, %v, want the older waiter %d", woken, err, older)
	}
}
