//go:build integration

package store

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/riverqueue/river/rivertype"

	"github.com/home-operations/kritika/internal/tuning"
)

// insertRiverJob puts a running job into River's table as a worker that
// claimed it ago leaves it, and removes it when the test ends.
func insertRiverJob(t *testing.T, ctx context.Context, s *Store, kind, state string, attempt, maxAttempts int, ago time.Duration, metadata string) int64 {
	t.Helper()
	var id int64
	if err := s.owner.QueryRow(ctx, `INSERT INTO river_job (kind, queue, args, state, attempt, max_attempts, attempted_at, metadata, finalized_at)
		VALUES ($1, $1, '{}', $2::text::river_job_state, $3, $4, now() - make_interval(secs => $5), $6::jsonb,
			CASE WHEN $2 IN ('completed', 'discarded', 'cancelled') THEN now() END) RETURNING id`,
		kind, state, attempt, maxAttempts, ago.Seconds(), metadata).Scan(&id); err != nil {
		t.Fatalf("insert river job: %v", err)
	}
	t.Cleanup(func() {
		_, _ = s.owner.Exec(context.Background(), `DELETE FROM job_heartbeats WHERE job_id = $1`, id)
		_, _ = s.owner.Exec(context.Background(), `DELETE FROM river_job WHERE id = $1`, id)
	})
	return id
}

// insertRun creates a runner run of job for a review, or for an index
// generation when review is "". It returns the run's and the parent's id.
func insertRun(t *testing.T, ctx context.Context, s *Store, account, review string, job int64) (runID, parentID string) {
	t.Helper()
	err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		parentID = review
		kind := RunnerKindReview
		if review == "" {
			kind = RunnerKindIndex
			if err := tx.QueryRow(ctx, `INSERT INTO index_runs (account_id, repository_id, commit_sha, embed_model, embed_dims, mode, status)
				SELECT $1, id, 'abc', 'm', 8, 'full', 'running' FROM repositories WHERE account_id = $1 LIMIT 1 RETURNING id`, account).
				Scan(&parentID); err != nil {
				return err
			}
		}
		var err error
		runID, err = InsertRunnerRun(ctx, tx, account, kind, parentID, job)
		return err
	})
	if err != nil {
		t.Fatalf("insert run: %v", err)
	}
	return runID, parentID
}

// rescueFixture is the set of running jobs TestAbandonedJobs and
// TestRescueJob share: one that stopped beating, one still beating, one
// claimed too recently to judge, one on its last attempt, one a cancel was
// asked of, and one that completed.
type rescueFixture struct {
	silent, beating, fresh, lastTry, cancelled, done int64
	alpha, review                                    string
}

const rescueStale = 3 * time.Minute

