//go:build integration

package store

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestWorkerReadContracts(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, twoAccounts)); err != nil {
		t.Fatal(err)
	}
	account, other := accountID(t, s, "alpha"), accountID(t, s, "beta")
	reviewID := insertReview(t, ctx, s, account)
	var prID, repoID, runID string
	var number int
	must := func(t *testing.T, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(t, s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT p.id, p.repository_id, p.number FROM reviews r JOIN pull_requests p ON p.id = r.pull_request_id WHERE r.id = $1`, reviewID).Scan(&prID, &repoID, &number); err != nil {
			return err
		}
		var err error
		runID, err = InsertRunnerRun(ctx, tx, account, RunnerKindReview, reviewID, 0)
		return err
	}))
	t.Run("pull fields and nullable heartbeat", func(t *testing.T) {
		must(t, s.WithAccount(ctx, account, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `UPDATE pull_requests SET title = 'change', body = 'description', author_is_bot = true, labels = '["review"]', opened_at = NULL WHERE id = $1`, prID); err != nil {
				return err
			}
			pr, err := FindReviewPullRequest(ctx, tx, repoID, number)
			must(t, err)
			if pr.ID != prID || pr.RepositoryID != repoID || pr.Title != "change" || !pr.AuthorIsBot || pr.Closed != "" {
				t.Fatalf("pull = %+v", pr)
			}
			head, err := PullRequestHead(ctx, tx, prID)
			must(t, err)
			body, err := PullRequestBody(ctx, tx, prID)
			must(t, err)
			if head != "abc" || body != "description" {
				t.Fatalf("head/body = %q/%q", head, body)
			}
			filter, err := ReadFilterPullRequest(ctx, tx, prID)
			must(t, err)
			if !filter.CreatedAt.IsZero() || string(filter.Labels) != `["review"]` || filter.Body != body {
				t.Fatalf("filter = %+v", filter)
			}
			age, seen, err := RunnerHeartbeatAge(ctx, tx, runID)
			must(t, err)
			if seen || age != 0 {
				t.Fatalf("unseen heartbeat = %v, %v", age, seen)
			}
			if _, err := tx.Exec(ctx, `UPDATE runner_runs SET heartbeat_at = now() - interval '90 seconds', phase = 'done' WHERE id = $1`, runID); err != nil {
				return err
			}
			age, seen, err = RunnerHeartbeatAge(ctx, tx, runID)
			must(t, err)
			phase, err := RunnerPhase(ctx, tx, runID)
			must(t, err)
			if age != 90*time.Second || !seen || phase != "done" {
				t.Fatalf("run = %v, %v, %s", age, seen, phase)
			}
			_, err = tx.Exec(ctx, `INSERT INTO context_packs (runner_run_id, account_id, head_sha, base_sha, patch_id, diff, delta_paths) VALUES ($1, $2, 'abc', 'base', 'patch', '', '{delta.go}')`, runID, account)
			must(t, err)
			paths, err := ContextPackDeltaPaths(ctx, tx, runID)
			must(t, err)
			if !slices.Equal(paths, []string{"delta.go"}) {
				t.Fatal(paths)
			}
			return nil
		}))
	})
	t.Run("other account cannot read records", func(t *testing.T) {
		must(t, s.WithAccount(ctx, other, func(tx pgx.Tx) error {
			_, err := FindReviewPullRequest(ctx, tx, repoID, number)
			if !errors.Is(err, ErrNotFound) {
				t.Fatalf("pull = %v", err)
			}
			_, err = PullRequestHead(ctx, tx, prID)
			if !errors.Is(err, pgx.ErrNoRows) {
				t.Fatalf("head = %v", err)
			}
			_, _, err = RunnerHeartbeatAge(ctx, tx, runID)
			if !errors.Is(err, pgx.ErrNoRows) {
				t.Fatalf("heartbeat = %v", err)
			}
			_, err = ContextPackDeltaPaths(ctx, tx, runID)
			if !errors.Is(err, pgx.ErrNoRows) {
				t.Fatalf("paths = %v", err)
			}
			return nil
		}))
	})
	t.Run("label lock lasts until rollback", func(t *testing.T) {
		checkLabelLockTransaction(t, s, ctx, account, prID)
	})
	t.Run("follow-up retry keeps reply and time window", func(t *testing.T) {
		checkFollowupStoreContracts(t, s, ctx, account, other, prID)
	})
}

func TestReviewHistorySelection(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, twoAccounts)); err != nil {
		t.Fatal(err)
	}
	account, other := accountID(t, s, "alpha"), accountID(t, s, "beta")
	id := insertReview(t, ctx, s, account)
	var prID string
	must := func(t *testing.T, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(t, s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT pull_request_id FROM reviews WHERE id = $1`, id).Scan(&prID)
	}))
	t.Run("running review is not a baseline", func(t *testing.T) {
		must(t, s.WithAccount(ctx, account, func(tx pgx.Tx) error {
			p, err := LastCompletedReview(ctx, tx, prID)
			must(t, err)
			c, err := LastReviewConfidence(ctx, tx, prID, "")
			must(t, err)
			if p.ID != "" || c != nil {
				t.Fatalf("baseline = %+v, confidence = %+v", p, c)
			}
			return nil
		}))
	})
	must(t, s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE reviews SET status = 'completed', patch_id = 'baseline', forge_patch_id = 'forge-baseline', merge_base_sha = 'base',
			created_at = '2000-01-01', finished_at = now() - interval '1 minute', summary = '{"diagram":"graph TD","checked":["checks"],"impact":["impact"]}', confidence = '{"score":4,"threshold":3,"risk":"low"}' WHERE id = $1`, id)
		must(t, err)
		_, err = tx.Exec(ctx, `INSERT INTO findings (account_id, review_id, path, line, severity, title, explanation, posted_inline, forge_comment_id, fingerprint, carried_from)
			VALUES ($1, $2, 'a.go', 3, 'p1', 'finding', 'why', true, 42, 'fp', 'older')`, account, id)
		must(t, err)
		for i, status := range []ReviewStatus{ReviewFailed, ReviewPrepared, ReviewCompleted} {
			_, err := tx.Exec(ctx, `INSERT INTO reviews (account_id, pull_request_id, head_sha, status, partial, patch_id, forge_patch_id, merge_base_sha, created_at, finished_at)
				VALUES ($1, $2, 'other-head', $3, $4, 'newer', 'forge-newer', 'partial-base', $5, now())`, account, prID, status, status == ReviewCompleted, time.Date(2000, 1, 2+i, 0, 0, 0, 0, time.UTC))
			if err != nil {
				return err
			}
		}
		return nil
	}))
	t.Run("partial and noncompleted reviews do not replace baseline", func(t *testing.T) {
		must(t, s.WithAccount(ctx, account, func(tx pgx.Tx) error {
			p, err := LastCompletedReview(ctx, tx, prID)
			must(t, err)
			if p.ID != id || p.Diagram != "graph TD" || !slices.Equal(p.Checked, []string{"checks"}) || !slices.Equal(p.Impact, []string{"impact"}) || p.Confidence == nil || p.Confidence.Score != 4 {
				t.Fatalf("baseline = %+v", p)
			}
			if p.Changed != nil || len(p.Findings) != 1 || p.Findings[0].CommentID != 42 || !p.Findings[0].PostedInline || p.Findings[0].CarriedFrom != "older" {
				t.Fatalf("findings/paths = %+v, %v", p.Findings, p.Changed)
			}
			patch, err := LastReviewPatchID(ctx, tx, prID, "00000000-0000-0000-0000-000000000000")
			must(t, err)
			forgePatch, err := LastForgePatchID(ctx, tx, prID)
			must(t, err)
			c, err := LastReviewConfidence(ctx, tx, prID, "")
			must(t, err)
			if patch != "baseline" || forgePatch != "forge-baseline" || c == nil || c.Score != 4 || c.Threshold != 3 {
				t.Fatalf("baseline values = %q, %q, %+v", patch, forgePatch, c)
			}
			patch, err = LastReviewPatchID(ctx, tx, prID, id)
			must(t, err)
			c, err = LastReviewConfidence(ctx, tx, prID, id)
			must(t, err)
			if patch != "" || c != nil {
				t.Fatalf("excluded baseline = %q, %+v", patch, c)
			}
			base, err := LastReviewMergeBase(ctx, tx, prID, "abc")
			must(t, err)
			count, err := CompletedReviewCount(ctx, tx, prID)
			must(t, err)
			if base != "partial-base" || count != 2 {
				t.Fatalf("merge base/count = %s, %d", base, count)
			}
			return nil
		}))
	})
	t.Run("current head wins over later completion", func(t *testing.T) {
		checkCurrentHeadHistory(t, s, ctx, account, prID, id)
	})
	t.Run("other account has no history", func(t *testing.T) {
		must(t, s.WithAccount(ctx, other, func(tx pgx.Tx) error {
			p, err := LastCompletedReview(ctx, tx, prID)
			must(t, err)
			count, err := CompletedReviewCount(ctx, tx, prID)
			must(t, err)
			if p.ID != "" || count != 0 {
				t.Fatalf("other account history = %+v, %d", p, count)
			}
			return nil
		}))
	})
}

func checkFollowupStoreContracts(t *testing.T, s *Store, ctx context.Context, account, other, prID string) {
	t.Helper()
	must := func(t *testing.T, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(t, s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		r := FollowupResult{AccountID: account, PullRequestID: prID, CommentID: 1, Status: FollowupAnswered, ReplyCommentID: 42}
		must(t, RecordFollowup(ctx, tx, r))
		r.ReplyCommentID, r.Reason = 0, strings.Repeat("x", 600)
		must(t, RecordFollowup(ctx, tx, r))
		state, err := FindFollowup(ctx, tx, prID, 1)
		must(t, err)
		if state.ReplyCommentID == nil || *state.ReplyCommentID != 42 || len(state.Reason) != 500 {
			t.Fatalf("follow-up = %+v", state)
		}
		r.CommentID, r.Status, r.Reason = 2, FollowupLimited, "limited"
		must(t, RecordFollowup(ctx, tx, r))
		r.CommentID, r.Status = 3, FollowupAnswered
		must(t, RecordFollowup(ctx, tx, r))
		_, err = tx.Exec(ctx, `UPDATE followups SET created_at = now() - interval '2 hours' WHERE pull_request_id = $1 AND comment_id = 3`, prID)
		must(t, err)
		must(t, RecordFollowup(ctx, tx, r))
		answered, notices, err := RecentFollowupCounts(ctx, tx, prID)
		must(t, err)
		if answered != 1 || notices != 1 {
			t.Fatalf("counts = %d, %d", answered, notices)
		}
		return nil
	}))
	must(t, s.WithAccount(ctx, other, func(tx pgx.Tx) error {
		_, err := FindFollowup(ctx, tx, prID, 1)
		if !errors.Is(err, ErrNotFound) {
			t.Fatal(err)
		}
		answered, notices, err := RecentFollowupCounts(ctx, tx, prID)
		if answered != 0 || notices != 0 {
			t.Fatalf("other account counts = %d, %d", answered, notices)
		}
		return err
	}))
}

func checkCurrentHeadHistory(t *testing.T, s *Store, ctx context.Context, account, prID, id string) {
	t.Helper()
	must := func(t *testing.T, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(t, s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE reviews SET status = 'failed' WHERE id = $1`, id)
		must(t, err)
		status, err := ReviewStatusSince(ctx, tx, prID, time.Now().Add(-time.Hour))
		must(t, err)
		if status != ReviewFailed {
			t.Fatal(status)
		}
		status, current, err := ReviewHeadStatus(ctx, tx, id)
		must(t, err)
		if status != ReviewFailed || !current {
			t.Fatalf("head status = %s, %v", status, current)
		}
		return nil
	}))
}

