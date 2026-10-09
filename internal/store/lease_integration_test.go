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