func newRescueFixture(t *testing.T, ctx context.Context, s *Store) rescueFixture {
	t.Helper()
	if err := s.ApplyConfig(ctx, parse(t, twoAccounts)); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}
	listRepos(t, s, "alpha", "alpha/one", "alpha/two")
	f := rescueFixture{
		alpha:     accountID(t, s, "alpha"),
		silent:    insertRiverJob(t, ctx, s, "review", "running", 1, 25, 10*time.Minute, `{}`),
		beating:   insertRiverJob(t, ctx, s, "review", "running", 1, 25, 10*time.Minute, `{}`),
		fresh:     insertRiverJob(t, ctx, s, "review", "running", 1, 25, time.Minute, `{}`),
		lastTry:   insertRiverJob(t, ctx, s, "index", "running", 3, 3, 10*time.Minute, `{}`),
		cancelled: insertRiverJob(t, ctx, s, "followup", "running", 1, 25, 10*time.Minute, `{"cancel_attempted_at": "2026-01-01T00:00:00Z"}`),
		done:      insertRiverJob(t, ctx, s, "review", "completed", 1, 25, 10*time.Minute, `{}`),
	}
	for _, id := range []int64{f.beating, f.fresh, f.done} {
		if err := s.HeartbeatJob(ctx, id); err != nil {
			t.Fatalf("HeartbeatJob(%d): %v", id, err)
		}
	}
	f.review = insertReview(t, ctx, s, f.alpha)
	if err := s.WithAccount(ctx, f.alpha, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE reviews SET river_job_id = $2 WHERE id = $1`, f.review, f.silent)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return f
}

// abandoned lists the fixture's abandoned jobs, in order.
func (f rescueFixture) abandoned(t *testing.T, ctx context.Context, s *Store) []AbandonedJob {
	t.Helper()
	got, err := s.AbandonedJobs(ctx, rescueStale, 100)
	if err != nil {
		t.Fatalf("AbandonedJobs: %v", err)
	}
	ids := make([]int64, 0, len(got))
	for _, j := range got {
		ids = append(ids, j.ID)
	}
	if want := []int64{f.silent, f.lastTry, f.cancelled}; !slices.Equal(ids, want) {
		t.Fatalf("abandoned = %v, want %v: no heartbeat for stale, claimed longer ago than that, and still running", ids, want)
	}
	return got
}

// jobRow is what a rescue leaves on River's row.
type jobRow struct {
	state     string
	finalized bool
	soon      bool
	errs      []string
}

func readJob(t *testing.T, ctx context.Context, s *Store, id int64) jobRow {
	t.Helper()
	var r jobRow
	if err := s.owner.QueryRow(ctx, `SELECT state::text, finalized_at IS NOT NULL, scheduled_at > now() - interval '10 seconds',
		coalesce((SELECT array_agg(e->>'error') FROM unnest(errors) e), '{}') FROM river_job WHERE id = $1`, id).
		Scan(&r.state, &r.finalized, &r.soon, &r.errs); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestAbandonedJobs(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	f := newRescueFixture(t, ctx, s)
	got := f.abandoned(t, ctx, s)
	if j := got[0]; j.Kind != "review" || j.Attempt != 1 || j.MaxAttempts != 25 || j.CancelRequested || j.RescueState() != rivertype.JobStateRetryable {
		t.Fatalf("silent job = %+v", j)
	}
	if j := got[1]; j.RescueState() != rivertype.JobStateDiscarded {
		t.Fatalf("last-try job = %+v, want discarded", j)
	}
	if j := got[2]; !j.CancelRequested || j.RescueState() != rivertype.JobStateCancelled {
		t.Fatalf("cancelled job = %+v, want cancelled", j)
	}
}

func TestRescueJob(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	f := newRescueFixture(t, ctx, s)
	got := f.abandoned(t, ctx, s)

	// A beat since the listing keeps the job: its worker is alive after all.
	if err := s.HeartbeatJob(ctx, f.silent); err != nil {
		t.Fatal(err)
	}
	if rescued, err := s.RescueJob(ctx, got[0], rescueStale, "lost"); err != nil || rescued {
		t.Fatalf("RescueJob after a beat = %v, %v; want left alone", rescued, err)
	}
	if _, err := s.owner.Exec(ctx, `UPDATE job_heartbeats SET heartbeat_at = now() - interval '10 minutes' WHERE job_id = $1`, f.silent); err != nil {
		t.Fatal(err)
	}
	for _, j := range got {
		if rescued, err := s.RescueJob(ctx, j, rescueStale, "lost"); err != nil || !rescued {
			t.Fatalf("RescueJob(%d) = %v, %v; want rescued", j.ID, rescued, err)
		}
		if rescued, err := s.RescueJob(ctx, j, rescueStale, "lost"); err != nil || rescued {
			t.Fatalf("second RescueJob(%d) = %v, %v; want nothing left to rescue", j.ID, rescued, err)
		}
	}
	if r := readJob(t, ctx, s, f.silent); r.state != "retryable" || r.finalized || !r.soon || !slices.Equal(r.errs, []string{"lost"}) {
		t.Fatalf("silent job after rescue = %+v, want retryable now with the reason recorded", r)
	}
	if r := readJob(t, ctx, s, f.lastTry); r.state != "discarded" || !r.finalized || len(r.errs) != 1 {
		t.Fatalf("last-try job after rescue = %+v, want discarded", r)
	}
	if r := readJob(t, ctx, s, f.cancelled); r.state != "cancelled" || !r.finalized {
		t.Fatalf("cancelled job after rescue = %+v, want cancelled", r)
	}
	for _, id := range []int64{f.beating, f.fresh, f.done} {
		if r := readJob(t, ctx, s, id); len(r.errs) != 0 {
			t.Fatalf("job %d was touched: %+v", id, r)
		}
	}
	var status, errText string
	if err := s.WithAccount(ctx, f.alpha, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT status, error FROM reviews WHERE id = $1`, f.review).Scan(&status, &errText)
	}); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || errText != "lost" {
		t.Fatalf("the rescued job's review = %s %q, want failed with the reason", status, errText)
	}

	// The beats of jobs no longer running go. A running job keeps its own.
	if n, err := s.SweepJobHeartbeats(ctx); err != nil || n != 2 {
		t.Fatalf("SweepJobHeartbeats = %d, %v; want the rescued and the completed job's beats swept", n, err)
	}
	rows, err := s.owner.Query(ctx, `SELECT job_id FROM job_heartbeats WHERE job_id = ANY($1) ORDER BY job_id`, []int64{f.silent, f.beating, f.fresh, f.done})
	if err != nil {
		t.Fatal(err)
	}
	left, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		t.Fatal(err)
	}
	if want := []int64{f.beating, f.fresh}; !slices.Equal(left, want) {
		t.Fatalf("heartbeats left = %v, want %v", left, want)
	}
}