func checkLabelLockTransaction(t *testing.T, s *Store, ctx context.Context, account, prID string) {
	t.Helper()
	must := func(t *testing.T, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	rollback := errors.New("rollback skip transaction")
	err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		labels, err := LockPullRequestLabels(ctx, tx, prID)
		if err != nil {
			return err
		}
		if string(labels) != `["review"]` {
			t.Fatal(string(labels))
		}
		blocked := s.WithAccount(ctx, account, func(other pgx.Tx) error {
			if _, err := other.Exec(ctx, `SET LOCAL lock_timeout = '50ms'`); err != nil {
				return err
			}
			_, err := other.Exec(ctx, `UPDATE pull_requests SET labels = '[]' WHERE id = $1`, prID)
			return err
		})
		pgerr, ok := errors.AsType[*pgconn.PgError](blocked)
		if !ok || pgerr.Code != "55P03" {
			t.Fatalf("label update = %v, want lock timeout", blocked)
		}
		if err := RecordFollowup(ctx, tx, FollowupResult{AccountID: account, PullRequestID: prID, CommentID: 1, Status: FollowupAnswered}); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	must(t, s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		_, err := FindFollowup(ctx, tx, prID, 1)
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("rolled-back follow-up = %v", err)
		}
		_, err = tx.Exec(ctx, `UPDATE pull_requests SET labels = '[]' WHERE id = $1`, prID)
		return err
	}))
}
