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

// insertReview creates a pull_requests row and a reviews row on top of it,
// both owned by account. The pull_requests row has a randomized,
// collision-safe number. This matches the convention that TestGatewayTokens
// already established for this UNIQUE(repository_id, number) constraint.
// insertReview returns the review's id.
func insertReview(t *testing.T, ctx context.Context, s *Store, account string) string {
	t.Helper()
	var reviewID string
	err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		var repoID string
		if err := tx.QueryRow(ctx, `SELECT id FROM repositories WHERE account_id = $1 LIMIT 1`, account).Scan(&repoID); err != nil {
			return err
		}
		var prID string
		if err := tx.QueryRow(ctx, `INSERT INTO pull_requests (account_id, repository_id, number, head_sha)
			VALUES ($1, $2, (random() * 1e6)::int, 'abc') RETURNING id`, account, repoID).Scan(&prID); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `INSERT INTO reviews (account_id, pull_request_id, head_sha, status)
			VALUES ($1, $2, 'abc', 'running') RETURNING id`, account, prID).Scan(&reviewID)
	})
	if err != nil {
		t.Fatalf("insert review: %v", err)
	}
	return reviewID
}

// TestListenPublishesReviewEvents exercises the notify.go/0001_init.sql
// contract end to end: a reviews.status change must produce an Event on the
// kritika_events channel that Listen decodes and hands to onEvent.
func TestListenPublishesReviewEvents(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, twoAccounts)); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}
	listRepos(t, s, "alpha", "alpha/one", "alpha/two")
	alpha := accountID(t, s, "alpha")
	reviewID := insertReview(t, ctx, s, alpha)

	events := make(chan Event, 10)
	listenCtx := t.Context()
	go s.Listen(listenCtx, ListenHandlers{OnEvent: func(e Event) { events <- e }})
	// Postgres only delivers NOTIFY to sessions already LISTENing at commit
	// time. Give Listen's connection a moment to register before the write.
	time.Sleep(250 * time.Millisecond)

	if err := s.WithAccount(ctx, alpha, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE reviews SET status = 'completed' WHERE id = $1`, reviewID)
		return err
	}); err != nil {
		t.Fatalf("update review status: %v", err)
	}

	select {
	case e := <-events:
		if e.Kind != EventReview || e.ID != reviewID || e.AccountID != alpha {
			t.Fatalf("event = %+v, want kind=review id=%s account=%s", e, reviewID, alpha)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no review event received for the status change")
	}
}

// TestListenSkipsRunnerRunHeartbeatOnlyUpdates makes sure that the WHEN clause
// on kritika_notify_runner_run works. A heartbeat-only update must not notify.
// A phase change (the positive control, which proves that the listener itself
// works) must notify, and must not fire more than once for it.
func TestListenSkipsRunnerRunHeartbeatOnlyUpdates(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, twoAccounts)); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}
	listRepos(t, s, "alpha", "alpha/one", "alpha/two")
	alpha := accountID(t, s, "alpha")
	var runID string
	if err := s.WithAccount(ctx, alpha, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO runner_runs (account_id, kind) VALUES ($1, 'index') RETURNING id`, alpha).Scan(&runID)
	}); err != nil {
		t.Fatalf("insert runner_runs: %v", err)
	}

	events := make(chan Event, 10)
	listenCtx := t.Context()
	go s.Listen(listenCtx, ListenHandlers{OnEvent: func(e Event) { events <- e }})
	time.Sleep(250 * time.Millisecond)

	if err := s.WithAccount(ctx, alpha, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE runner_runs SET heartbeat_at = now() WHERE id = $1`, runID)
		return err
	}); err != nil {
		t.Fatalf("update heartbeat_at: %v", err)
	}
	select {
	case e := <-events:
		t.Fatalf("heartbeat-only update must not notify, got %+v", e)
	case <-time.After(500 * time.Millisecond):
	}

	if err := s.WithAccount(ctx, alpha, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE runner_runs SET phase = 'fetching' WHERE id = $1`, runID)
		return err
	}); err != nil {
		t.Fatalf("update phase: %v", err)
	}
	select {
	case e := <-events:
		if e.Kind != EventRunnerRun || e.ID != runID {
			t.Fatalf("event = %+v, want kind=runner_run id=%s", e, runID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no runner_run event received for the phase change (listener not working?)")
	}
	// The phase change above must produce exactly one event, not a spurious
	// second one (for example, a stray heartbeat notification re-delivered).
	select {
	case e := <-events:
		t.Fatalf("unexpected second event after the phase change: %+v", e)
	case <-time.After(500 * time.Millisecond):
	}
}

// TestModelCallsRowLevelSecurity makes sure that model_calls, the one new web
// table that is account content, gets the same account_isolation treatment as
// every other account-scoped table.
func TestModelCallsRowLevelSecurity(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, twoAccounts)); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}
	listRepos(t, s, "alpha", "alpha/one", "alpha/two")
	alpha, beta := accountID(t, s, "alpha"), accountID(t, s, "beta")

	if err := s.WithAccount(ctx, alpha, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO model_calls (account_id, kind, model) VALUES ($1, 'followup', 'acme/large')`, alpha)
		return err
	}); err != nil {
		t.Fatalf("insert model_calls: %v", err)
	}

	count := func(account string) int {
		t.Helper()
		var n int
		if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) FROM model_calls`).Scan(&n)
		}); err != nil {
			t.Fatalf("count model_calls: %v", err)
		}
		return n
	}
	if n := count(alpha); n != 1 {
		t.Fatalf("alpha sees %d model_calls rows, want 1", n)
	}
	if n := count(beta); n != 0 {
		t.Fatalf("beta sees %d model_calls rows, want 0 (RLS leak)", n)
	}

	// A foreign account_id must fail the WITH CHECK policy, same as every
	// other account-scoped table.
	err := s.WithAccount(ctx, alpha, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO model_calls (account_id, kind, model) VALUES ($1, 'followup', 'acme/large')`, beta)
		return err
	})
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); !ok || pgErr.Code != "42501" {
		t.Fatalf("insert into model_calls with a foreign account_id: err = %v, want a 42501 permission-denied error", err)
	}
}

// TestLastRepoFiles: a repository's file is the one its newest context pack
// read, nil where that pack read none, and only the account's own
// repositories are read.
func TestLastRepoFiles(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, twoAccounts)); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}
	// The suites share one database: repositories of this test's own, so
	// no other test's packs are read.
	listRepos(t, s, "alpha", "alpha/files-one", "alpha/files-two")
	listRepos(t, s, "beta", "beta/files")
	alpha, beta := accountID(t, s, "alpha"), accountID(t, s, "beta")
	// pack records a review of the repository whose context pack, written
	// age ago, read doc as the repository's file, or none for nil. It returns
	// the review's id.
	pack := func(account, repo string, doc *string, age time.Duration) string {
		t.Helper()
		var reviewID string
		if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
			var prID, runID string
			if err := tx.QueryRow(ctx, `INSERT INTO pull_requests (account_id, repository_id, number, head_sha)
				VALUES ($1, $2, (random() * 1e6)::int, 'abc') RETURNING id`, account, configfile.RepositoryID(account, repo)).Scan(&prID); err != nil {
				return err
			}
			if err := tx.QueryRow(ctx, `INSERT INTO reviews (account_id, pull_request_id, head_sha, status)
				VALUES ($1, $2, 'abc', 'completed') RETURNING id`, account, prID).Scan(&reviewID); err != nil {
				return err
			}
			if err := tx.QueryRow(ctx, `INSERT INTO runner_runs (account_id, kind, review_id) VALUES ($1, 'review', $2) RETURNING id`,
				account, reviewID).Scan(&runID); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `INSERT INTO context_packs (runner_run_id, account_id, head_sha, base_sha, patch_id, diff, created_at, repo_files)
				VALUES ($1, $2, 'h', 'b', 'p', '', now() - make_interval(secs => $3),
					CASE WHEN $4::text IS NULL THEN '{}'::jsonb ELSE jsonb_build_object('.kritika.yaml', $4::text) END)`,
				runID, account, age.Seconds(), doc)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return reviewID
	}
	old, newer, other := "one: old", "one: new", "beta: file"
	pack(alpha, "alpha/files-one", &old, 2*time.Hour)
	newest := pack(alpha, "alpha/files-one", &newer, time.Hour)
	// The newest pack read no file: no older pack's stands in for it.
	pack(alpha, "alpha/files-two", &old, 2*time.Hour)
	bare := pack(alpha, "alpha/files-two", nil, time.Hour)
	pack(beta, "beta/files", &other, time.Minute)
	one, two := configfile.RepositoryID(alpha, "alpha/files-one"), configfile.RepositoryID(alpha, "alpha/files-two")
	betas := configfile.RepositoryID(beta, "beta/files")
	if err := s.WithAccount(ctx, alpha, func(tx pgx.Tx) error {
		files, err := LastRepoFiles(ctx, tx)
		if err != nil {
			return err
		}
		if _, ok := files[betas]; ok || files[one].ReviewID != newest || files[one].Doc == nil || *files[one].Doc != newer ||
			files[two].ReviewID != bare || files[two].Doc != nil {
			t.Fatalf("files = %+v, want alpha/files-one's newest and alpha/files-two's with no file", files)
		}
		for _, tt := range []struct {
			repo, review string
			doc          *string
		}{{one, newest, &newer}, {two, bare, nil}} {
			f, err := LastRepoFile(ctx, tx, tt.repo)
			if err != nil || f.ReviewID != tt.review || f.Commit != "b" || (f.Doc == nil) != (tt.doc == nil) || f.Doc != nil && *f.Doc != *tt.doc {
				t.Fatalf("file of %s = %+v, %v; want review %s", tt.repo, f, err, tt.review)
			}
		}
		if _, err := LastRepoFile(ctx, tx, betas); !errors.Is(err, ErrNotFound) {
			t.Fatalf("another account's file = %v, want not found", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