func TestOrphanedRuns(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, twoAccounts)); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}
	listRepos(t, s, "alpha", "alpha/one", "alpha/two")
	alpha := accountID(t, s, "alpha")
	live := insertRiverJob(t, ctx, s, "review", "running", 1, 25, time.Minute, `{}`)
	retried := insertRiverJob(t, ctx, s, "review", "retryable", 1, 25, 10*time.Minute, `{}`)
	gone := insertRiverJob(t, ctx, s, "index", "discarded", 3, 3, 10*time.Minute, `{}`)
	// A run an earlier attempt of the live job left, which runs anew.
	leftRun, _ := insertRun(t, ctx, s, alpha, insertReview(t, ctx, s, alpha), live)
	if err := s.WithAccount(ctx, alpha, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE runner_runs SET created_at = now() - interval '1 minute' WHERE id = $1`, leftRun)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	liveRun, _ := insertRun(t, ctx, s, alpha, insertReview(t, ctx, s, alpha), live)
	reviewRun, review := insertRun(t, ctx, s, alpha, insertReview(t, ctx, s, alpha), retried)
	indexRun, indexGen := insertRun(t, ctx, s, alpha, "", gone)
	// A run of the retried job that its worker did end.
	endedRun, _ := insertRun(t, ctx, s, alpha, insertReview(t, ctx, s, alpha), retried)
	if err := s.WithAccount(ctx, alpha, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE runner_runs SET finished_at = now() WHERE id = $1`, endedRun)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	got, err := s.OrphanedRuns(ctx, 100)
	if err != nil {
		t.Fatalf("OrphanedRuns: %v", err)
	}
	ids := make([]string, 0, len(got))
	for _, r := range got {
		ids = append(ids, r.ID)
	}
	if want := []string{leftRun, reviewRun, indexRun}; !slices.Equal(ids, want) {
		t.Fatalf("orphaned = %v, want %v: unfinished, of a job not running or with a newer run (not %s or %s)",
			ids, want, liveRun, endedRun)
	}
	if r := got[1]; r.AccountID != alpha || r.Kind != RunnerKindReview || r.ReviewID != review || r.IndexRunID != "" {
		t.Fatalf("review run = %+v", r)
	}
	if r := got[2]; r.Kind != RunnerKindIndex || r.IndexRunID != indexGen || r.ReviewID != "" {
		t.Fatalf("index run = %+v", r)
	}
	for range 2 { // idempotent
		for _, r := range got {
			if err := s.EndOrphanedRun(ctx, r, "lost"); err != nil {
				t.Fatalf("EndOrphanedRun(%s): %v", r.ID, err)
			}
		}
	}
	var phase, runErr, reviewStatus, indexStatus string
	var finished bool
	if err := s.WithAccount(ctx, alpha, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT phase, error, finished_at IS NOT NULL FROM runner_runs WHERE id = $1`, reviewRun).
			Scan(&phase, &runErr, &finished); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT status FROM reviews WHERE id = $1`, review).Scan(&reviewStatus); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT status FROM index_runs WHERE id = $1`, indexGen).Scan(&indexStatus)
	}); err != nil {
		t.Fatal(err)
	}
	if phase != "failed" || runErr != "lost" || !finished || reviewStatus != "failed" || indexStatus != "failed" {
		t.Fatalf("after ending: run %s %q finished=%v, review %s, index run %s; want all failed", phase, runErr, finished, reviewStatus, indexStatus)
	}
	if left, err := s.OrphanedRuns(ctx, 100); err != nil || len(left) != 0 {
		t.Fatalf("OrphanedRuns after ending = %v, %v; want none", left, err)
	}
}

func TestJobHead(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	f := newRescueFixture(t, ctx, s)
	// The job started its review, which StartReview does with a runner run.
	insertRun(t, ctx, s, f.alpha, f.review, f.silent)
	head, ok, err := s.JobHead(ctx, f.silent)
	if err != nil || !ok || head.AccountID != f.alpha || head.Repository == "" || head.HeadSHA == "" {
		t.Fatalf("JobHead = %+v, %v, %v; want the head of the job's review", head, ok, err)
	}
	if _, ok, err := s.JobHead(ctx, f.lastTry); err != nil || ok {
		t.Fatalf("JobHead of a job with no review = %v, %v; want none", ok, err)
	}
	// A review the job ended before a runner is not its head to report.
	if err := s.WithAccount(ctx, f.alpha, func(tx pgx.Tx) error {
		var prID string
		if err := tx.QueryRow(ctx, `SELECT pull_request_id::text FROM reviews WHERE id = $1`, f.review).Scan(&prID); err != nil {
			return err
		}
		_, err := RecordEndedReview(ctx, tx, EndedReview{
			AccountID: f.alpha, PullRequestID: prID, HeadSHA: "ended", Status: ReviewSuperseded, JobID: f.lastTry,
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.JobHead(ctx, f.lastTry); err != nil || ok {
		t.Fatalf("JobHead of a job that only ended a review = %v, %v; want none", ok, err)
	}
	if head.Statusless {
		t.Fatal("the review of an open pull request is statusless")
	}

	// A review that began after its pull request was merged is a look back.
	for _, tt := range []struct {
		closed time.Duration
		want   bool
	}{{-time.Minute, true}, {time.Minute, false}} {
		if _, err := s.owner.Exec(ctx, `UPDATE pull_requests SET state = 'closed', merged = true,
				closed_at = (SELECT created_at FROM reviews WHERE id = $1) + $2 * interval '1 second'
			WHERE id = (SELECT pull_request_id FROM reviews WHERE id = $1)`, f.review, tt.closed.Seconds()); err != nil {
			t.Fatal(err)
		}
		if head, _, err := s.JobHead(ctx, f.silent); err != nil || head.Statusless != tt.want {
			t.Fatalf("closed %s from the review's start: statusless = %v, %v; want %v", tt.closed, head.Statusless, err, tt.want)
		}
	}
}

// TestRecordEndedReviewOncePerJob: a job ends a head's review before its
// runner once, whatever its retry ends it as. A review that it started a
// runner for, another head and another job each record their own. A second
// record of the job waits for the first's transaction.
func TestRecordEndedReviewOncePerJob(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	f := newRescueFixture(t, ctx, s)
	var prID string
	if err := s.owner.QueryRow(ctx, `SELECT pull_request_id::text FROM reviews WHERE id = $1`, f.review).Scan(&prID); err != nil {
		t.Fatal(err)
	}
	// The fixture's review is one f.silent started a runner for, at "abc".
	insertRun(t, ctx, s, f.alpha, f.review, f.silent)
	record := func(job int64, head string, status ReviewStatus) bool {
		t.Helper()
		var recorded bool
		if err := s.WithAccount(ctx, f.alpha, func(tx pgx.Tx) error {
			var err error
			recorded, err = RecordEndedReview(ctx, tx, EndedReview{
				AccountID: f.alpha, PullRequestID: prID, HeadSHA: head, Status: status, JobID: job,
			})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return recorded
	}
	for _, tt := range []struct {
		what   string
		job    int64
		head   string
		status ReviewStatus
		want   bool
	}{
		{"the job's first end", f.lastTry, "def", ReviewFailed, true},
		{"its retry ending otherwise", f.lastTry, "def", ReviewSkipped, false},
		{"its end of another head", f.lastTry, "ghi", ReviewSuperseded, true},
		{"another job's end", f.beating, "def", ReviewSkipped, true},
		{"the end of a head the job started a runner for", f.silent, "abc", ReviewSuperseded, true},
	} {
		if got := record(tt.job, tt.head, tt.status); got != tt.want {
			t.Fatalf("%s recorded = %v, want %v", tt.what, got, tt.want)
		}
	}
	err := s.WithAccount(ctx, f.alpha, func(tx pgx.Tx) error {
		if _, err := RecordEndedReview(ctx, tx, EndedReview{AccountID: f.alpha, PullRequestID: prID, HeadSHA: "jkl",
			Status: ReviewSkipped, JobID: f.fresh}); err != nil {
			return err
		}
		blocked := s.WithAccount(ctx, f.alpha, func(other pgx.Tx) error {
			if _, err := other.Exec(ctx, `SET LOCAL lock_timeout = '50ms'`); err != nil {
				return err
			}
			_, err := RecordEndedReview(ctx, other, EndedReview{AccountID: f.alpha, PullRequestID: prID, HeadSHA: "jkl",
				Status: ReviewSkipped, JobID: f.fresh})
			return err
		})
		if pgerr, ok := errors.AsType[*pgconn.PgError](blocked); !ok || pgerr.Code != "55P03" {
			t.Fatalf("a second record of the job = %v, want it to wait for the first", blocked)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestRecordRunnerRunLeavesUnknownTimesNull records a run whose pod never
// scheduled or started, in a session whose time zone is not UTC. It makes
// sure that the times it never learned are NULL rather than the zero time.
func TestRecordRunnerRunLeavesUnknownTimesNull(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, soloAccount("unstarted"))); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}
	listRepos(t, s, "unstarted", "unstarted/one")
	account := accountID(t, s, "unstarted")
	runID, _ := insertRun(t, ctx, s, account, insertReview(t, ctx, s, account), 0)
	var scheduled, started *time.Time
	var phase string
	err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SET LOCAL TIME ZONE 'Asia/Tokyo'`); err != nil {
			return err
		}
		if err := RecordRunnerRun(ctx, tx, runID, RunnerResult{JobName: "kritika-run-x", Error: "never started"}); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT scheduled_at, started_at, phase FROM runner_runs WHERE id = $1`, runID).
			Scan(&scheduled, &started, &phase)
	})
	if err != nil {
		t.Fatal(err)
	}
	if scheduled != nil || started != nil || phase != "failed" {
		t.Fatalf("scheduled_at = %v, started_at = %v, phase = %s; want both NULL and the run failed", scheduled, started, phase)
	}
}

func TestRescuePublication(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, twoAccounts)); err != nil {
		t.Fatal(err)
	}
	listRepos(t, s, "alpha", "alpha/one", "alpha/two")
	account := accountID(t, s, "alpha")
	for _, tt := range []struct {
		name                 string
		attempt, maxAttempts int
		cancel               bool
		want                 ReviewStatus
	}{
		{name: "retry keeps its checkpoint prepared", attempt: 1, maxAttempts: 8, want: ReviewPrepared},
		{name: "last attempt ends the review", attempt: 8, maxAttempts: 8, want: ReviewFailed},
		{name: "cancel ends the review", attempt: 1, maxAttempts: 8, cancel: true, want: ReviewFailed},
	} {
		t.Run(tt.name, func(t *testing.T) {
			id := insertReview(t, ctx, s, account)
			metadata := `{}`
			if tt.cancel {
				metadata = `{"cancel_attempted_at":"2026-01-01T00:00:00Z"}`
			}
			jobID := insertRiverJob(t, ctx, s, "review", "running", tt.attempt, tt.maxAttempts, 10*time.Minute, metadata)
			if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
				_, err := tx.Exec(ctx, `UPDATE reviews SET status = 'prepared', publication = '{"Version":1}', river_job_id = $2 WHERE id = $1`, id, jobID)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			ok, err := s.RescueJob(ctx, AbandonedJob{ID: jobID, Kind: "review", Attempt: tt.attempt, MaxAttempts: tt.maxAttempts, CancelRequested: tt.cancel}, rescueStale, "lost heartbeat")
			if err != nil || !ok {
				t.Fatalf("rescue = %v, %v", ok, err)
			}
			if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
				p, err := ReadPublication(ctx, tx, jobID)
				if err == nil && p.Status != tt.want {
					t.Fatalf("status = %s, want %s", p.Status, tt.want)
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestRescueByTheOwnersWindow: a job is abandoned by the window that its
// owner's last beat carries, not the leader's, so that replicas on
// different heartbeat settings during a rollout never rescue a live job. A
// beat that carries no window, from a replica before the column, is judged
// by the fixed window of that replica.
func TestRescueByTheOwnersWindow(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	beat := func(id int64, interval time.Duration, beats int, ago time.Duration) {
		t.Helper()
		tuning.Override(t, func(v *tuning.Values) { v.JobTimeout.HeartbeatInterval, v.JobTimeout.AbandonedBeats = interval, beats })
		if err := s.HeartbeatJob(ctx, id); err != nil {
			t.Fatal(err)
		}
		if _, err := s.owner.Exec(ctx, `UPDATE job_heartbeats SET heartbeat_at = now() - make_interval(secs => $2),
			abandon_after_at = now() - make_interval(secs => $2) WHERE job_id = $1`,
			id, ago.Seconds()); err != nil {
			t.Fatal(err)
		}
	}
	// A slow owner, 6m to abandonment, 3m silent: alive to a leader whose
	// own window is 1m.
	slow := insertRiverJob(t, ctx, s, "review", "running", 1, 25, 10*time.Minute, `{}`)
	beat(slow, time.Minute, 6, 3*time.Minute)
	// A fast owner, 30s to abandonment, 2m silent: abandoned to a leader
	// whose own window is 3m.
	fast := insertRiverJob(t, ctx, s, "review", "running", 1, 25, 10*time.Minute, `{}`)
	beat(fast, 10*time.Second, 3, 2*time.Minute)
	// Beats that carry no window, from replicas before it was recorded,
	// whose window was the fixed 3m: 2m silent, alive whatever the leader's
	// window, and 4m silent, abandoned.
	legacy := insertRiverJob(t, ctx, s, "review", "running", 1, 25, 10*time.Minute, `{}`)
	beat(legacy, time.Minute, 6, 2*time.Minute)
	legacyGone := insertRiverJob(t, ctx, s, "review", "running", 1, 25, 10*time.Minute, `{}`)
	beat(legacyGone, time.Minute, 6, 4*time.Minute)
	if _, err := s.owner.Exec(ctx, `UPDATE job_heartbeats SET abandon_after = DEFAULT, abandon_after_at = NULL WHERE job_id = ANY($1)`, []int64{legacy, legacyGone}); err != nil {
		t.Fatal(err)
	}
	ours := []int64{slow, fast, legacy, legacyGone}
	abandoned := func(stale time.Duration) []int64 {
		t.Helper()
		got, err := s.AbandonedJobs(ctx, stale, 100)
		if err != nil {
			t.Fatal(err)
		}
		var ids []int64
		for _, j := range got {
			if slices.Contains(ours, j.ID) {
				ids = append(ids, j.ID)
			}
		}
		return ids
	}
	if got, want := abandoned(time.Minute), []int64{fast, legacyGone}; !slices.Equal(got, want) {
		t.Fatalf("abandoned to a 1m leader = %v, want %v: the slow owner's 6m window and the legacy 3m have not passed", got, want)
	}
	if got, want := abandoned(3*time.Minute), []int64{fast, legacyGone}; !slices.Equal(got, want) {
		t.Fatalf("abandoned to a 3m leader = %v, want %v: the fast owner's 30s window and the legacy 3m have passed", got, want)
	}
	// The rescue's own check judges as the listing does.
	if rescued, err := s.RescueJob(ctx, AbandonedJob{ID: slow, Kind: "review", Attempt: 1, MaxAttempts: 25}, time.Minute, "lost"); err != nil || rescued {
		t.Fatalf("RescueJob of the slow owner's job = %v, %v; want it left alone", rescued, err)
	}
	if rescued, err := s.RescueJob(ctx, AbandonedJob{ID: legacy, Kind: "review", Attempt: 1, MaxAttempts: 25}, time.Minute, "lost"); err != nil || rescued {
		t.Fatalf("RescueJob of the legacy beat 2m silent = %v, %v; want it left alone under a 1m leader", rescued, err)
	}
	if rescued, err := s.RescueJob(ctx, AbandonedJob{ID: fast, Kind: "review", Attempt: 1, MaxAttempts: 25}, 3*time.Minute, "lost"); err != nil || !rescued {
		t.Fatalf("RescueJob of the fast owner's job = %v, %v; want it rescued", rescued, err)
	}
}

// TestStartJob: the horizon that a replica records on its attempt's
// row is the one River's rescuer reads back from the row, whatever the
// reading replica's settings, and only for that attempt.
func TestStartJob(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	id := insertRiverJob(t, ctx, s, "review", "running", 1, 25, 10*time.Minute, `{"kept": true}`)
	read := func() *rivertype.JobRow {
		t.Helper()
		var r rivertype.JobRow
		r.ID = id
		if err := s.owner.QueryRow(ctx, `SELECT attempted_at, metadata FROM river_job WHERE id = $1`, id).Scan(&r.AttemptedAt, &r.Metadata); err != nil {
			t.Fatal(err)
		}
		return &r
	}
	claimed := read()
	if _, ok := JobRescueAfter(claimed); ok {
		t.Fatal("a row as claimed carries a rescue horizon")
	}
	// The working replica's maxJobTimeout 6h, with its 1h grace.
	tuning.Override(t, func(v *tuning.Values) { v.JobTimeout.MaxJobTimeout = 6 * time.Hour })
	if _, err := s.StartJob(ctx, claimed); err != nil {
		t.Fatal(err)
	}
	// The rescuer's own settings are the defaults, a 4h horizon.
	tuning.Override(t, func(v *tuning.Values) { v.JobTimeout.MaxJobTimeout = 3 * time.Hour })
	got := read()
	if after, ok := JobRescueAfter(got); !ok || after != 7*time.Hour {
		t.Fatalf("JobRescueAfter = %s, %v; want the working replica's 7h", after, ok)
	}
	if !strings.Contains(string(got.Metadata), `"kept"`) {
		t.Fatalf("metadata = %s, want the keys it held kept", got.Metadata)
	}
	// A later attempt is claimed anew: the record does not match it.
	if _, err := s.owner.Exec(ctx, `UPDATE river_job SET attempted_at = now() WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if after, ok := JobRescueAfter(read()); ok {
		t.Fatalf("JobRescueAfter of a later attempt = %s, want no record", after)
	}
}

