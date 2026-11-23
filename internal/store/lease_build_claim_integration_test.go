//go:build integration

package store

import (
	"context"
	"testing"
	"time"
)

// TestIndexBuildClaimsQueue: an index build's claim of a slot that comes
// while another build's claim commits waits for it, and then takes a free
// slot, rather than give up as if every slot were held. No update waits, so
// nothing should hold the builds off.
func TestIndexBuildClaimsQueue(t *testing.T) {
	s := openStore(t)
	// A bound on the test's waits alone, for a claim that never queues or
	// never ends: the test then fails rather than hold up the suite.
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	if err := s.ApplyConfig(ctx, parse(t, twoAccounts)); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}
	alpha := accountID(t, s, "alpha")
	const model = "embed:test/build-claims"
	const slots = 2
	t.Cleanup(func() {
		_, _ = s.owner.Exec(context.Background(), `DELETE FROM model_leases WHERE model_key = $1`, model)
	})
	// The slots exist, as after an earlier claim: rows that the first claim
	// inserted would hold the second up before it reached the claims' locks.
	if _, err := s.owner.Exec(ctx, `INSERT INTO model_leases (scope, model_key, slot)
		SELECT $1, $2, s FROM generate_series(1, $3::int) AS s`, alpha, model, slots); err != nil {
		t.Fatalf("insert slots: %v", err)
	}
	// The first build's claim, under way: it holds the locks that a build's
	// claim takes and has yet to claim its slot. Once it has, the second
	// claim's insert of the slots would wait for it to commit anyway.
	first, err := s.owner.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Rollback(context.Background()) }()
	for _, stmt := range []string{
		`SELECT pg_advisory_xact_lock(` + indexBuildClaimLock + `)`,
		`SELECT pg_try_advisory_xact_lock(` + indexWaitLock + `)`,
	} {
		if _, err := first.Exec(ctx, stmt, alpha, model); err != nil {
			t.Fatalf("first claim: %v", err)
		}
	}

	var second *Lease
	var secondErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		second, secondErr = s.TakeIndexBuildLease(ctx, alpha, model, own(slots), 2)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
		if second != nil {
			_, _ = second.Release(context.Background())
		}
	})
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	for {
		var queued bool
		if err := s.owner.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_locks l, (SELECT `+indexBuildClaimLock+` AS k) w
			WHERE l.locktype = 'advisory' AND NOT l.granted AND l.objsubid = 1
				AND l.classid::bigint = (w.k >> 32) & 4294967295 AND l.objid::bigint = w.k & 4294967295)`,
			alpha, model).Scan(&queued); err != nil {
			t.Fatalf("the second claim's wait not seen: %v", err)
		}
		if queued {
			break
		}
		select {
		case <-done:
			t.Fatalf("TakeIndexBuildLease = %v, %v while another build's claim committed; want it to wait for that claim",
				second, secondErr)
		case <-ctx.Done():
			t.Fatalf("the second claim's wait not seen: %v", ctx.Err())
		case <-poll.C:
		}
	}
	if _, err := first.Exec(ctx, `UPDATE model_leases SET job_id = 1, account_id = $1::uuid, expires_at = now() + interval '2 minutes'
		WHERE scope = $1::uuid::text AND model_key = $2 AND slot = 1`, alpha, model); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if err := first.Commit(ctx); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatalf("the second claim did not end: %v", ctx.Err())
	}
	if secondErr != nil || second == nil {
		t.Fatalf("TakeIndexBuildLease = %v, %v; want the free slot", second, secondErr)
	}
	if second.slot != 2 {
		t.Fatalf("the second build took slot %d, want 2, the one the first left free", second.slot)
	}
}
