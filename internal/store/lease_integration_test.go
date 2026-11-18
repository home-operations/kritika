//go:build integration

package store

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"golang.org/x/sync/errgroup"
)

// TestReleaseWakesSnoozedReview: letting a slot go wakes the oldest review
// snoozed for one on that model of that account, one per release. It leaves
// every other queued job to its own time.
func TestReleaseWakesSnoozedReview(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, twoAccounts)); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}
	listRepos(t, s, "alpha", "alpha/one", "alpha/two")
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

// TestReleaseWakesReadyBeforeDraft: a slot let go wakes the oldest review of
// a pull request that is ready before any review of a draft, and a draft's
// once it is marked ready as any other's. A draft's review is neither woken
// nor given a free slot while a review of a ready pull request waits for its
// first.
func TestReleaseWakesReadyBeforeDraft(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, twoAccounts)); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}
	listRepos(t, s, "alpha", "alpha/one", "alpha/two")
	alpha := accountID(t, s, "alpha")
	const model = "test/wake-ready-model"
	t.Cleanup(func() {
		_, _ = s.owner.Exec(context.Background(), `DELETE FROM model_leases WHERE model_key = $1`, model)
	})
	var repo string
	if err := s.owner.QueryRow(ctx, `SELECT id FROM repositories WHERE account_id = $1 AND name = 'alpha/one'`, alpha).Scan(&repo); err != nil {
		t.Fatal(err)
	}
	// Numbers that no other test's pull requests of the repository have.
	const firstPull = 91001
	pull := func(number int, draft bool) {
		t.Helper()
		if err := s.WithAccount(ctx, alpha, func(tx pgx.Tx) error {
			_, err := UpsertPullRequest(ctx, tx, PullRequestRow{AccountID: alpha, RepositoryID: repo, Number: number, Draft: draft, HeadSHA: "abc", Labels: []byte("[]")})
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		if err := s.WithAccount(context.Background(), alpha, func(tx pgx.Tx) error {
			_, err := tx.Exec(context.Background(), `DELETE FROM pull_requests WHERE repository_id = $1 AND number BETWEEN $2 AND $2 + 3`, repo, firstPull)
			return err
		}); err != nil {
			t.Error(err)
		}
	})
	insert := func(number int) int64 {
		t.Helper()
		var id int64
		if err := s.owner.QueryRow(ctx, `INSERT INTO river_job (kind, queue, args, state, max_attempts, scheduled_at, metadata)
			VALUES ('review', 'review', jsonb_build_object('account_id', $1::text, 'repository_id', $2::text, 'number', $3::int),
				'scheduled', 8, now() + interval '5 minutes', jsonb_build_object('slot_wait', $4::text)) RETURNING id`,
			alpha, repo, number, model).Scan(&id); err != nil {
			t.Fatalf("insert river job: %v", err)
		}
		t.Cleanup(func() { _, _ = s.owner.Exec(context.Background(), `DELETE FROM river_job WHERE id = $1`, id) })
		return id
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
	pull(firstPull, true)
	pull(firstPull+1, true)
	pull(firstPull+2, false)
	pull(firstPull+3, false)
	draft, marked, ready, later := insert(firstPull), insert(firstPull+1), insert(firstPull+2), insert(firstPull+3)
	// The second draft is marked ready while its review waits.
	pull(firstPull+1, false)
	claim := func(job int64) *Lease {
		t.Helper()
		l, err := s.TakeReviewLease(ctx, alpha, model, 1, job)
		if err != nil {
			t.Fatalf("TakeReviewLease: %v", err)
		}
		return l
	}
	// The slot is free, and the draft's review does not get it.
	if l := claim(draft); l != nil {
		_, _ = l.Release(ctx)
		t.Fatal("a draft's review took a slot that reviews of ready pull requests wait for")
	}
	// The reviews of ready pull requests are woken in turn. They have no
	// slot yet, so the draft's stays asleep.
	for i, want := range []int64{marked, ready, later, 0} {
		if got := release(); got != want {
			t.Fatalf("release %d woke job %d, want %d", i+1, got, want)
		}
	}
	if woken, err := s.WakeIfSlotFree(ctx, alpha, model, 1, draft); err != nil || woken {
		t.Fatalf("WakeIfSlotFree of a draft's review behind ready ones = %v, %v, want false", woken, err)
	}
	held := claim(marked)
	if held == nil {
		t.Fatal("the review of a pull request marked ready took no slot")
	}
	// The review that has its slot waits no more, also once it lets the
	// slot go, though River clears its mark only as its attempt ends.
	if waited, err := s.ClearSlotWait(ctx, alpha, marked); err != nil || waited != model {
		t.Fatalf("ClearSlotWait = %q, %v, want %q", waited, err, model)
	}
	if waited, err := s.ClearSlotWait(ctx, alpha, marked); err != nil || waited != "" {
		t.Fatalf("ClearSlotWait of a review with no mark = %q, %v", waited, err)
	}
	var waits bool
	if err := s.owner.QueryRow(ctx, `SELECT metadata ? 'slot_wait' FROM river_job WHERE id = $1`, marked).Scan(&waits); err != nil || waits {
		t.Fatalf("the review with a slot is marked as waiting = %v, %v", waits, err)
	}
	// Two ready reviews still wait, and then none does: the draft's is next.
	if _, err := s.owner.Exec(ctx, `DELETE FROM river_job WHERE id = ANY($1)`, []int64{ready, later}); err != nil {
		t.Fatal(err)
	}
	if woken, err := held.Release(ctx); err != nil || woken != draft {
		t.Fatalf("Release woke job %d, %v, want the draft's review %d", woken, err, draft)
	}
	l := claim(draft)
	if l == nil {
		t.Fatal("a draft's review took no slot with no other review waiting")
	}
	defer func() { _, _ = l.Release(context.Background()) }()

	draftTakesOnlySpareSlot(t, s, alpha, model, insert(firstPull), insert(firstPull+2))
}

// draftTakesOnlySpareSlot has the review job draft, of a draft, claim a slot
// of model for which three slots are given: one is held, and the review job
// ready, of a ready pull request, waits for one.
func draftTakesOnlySpareSlot(t *testing.T, s *Store, alpha, model string, draft, ready int64) {
	t.Helper()
	ctx := t.Context()
	// The slots are there before the claims, which then race for the two
	// that are free. One is left to the review that waits, so only one of
	// the claims made at once takes a slot.
	wide, err := s.TakeLease(ctx, alpha, model, 3, 7)
	if err != nil || wide == nil {
		t.Fatalf("TakeLease = %v, %v", wide, err)
	}
	if _, err := wide.Release(ctx); err != nil {
		t.Fatal(err)
	}
	// Two slots are free and one review waits, but that review has no
	// worker yet: the draft's review would hold one for as long as it runs.
	if l, err := s.TakeReviewLease(ctx, alpha, model, 3, draft); err != nil || l != nil {
		if l != nil {
			_, _ = l.Release(ctx)
		}
		t.Fatalf("a draft's review took a slot ahead of a ready one that is not running: %v", err)
	}
	if woken, err := s.WakeIfSlotFree(ctx, alpha, model, 3, draft); err != nil || woken {
		t.Fatalf("WakeIfSlotFree of a draft's review ahead of a ready one that is not running = %v, %v", woken, err)
	}
	if _, err := s.owner.Exec(ctx, `UPDATE river_job SET state = 'running' WHERE id = $1`, ready); err != nil {
		t.Fatal(err)
	}
	// The claims of the first round follow one another while each
	// connection prepares its statements. Those of the later rounds
	// overlap.
	for round := range 5 {
		count, err := claimAtOnce(ctx, 8, func() (*Lease, error) { return s.TakeReviewLease(ctx, alpha, model, 3, draft) })
		if err != nil || count != 1 {
			t.Fatalf("round %d: drafts' reviews took %d slots, %v, want the 1 that the waiting review leaves", round+1, count, err)
		}
	}
}

// claimAtOnce makes n claims at once and returns how many took a slot. It
// lets the slots go.
func claimAtOnce(ctx context.Context, n int, claim func() (*Lease, error)) (int, error) {
	var g errgroup.Group
	taken := make(chan *Lease, n)
	start := make(chan struct{})
	for range n {
		g.Go(func() error {
			<-start
			l, err := claim()
			if l != nil {
				taken <- l
			}
			return err
		})
	}
	close(start)
	err := g.Wait()
	close(taken)
	count := len(taken)
	for l := range taken {
		_, _ = l.Release(ctx)
	}
	return count, err
}

// TestWakeIfSlotFree: a review snoozed for a model slot is woken when a
// slot of the model is free, and only that review. It is left asleep while
// every slot is held, as is one not snoozed for a slot.
func TestWakeIfSlotFree(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, twoAccounts)); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}
	listRepos(t, s, "alpha", "alpha/one", "alpha/two")
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
