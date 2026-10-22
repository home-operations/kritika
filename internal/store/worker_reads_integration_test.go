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
			if pr.ID != prID || pr.RepositoryID != repoID || pr.Title != "change" || !pr.AuthorIsBot || pr.State != "open" || pr.Merged {
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
			changes, err := ContextPackDiffs(ctx, tx, runID)
			must(t, err)
			if !slices.Equal(changes.DeltaPaths, []string{"delta.go"}) {
				t.Fatal(changes.DeltaPaths)
			}
			return nil
		}))
	})
	t.Run("other account cannot read records", func(t *testing.T) {
		checkMissingWorkerRecords(t, s, ctx, other, repoID, prID, runID, reviewID, number)
	})
	t.Run("label lock lasts until rollback", func(t *testing.T) {
		checkLabelLockTransaction(t, s, ctx, account, prID)
	})
	t.Run("follow-up retry keeps reply and time window", func(t *testing.T) {
		checkFollowupStoreContracts(t, s, ctx, account, other, prID)
	})
	t.Run("follow-up counts take turns", func(t *testing.T) {
		checkFollowupCountsTakeTurns(t, s, ctx, account, prID)
	})
}

// checkFollowupCountsTakeTurns: a count holds the pull request's follow-up
// lock until its transaction ends, so another waits for it and then counts
// what the first recorded.
func checkFollowupCountsTakeTurns(t *testing.T, s *Store, ctx context.Context, account, prID string) {
	t.Helper()
	var before int
	err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		_, notices, err := RecentFollowupCounts(ctx, tx, prID)
		if err != nil {
			return err
		}
		before = notices
		blocked := s.WithAccount(ctx, account, func(other pgx.Tx) error {
			if _, err := other.Exec(ctx, `SET LOCAL lock_timeout = '50ms'`); err != nil {
				return err
			}
			_, _, err := RecentFollowupCounts(ctx, other, prID)
			return err
		})
		if pgerr, ok := errors.AsType[*pgconn.PgError](blocked); !ok || pgerr.Code != "55P03" {
			t.Fatalf("a second count = %v, want it to wait for the first", blocked)
		}
		return RecordFollowup(ctx, tx, FollowupResult{AccountID: account, PullRequestID: prID, CommentID: 200, Status: FollowupLimited})
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		_, notices, err := RecentFollowupCounts(ctx, tx, prID)
		if err == nil && notices != before+1 {
			t.Fatalf("notices after the first count's record = %d, want %d", notices, before+1)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// A mention that failed two hours ago and is limited on a retry counts
	// from its retry.
	if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		r := FollowupResult{AccountID: account, PullRequestID: prID, CommentID: 201, Status: FollowupFailed}
		if err := RecordFollowup(ctx, tx, r); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE followups SET created_at = now() - interval '2 hours' WHERE pull_request_id = $1 AND comment_id = 201`,
			prID); err != nil {
			return err
		}
		r.Status = FollowupLimited
		if err := RecordFollowup(ctx, tx, r); err != nil {
			return err
		}
		_, notices, err := RecentFollowupCounts(ctx, tx, prID)
		if err == nil && notices != before+2 {
			t.Fatalf("notices after a retry limited = %d, want %d", notices, before+2)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
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
			baseline, err := LastReviewBaseline(ctx, tx, prID, "")
			must(t, err)
			if p.ID != "" || baseline.Confidence != nil || baseline.PatchID != "" {
				t.Fatalf("baseline = %+v, confidence = %+v", p, baseline.Confidence)
			}
			return nil
		}))
	})
	must(t, s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE reviews SET status = 'completed', patch_id = 'baseline', forge_patch_id = 'forge-baseline', merge_base_sha = 'base',
			created_at = '2000-01-01', finished_at = now() - interval '1 minute', summary = '{"diagram":"graph TD","checked":["checks"]}', confidence = '{"score":4,"threshold":3,"risk":"low"}' WHERE id = $1`, id)
		must(t, err)
		_, err = tx.Exec(ctx, `INSERT INTO findings (account_id, review_id, path, line, severity, title, explanation, posted_inline, forge_comment_id, fingerprint, carried_from)
			VALUES ($1, $2, 'a.go', 3, 'p1', 'finding', 'why', true, 42, 'fp', 'older')`, account, id)
		must(t, err)
		for i, status := range []ReviewStatus{ReviewCompleted, ReviewPrepared, ReviewFailed} {
			_, err := tx.Exec(ctx, `INSERT INTO reviews (account_id, pull_request_id, head_sha, status, partial, patch_id, forge_patch_id, merge_base_sha, created_at, finished_at)
				VALUES ($1, $2, 'other-head', $3, $4, 'newer', 'forge-newer', $5, $6, now())`, account, prID, status, status == ReviewCompleted, string(status)+"-base", time.Date(2000, 1, 2+i, 0, 0, 0, 0, time.UTC))
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
			if p.ID != id || p.Diagram != "graph TD" || !slices.Equal(p.Checked, []string{"checks"}) || p.Confidence == nil || p.Confidence.Score != 4 {
				t.Fatalf("baseline = %+v", p)
			}
			if p.Changed != nil || len(p.Findings) != 1 || p.Findings[0].CommentID != 42 || !p.Findings[0].PostedInline || p.Findings[0].CarriedFrom != "older" {
				t.Fatalf("findings/paths = %+v, %v", p.Findings, p.Changed)
			}
			baseline, err := LastReviewBaseline(ctx, tx, prID, "")
			must(t, err)
			forgePatch, err := LastForgePatchID(ctx, tx, prID)
			must(t, err)
			c := baseline.Confidence
			if baseline.PatchID != "baseline" || forgePatch != "forge-baseline" || c == nil || c.Score != 4 || c.Threshold != 3 {
				t.Fatalf("baseline values = %+v, %q, %+v", baseline, forgePatch, c)
			}
			baseline, err = LastReviewBaseline(ctx, tx, prID, id)
			must(t, err)
			if baseline.PatchID != "" || baseline.Confidence != nil {
				t.Fatalf("excluded baseline = %+v", baseline)
			}
			base, err := LastReviewMergeBase(ctx, tx, prID, "abc")
			must(t, err)
			count, err := CompletedReviewCount(ctx, tx, prID)
			must(t, err)
			if base != "completed-base" || count != 2 {
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
		state, err := FindFollowup(ctx, tx, prID, 1, false)
		must(t, err)
		if state.ReplyCommentID == nil || *state.ReplyCommentID != 42 || len(state.Reason) != 500 {
			t.Fatalf("follow-up = %+v", state)
		}
		inline := r
		inline.CommentID = 4
		inline.Inline, inline.Status, inline.Reason, inline.ReplyCommentID = true, FollowupIgnored, "inline", 84
		must(t, RecordFollowup(ctx, tx, inline))
		inline.ReplyCommentID = 0
		must(t, RecordFollowup(ctx, tx, inline))
		for _, tt := range []struct {
			inline    bool
			commentID int64
			status    FollowupStatus
			reply     int64
		}{
			{false, 1, FollowupAnswered, 42},
			{true, 4, FollowupIgnored, 84},
		} {
			state, err := FindFollowup(ctx, tx, prID, tt.commentID, tt.inline)
			must(t, err)
			if state.Status != tt.status || state.ReplyCommentID == nil || *state.ReplyCommentID != tt.reply {
				t.Fatalf("inline=%v: follow-up = %+v", tt.inline, state)
			}
		}
		_, err = FindFollowup(ctx, tx, prID, 1, true)
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("other comment namespace = %v, want not found", err)
		}
		_, err = tx.Exec(ctx, `INSERT INTO followups (account_id, pull_request_id, comment_id, author, status)
			VALUES ($1, $2, 4, '', 'ignored') ON CONFLICT (pull_request_id, comment_id)
			DO UPDATE SET reason = 'legacy writer'`, account, prID)
		must(t, err)
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
	for i, tt := range []struct {
		name   string
		status FollowupStatus
		reply  int64
		want   FollowupStatus
	}{
		{"acknowledged", FollowupAnswered, 0, FollowupAnswered},
		{"limited", FollowupLimited, 0, FollowupLimited},
		{"reply recorded", FollowupIgnored, 42, FollowupIgnored},
		{"retryable", FollowupFailed, 0, FollowupFailed},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := FollowupResult{AccountID: account, PullRequestID: prID, CommentID: int64(100 + i), Status: tt.status, ReplyCommentID: tt.reply, Reason: "durable"}
			must(t, s.WithAccount(ctx, account, func(tx pgx.Tx) error {
				return RecordFollowup(ctx, tx, r)
			}))
			r.Status, r.Reason, r.ReplyCommentID = FollowupFailed, "commit response lost", 0
			must(t, s.WithAccount(ctx, account, func(tx pgx.Tx) error {
				return RecordFollowup(ctx, tx, r)
			}))
			must(t, s.WithAccount(ctx, account, func(tx pgx.Tx) error {
				state, err := FindFollowup(ctx, tx, prID, r.CommentID, false)
				must(t, err)
				wantReason := "durable"
				if tt.want == FollowupFailed {
					wantReason = r.Reason
				}
				if state.Status != tt.want || state.Reason != wantReason {
					t.Fatalf("after failed attempt = %+v; want %s (%s)", state, tt.want, wantReason)
				}
				return nil
			}))
		})
	}
	must(t, s.WithAccount(ctx, other, func(tx pgx.Tx) error {
		_, err := FindFollowup(ctx, tx, prID, 1, false)
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
	must(t, s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE pull_requests SET labels = '["review"]' WHERE id = $1`, prID)
		return err
	}))
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
		_, err := FindFollowup(ctx, tx, prID, 1, false)
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("rolled-back follow-up = %v", err)
		}
		_, err = tx.Exec(ctx, `UPDATE pull_requests SET labels = '[]' WHERE id = $1`, prID)
		return err
	}))
}

func checkMissingWorkerRecords(t *testing.T, s *Store, ctx context.Context, account, repoID, prID, runID, reviewID string, number int) {
	t.Helper()
	tests := []struct {
		name string
		read func(pgx.Tx) error
	}{
		{"pull", func(tx pgx.Tx) error { _, err := FindReviewPullRequest(ctx, tx, repoID, number); return err }},
		{"head", func(tx pgx.Tx) error { _, err := PullRequestHead(ctx, tx, prID); return err }},
		{"body", func(tx pgx.Tx) error { _, err := PullRequestBody(ctx, tx, prID); return err }},
		{"labels", func(tx pgx.Tx) error { _, err := LockPullRequestLabels(ctx, tx, prID); return err }},
		{"filter", func(tx pgx.Tx) error { _, err := ReadFilterPullRequest(ctx, tx, prID); return err }},
		{"heartbeat", func(tx pgx.Tx) error { _, _, err := RunnerHeartbeatAge(ctx, tx, runID); return err }},
		{"phase", func(tx pgx.Tx) error { _, err := RunnerPhase(ctx, tx, runID); return err }},
		{"diffs", func(tx pgx.Tx) error { _, err := ContextPackDiffs(ctx, tx, runID); return err }},
		{"review status", func(tx pgx.Tx) error { _, _, err := ReviewHeadStatus(ctx, tx, reviewID); return err }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := s.WithAccount(ctx, account, tt.read)
			if !errors.Is(err, ErrNotFound) {
				t.Fatalf("read = %v, want ErrNotFound", err)
			}
		})
	}
}

func TestOtherReviewRunningStates(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, twoAccounts)); err != nil {
		t.Fatal(err)
	}
	account := accountID(t, s, "alpha")
	id := insertReview(t, ctx, s, account)
	for _, tt := range []struct {
		status ReviewStatus
		busy   bool
	}{{ReviewRunning, true}, {ReviewPrepared, true}, {ReviewCompleted, false}, {ReviewFailed, false}, {ReviewSkipped, false}} {
		t.Run(string(tt.status), func(t *testing.T) {
			err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
				var prID string
				if err := tx.QueryRow(ctx, `UPDATE reviews SET status = $2 WHERE id = $1 RETURNING pull_request_id`, id, tt.status).Scan(&prID); err != nil {
					return err
				}
				busy, err := OtherReviewRunning(ctx, tx, prID, "")
				if err != nil {
					return err
				}
				if busy != tt.busy {
					t.Fatalf("busy = %v, want %v", busy, tt.busy)
				}
				busy, err = OtherReviewRunning(ctx, tx, prID, id)
				if busy {
					t.Fatal("excluded review counts as busy")
				}
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestReviewBaselineConfidence(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, twoAccounts)); err != nil {
		t.Fatal(err)
	}
	account := accountID(t, s, "alpha")
	id := insertReview(t, ctx, s, account)
	for _, tt := range []struct {
		name, raw string
		hasScore  bool
	}{
		{"SQL null", "", false},
		{"JSON null", "null", false},
		{"score", `{"score":4}`, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
				var prID string
				if err := tx.QueryRow(ctx, `UPDATE reviews SET status = 'completed', patch_id = 'patch', confidence = nullif($2, '')::jsonb WHERE id = $1 RETURNING pull_request_id`, id, tt.raw).Scan(&prID); err != nil {
					return err
				}
				baseline, err := LastReviewBaseline(ctx, tx, prID, "")
				if err != nil {
					return err
				}
				full, err := LastCompletedReview(ctx, tx, prID)
				if err != nil {
					return err
				}
				if baseline.PatchID != "patch" || (baseline.Confidence != nil) != tt.hasScore || (full.Confidence != nil) != tt.hasScore {
					t.Fatalf("baseline = %+v, full confidence = %+v", baseline, full.Confidence)
				}
				if tt.hasScore && (baseline.Confidence.Score != 4 || full.Confidence.Score != 4) {
					t.Fatal("stored score not returned")
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}