// TestStartJobOfARescuedAttempt: an attempt that the leader rescued before
// its replica took it does not start, and stamps no beat.
func TestStartJobOfARescuedAttempt(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	id := insertRiverJob(t, ctx, s, "review", "running", 1, 25, 10*time.Minute, `{}`)
	var claimed rivertype.JobRow
	claimed.ID = id
	if err := s.owner.QueryRow(ctx, `SELECT attempted_at, metadata FROM river_job WHERE id = $1`, id).Scan(&claimed.AttemptedAt, &claimed.Metadata); err != nil {
		t.Fatal(err)
	}
	if rescued, err := s.RescueJob(ctx, AbandonedJob{ID: id, Kind: "review", Attempt: 1, MaxAttempts: 25}, time.Minute, "lost"); err != nil || !rescued {
		t.Fatalf("RescueJob = %v, %v", rescued, err)
	}
	if _, err := s.StartJob(ctx, &claimed); !errors.Is(err, ErrJobNotRunning) {
		t.Fatalf("StartJob of a rescued attempt = %v, want ErrJobNotRunning", err)
	}
	var beats int
	if err := s.owner.QueryRow(ctx, `SELECT count(*) FROM job_heartbeats WHERE job_id = $1`, id).Scan(&beats); err != nil || beats != 0 {
		t.Fatalf("beats = %d, %v; want none", beats, err)
	}
}

