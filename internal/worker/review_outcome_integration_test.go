//go:build integration

package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/home-operations/kritika/internal/forge"
	"github.com/home-operations/kritika/internal/jobs"
	"github.com/home-operations/kritika/internal/store"
)

func queuedOutcomeReview(t *testing.T, h *agenticHarness) *river.Job[jobs.ReviewArgs] {
	t.Helper()
	if err := h.queue.Stop(h.ctx); err != nil {
		t.Fatal(err)
	}
	h.dispatch(t, h.head)
	var id int64
	if err := h.st.WithAccount(h.ctx, h.account.ID(), func(tx pgx.Tx) error {
		return tx.QueryRow(h.ctx, `SELECT id FROM river_job WHERE kind = 'review' AND (args->>'number')::int = $1`, h.number).Scan(&id)
	}); err != nil {
		t.Fatal(err)
	}
	row, err := h.insertOnly.JobGet(h.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	job := &river.Job[jobs.ReviewArgs]{JobRow: row}
	if err := json.Unmarshal(row.EncodedArgs, &job.Args); err != nil {
		t.Fatal(err)
	}
	job.Attempt = 1
	return job
}

func TestReviewLastAttemptSoftStop(t *testing.T) {
	h := newAgenticHarness(t, 981)
	job := queuedOutcomeReview(t, h)
	if _, err := h.st.App().Exec(h.ctx, `UPDATE river_job SET max_attempts = 1 WHERE id = $1`, job.ID); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	h.review.Executor = &blockingExecutor{started: started}
	h.review.rowWait = time.Millisecond
	if err := h.queue.Start(h.ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("runner did not start")
	}
	if err := h.queue.StopAndCancel(h.ctx); err != nil {
		t.Fatal(err)
	}
	row, err := h.insertOnly.JobGet(h.ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.State != rivertype.JobStateAvailable || row.Attempt != 0 {
		t.Fatalf("job = %s, attempt %d; want available with refunded attempt", row.State, row.Attempt)
	}
	_, status, _ := h.waitReview(t, h.head)
	if status != "superseded" {
		t.Fatalf("review = %s", status)
	}
	h.lf.mu.Lock()
	defer h.lf.mu.Unlock()
	if h.lf.status != "pending: kritika: review running" || h.lf.pullReactions[h.number][forge.ReactionFailed] {
		t.Fatalf("refunded attempt reported as failure: %s, %v", h.lf.status, h.lf.pullReactions[h.number])
	}
}

func TestPublicationSetupInterruption(t *testing.T) {
	h := newAgenticHarness(t, 982)
	h.sm.reset(scriptSubmit)
	h.dispatch(t, h.head)
	id, status, why := h.waitReview(t, h.head)
	if status != "completed" {
		t.Fatalf("review = %s: %s", status, why)
	}
	waitPublicationJob(t, h, id)
	for _, tt := range []struct {
		name  string
		cause error
		want  store.ReviewStatus
		why   string
	}{
		{name: "timeout", cause: context.DeadlineExceeded, want: store.ReviewFailed, why: "review timed out"},
		{name: "cancel", cause: river.ErrJobCancelledRemotely, want: store.ReviewCanceled},
	} {
		t.Run(tt.name, func(t *testing.T) {
			worker, job := publicationReplay(t, h, id, json.RawMessage(`{"Version":1}`))
			job.Attempt = job.MaxAttempts
			ctx, cancel := context.WithCancelCause(h.ctx)
			defer cancel(nil)
			worker.Forges = clientsFunc(func(string) (forge.Client, error) {
				cancel(tt.cause)
				return nil, context.Canceled
			})
			if err := worker.Work(ctx, job); err != nil {
				t.Fatalf("Work = %v, want no retry", err)
			}
			if err := h.st.WithAccount(h.ctx, h.account.ID(), func(tx pgx.Tx) error {
				var status store.ReviewStatus
				var why string
				if err := tx.QueryRow(h.ctx, `SELECT status, error FROM reviews WHERE id = $1`, id).Scan(&status, &why); err != nil {
					return err
				}
				if status != tt.want || !strings.Contains(why, tt.why) {
					t.Fatalf("review = %s: %s, want %s: %s", status, why, tt.want, tt.why)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestUnstartedReviewLogsRunnerFailure(t *testing.T) {
	h := newAgenticHarness(t, 983)
	job := queuedOutcomeReview(t, h)
	var logs bytes.Buffer
	h.review.Logger = slog.New(slog.NewJSONHandler(&logs, nil))
	h.exec.unstarted = errors.New("image registry unavailable")
	if err := h.review.Work(h.ctx, job); err == nil {
		t.Fatal("unstarted runner should retry")
	}
	if !strings.Contains(logs.String(), `"runner_error":"image registry unavailable"`) || !strings.Contains(logs.String(), `"attempt":1`) {
		t.Fatalf("runner diagnostics missing: %s", &logs)
	}
}

func TestSuccessfulRunnerInterruptedBeforeCheckpoint(t *testing.T) {
	h := newAgenticHarness(t, 984)
	job := queuedOutcomeReview(t, h)
	h.sm.reset(scriptSubmit)
	ctx, cancel := context.WithCancelCause(h.ctx)
	defer cancel(nil)
	stopped := errors.New("stop initiated")
	h.exec.after = func() { cancel(stopped) }
	if err := h.review.Work(ctx, job); !errors.Is(err, stopped) {
		t.Fatalf("Work = %v, want retry after interruption", err)
	}
	id, status, _ := h.waitReview(t, h.head)
	if status != "superseded" {
		t.Fatalf("review = %s", status)
	}
	if err := h.st.WithAccount(h.ctx, h.account.ID(), func(tx pgx.Tx) error {
		var checkpointed bool
		if err := tx.QueryRow(h.ctx, `SELECT publication IS NOT NULL FROM reviews WHERE id = $1`, id).Scan(&checkpointed); err != nil {
			return err
		}
		if checkpointed {
			t.Fatal("unexpected checkpoint before afterRun")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestReviewCancelDuringFailedRunRecording(t *testing.T) {
	h := newAgenticHarness(t, 985)
	job := queuedOutcomeReview(t, h)
	h.sm.reset(scriptSubmit)
	owner, observer := publicationTestOwner(t), publicationTestOwner(t)
	var logs bytes.Buffer
	h.review.Logger = slog.New(slog.NewJSONHandler(&logs, nil))
	ctx, cancel := context.WithCancelCause(h.ctx)
	defer cancel(nil)
	locked := make(chan struct{})
	var tx pgx.Tx
	var lockErr error
	h.exec.after = func() {
		defer close(locked)
		_, lockErr = owner.Exec(h.ctx, `CREATE FUNCTION reject_record_test() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'reject_record_test'; END $$;
CREATE TRIGGER reject_record_test AFTER UPDATE OF job_name ON runner_runs FOR EACH ROW EXECUTE FUNCTION reject_record_test()`)
		if lockErr != nil {
			return
		}
		tx, lockErr = owner.Begin(h.ctx)
		if lockErr == nil {
			var runID string
			lockErr = tx.QueryRow(h.ctx, `SELECT id FROM runner_runs WHERE river_job_id = $1 FOR UPDATE`, job.ID).Scan(&runID)
		}
	}
	t.Cleanup(func() {
		if _, err := owner.Exec(context.Background(), `DROP TRIGGER IF EXISTS reject_record_test ON runner_runs;
DROP FUNCTION IF EXISTS reject_record_test()`); err != nil {
			t.Error(err)
		}
	})
	done := make(chan error, 1)
	go func() { done <- h.review.Work(ctx, job) }()
	select {
	case <-locked:
	case err := <-done:
		t.Fatalf("Work ended before recordRun: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("runner did not finish")
	}
	defer func() {
		if tx != nil {
			if err := tx.Rollback(context.Background()); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
				t.Error(err)
			}
		}
	}()
	if lockErr != nil {
		t.Fatal(lockErr)
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.After(10 * time.Second)
	for {
		var waiting bool
		if err := observer.QueryRow(h.ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE $1 = ANY(pg_blocking_pids(pid)))`, owner.PgConn().PID()).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case <-ticker.C:
		case err := <-done:
			t.Fatalf("Work ended before blocking on recordRun: %v", err)
		case <-deadline:
			t.Fatal("recordRun did not wait on the locked row")
		}
	}
	cancel(river.ErrJobCancelledRemotely)
	if err := tx.Rollback(h.ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Work = %v, want canceled without retry", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Work did not finish after recordRun failed")
	}
	if !strings.Contains(logs.String(), "reject_record_test") {
		t.Fatalf("recordRun failure was not exercised: %s", &logs)
	}
	_, status, _ := h.waitReview(t, h.head)
	if status != "canceled" {
		t.Fatalf("review = %s, want canceled", status)
	}
	h.lf.mu.Lock()
	defer h.lf.mu.Unlock()
	if h.lf.status != "error: kritika: review canceled" {
		t.Fatal(h.lf.status)
	}
}