// TestLegacyBeatOverATunedWindow: a beat of a replica from before the window
// was written, which updates heartbeat_at alone, is judged by 180 seconds,
// not by the window that an earlier attempt's beat left on the row.
func TestLegacyBeatOverATunedWindow(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	id := insertRiverJob(t, ctx, s, "review", "running", 1, 25, 10*time.Minute, `{}`)
	tuning.Override(t, func(v *tuning.Values) { v.JobTimeout.HeartbeatInterval, v.JobTimeout.AbandonedBeats = 4*time.Second, 3 })
	if err := s.HeartbeatJob(ctx, id); err != nil {
		t.Fatal(err)
	}
	// The pre-upgrade upsert, which writes heartbeat_at alone, dated 30s
	// back: past the 12s window, within the legacy 180s.
	if _, err := s.app.Exec(ctx, `INSERT INTO job_heartbeats (job_id, heartbeat_at) VALUES ($1, now() - interval '30 seconds')
		ON CONFLICT (job_id) DO UPDATE SET heartbeat_at = excluded.heartbeat_at`, id); err != nil {
		t.Fatal(err)
	}
	got, err := s.AbandonedJobs(ctx, time.Minute, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, j := range got {
		if j.ID == id {
			t.Fatal("a legacy beat 30s old is abandoned by an earlier beat's 12s window")
		}
	}
}

// TestRescueWaitsOutAStartingAttempt: a rescue that starts while an
// attempt's first beat is being written, before it commits, sees the beat
// once it does, and leaves the attempt running.
func TestRescueWaitsOutAStartingAttempt(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	id := insertRiverJob(t, ctx, s, "review", "running", 1, 25, 10*time.Minute, `{}`)
	tx, err := s.app.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `UPDATE river_job SET metadata = metadata || '{}' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if err := heartbeatJob(ctx, tx, id); err != nil {
		t.Fatal(err)
	}
	type result struct {
		rescued bool
		err     error
	}
	done := make(chan result, 1)
	go func() {
		rescued, err := s.RescueJob(ctx, AbandonedJob{ID: id, Kind: "review", Attempt: 1, MaxAttempts: 25}, time.Minute, "lost")
		done <- result{rescued, err}
	}()
	waitBlockedBy(t, ctx, s, backendPID(t, ctx, tx), done)
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if r := <-done; r.err != nil || r.rescued {
		t.Fatalf("RescueJob = %v, %v; want the attempt left running", r.rescued, r.err)
	}
	if r := readJob(t, ctx, s, id); r.state != "running" {
		t.Fatalf("job = %+v, want running", r)
	}
}

// TestStartJobStampsAfterTheLock: StartJob's first beat is dated after it
// got the job's row, not when its transaction began, which a wait on the
// row can leave older than the window.
func TestStartJobStampsAfterTheLock(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	id := insertRiverJob(t, ctx, s, "review", "running", 1, 25, 10*time.Minute, `{}`)
	var job rivertype.JobRow
	job.ID = id
	if err := s.owner.QueryRow(ctx, `SELECT attempted_at FROM river_job WHERE id = $1`, id).Scan(&job.AttemptedAt); err != nil {
		t.Fatal(err)
	}
	hold, err := s.owner.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = hold.Rollback(ctx) }()
	if _, err := hold.Exec(ctx, `SELECT 1 FROM river_job WHERE id = $1 FOR UPDATE`, id); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := s.StartJob(ctx, &job)
		done <- err
	}()
	waitBlockedBy(t, ctx, s, backendPID(t, ctx, hold), done)
	var released time.Time
	if err := s.owner.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&released); err != nil {
		t.Fatal(err)
	}
	if err := hold.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	var beat time.Time
	if err := s.owner.QueryRow(ctx, `SELECT heartbeat_at FROM job_heartbeats WHERE job_id = $1`, id).Scan(&beat); err != nil {
		t.Fatal(err)
	}
	if beat.Before(released) {
		t.Fatalf("first beat at %v, before the lock was released at %v", beat, released)
	}
}

// TestOpenRefusesAOneConnectionOwnerPool: an owner pool of one connection,
// which the leader lock would hold for its whole tenure, is refused as a
// configuration error, whichever of the DSN or tuning sets it.
func TestOpenRefusesAOneConnectionOwnerPool(t *testing.T) {
	ctx := t.Context()
	owner, err := url.Parse(testEnv(t, "KRITIKA_TEST_OWNER_URL"))
	if err != nil {
		t.Fatal(err)
	}
	q := owner.Query()
	q.Set("pool_max_conns", "1")
	owner.RawQuery = q.Encode()
	opts := Options{AppURL: testEnv(t, "KRITIKA_TEST_APP_URL"), OwnerURL: owner.String(), Logger: slog.New(slog.DiscardHandler)}
	if _, err := Open(ctx, opts); !errors.Is(err, ErrOwnerPoolTooSmall) || !IsConfigurationError(err) {
		t.Fatalf("Open with pool_max_conns=1 = %v, want ErrOwnerPoolTooSmall", err)
	}
	tuning.Override(t, func(v *tuning.Values) { v.Store.OwnerPoolMaxConns = 1 })
	opts.OwnerURL = testEnv(t, "KRITIKA_TEST_OWNER_URL")
	if _, err := Open(ctx, opts); !errors.Is(err, ErrOwnerPoolTooSmall) {
		t.Fatalf("Open with ownerPoolMaxConns 1 = %v, want ErrOwnerPoolTooSmall", err)
	}
}

// backendPID is the Postgres backend that tx runs on.
func backendPID(t *testing.T, ctx context.Context, tx pgx.Tx) uint32 {
	t.Helper()
	var pid uint32
	if err := tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	return pid
}

// waitBlockedBy waits until a backend waits on pid's locks, and fails if done
// reports first that what was to wait ended, or ten seconds pass.
func waitBlockedBy[T any](t *testing.T, ctx context.Context, s *Store, pid uint32, done <-chan T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		var blocked bool
		if err := s.owner.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE $1 = ANY(pg_blocking_pids(pid)))`,
			pid).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			return
		}
		select {
		case r := <-done:
			t.Fatalf("ended without waiting on backend %d: %v", pid, r)
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatalf("nothing waited on backend %d", pid)
		}
	}
}
