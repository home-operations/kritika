//go:build integration

package worker

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertest"
	"github.com/riverqueue/river/rivertype"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/jobs"
	"github.com/home-operations/kritika/internal/store"
)

func filteredPreparation(t *testing.T, number int) (*agenticHarness, *river.Job[jobs.ReviewArgs], *pullRequest, string, string) {
	t.Helper()
	h := newAgenticHarness(t, number)
	job := queuedOutcomeReview(t, h)
	pr, err := loadPullRequest(h.ctx, h.st, h.account.ID(), job.Args.RepositoryID, h.number)
	if err != nil {
		t.Fatal(err)
	}
	started, _, err := h.review.start(h.ctx, job.Args, pr, h.base, "", job.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	reviewID, runID := started.ReviewID, started.RunID
	err = h.st.WithAccount(h.ctx, h.account.ID(), func(tx pgx.Tx) error {
		if _, err := tx.Exec(h.ctx, `UPDATE river_job SET state = 'running', attempt = 1, attempted_at = now() WHERE id = $1`, job.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(h.ctx, `UPDATE pull_requests SET labels = '[]' WHERE id = $1`, pr.id); err != nil {
			return err
		}
		_, err := tx.Exec(h.ctx, `INSERT INTO context_packs (runner_run_id, account_id, head_sha, base_sha, patch_id, diff, skip_reason)
			VALUES ($1, $2, $3, $4, 'patch', '', 'filtered')`, runID, h.account.ID(), h.head, h.base)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	job.State = rivertype.JobStateRunning
	return h, job, pr, reviewID, runID
}

func TestFilteredPreparation(t *testing.T) {
	for _, tt := range []struct {
		name, labels string
		number       int
		// available has the job no longer running as River's client sees it;
		// moved has River's row moved on, to a rescue or a newer attempt.
		available       bool
		movedState      rivertype.JobState
		movedAttempt    int
		wantErr         bool
		wantStatus      store.ReviewStatus
		wantJob         rivertype.JobState
		wantAttempt     int
		wantDisposition reviewDisposition
	}{
		{name: "stable", labels: `[]`, number: 986,
			wantStatus: store.ReviewSkipped, wantJob: rivertype.JobStateCompleted, wantAttempt: 1, wantDisposition: reviewFinish},
		{name: "changed", labels: `["review"]`, number: 987,
			wantStatus: store.ReviewSkipped, wantJob: rivertype.JobStateRunning, wantAttempt: 1, wantDisposition: reviewSnooze},
		{name: "completion failure rolls back skip", labels: `[]`, number: 988, available: true, wantErr: true,
			wantStatus: store.ReviewRunning, wantJob: rivertype.JobStateRunning, wantAttempt: 1},
		{name: "a rescued attempt rolls back skip", labels: `[]`, number: 1010,
			movedState: rivertype.JobStateRetryable, movedAttempt: 1, wantErr: true,
			wantStatus: store.ReviewRunning, wantJob: rivertype.JobStateRetryable, wantAttempt: 1},
		{name: "a newer attempt rolls back skip", labels: `[]`, number: 1011,
			movedState: rivertype.JobStateRunning, movedAttempt: 2, wantErr: true,
			wantStatus: store.ReviewRunning, wantJob: rivertype.JobStateRunning, wantAttempt: 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h, job, pr, reviewID, runID := filteredPreparation(t, tt.number)
			if err := h.st.WithAccount(h.ctx, h.account.ID(), func(tx pgx.Tx) error {
				if _, err := tx.Exec(h.ctx, `UPDATE pull_requests SET labels = $2 WHERE id = $1`, pr.id, tt.labels); err != nil {
					return err
				}
				if tt.movedState == "" {
					return nil
				}
				_, err := tx.Exec(h.ctx, `UPDATE river_job SET state = $2::text::river_job_state, attempt = $3 WHERE id = $1`,
					job.ID, tt.movedState, tt.movedAttempt)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if tt.available {
				job.State = rivertype.JobStateAvailable
			}
			ctx := rivertest.WorkContext(h.ctx, h.insertOnly)
			_, decision, err := h.review.recordPreparation(ctx, job, pr.id, reviewID, runID, "", json.RawMessage(`[]`), configfile.Confidence{})
			if (err != nil) != tt.wantErr {
				t.Fatalf("preparation = %v", err)
			}
			if err == nil {
				checkReviewDecision(t, decision, reviewDecision{end: store.ReviewEnd{Status: store.ReviewSkipped, SkipReason: "filtered", PatchID: "patch"}, disposition: tt.wantDisposition})
			}
			if err := h.st.WithAccount(h.ctx, h.account.ID(), func(tx pgx.Tx) error {
				var status store.ReviewStatus
				var state rivertype.JobState
				var attempt int
				if err := tx.QueryRow(h.ctx, `SELECT r.status, j.state, j.attempt FROM reviews r JOIN river_job j ON j.id = r.river_job_id WHERE r.id = $1`, reviewID).Scan(&status, &state, &attempt); err != nil {
					return err
				}
				if status != tt.wantStatus || state != tt.wantJob || attempt != tt.wantAttempt {
					t.Fatalf("review=%s job=%s attempt=%d", status, state, attempt)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestFilteredPreparationConcurrentLabelChange(t *testing.T) {
	h, job, pr, reviewID, runID := filteredPreparation(t, 989)
	ctx, cancel := context.WithTimeout(rivertest.WorkContext(h.ctx, h.insertOnly), 10*time.Second)
	defer cancel()
	owner, observer := publicationTestOwner(t), publicationTestOwner(t)
	tx, err := owner.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := tx.Rollback(context.Background()); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			t.Error(err)
		}
	}()
	if _, err := tx.Exec(ctx, `SELECT id FROM river_job WHERE id = $1 FOR UPDATE`, job.ID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, _, err := h.review.recordPreparation(ctx, job, pr.id, reviewID, runID, "", json.RawMessage(`[]`), configfile.Confidence{})
		done <- err
	}()
	waitFor(t, 5*time.Second, "skip waiting to complete its River job", func() bool {
		var blocked bool
		if err := observer.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE $1 = ANY(pg_blocking_pids(pid)))`, owner.PgConn().PID()).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		return blocked
	})
	changed := make(chan error, 1)
	go func() {
		changed <- h.st.WithAccount(ctx, h.account.ID(), func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `UPDATE pull_requests SET labels = '["review"]' WHERE id = $1`, pr.id); err != nil {
				return err
			}
			_, err := jobs.EnqueueLabelChange(ctx, tx, h.insertOnly, h.account.ID(), job.Args.RepositoryID, h.number, "labeled")
			return err
		})
	}()
	waitFor(t, 5*time.Second, "label update waiting on skip transaction", func() bool {
		var blocked bool
		if err := observer.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity worker, pg_stat_activity labels
			WHERE $1 = ANY(pg_blocking_pids(worker.pid)) AND worker.pid = ANY(pg_blocking_pids(labels.pid)))`, owner.PgConn().PID()).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		return blocked
	})
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	for _, result := range []<-chan error{done, changed} {
		select {
		case err := <-result:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal(context.Cause(ctx))
		}
	}
	if err := h.st.WithAccount(ctx, h.account.ID(), func(tx pgx.Tx) error {
		var completed bool
		var queued int
		if err := tx.QueryRow(ctx, `SELECT state = 'completed',
			(SELECT count(*) FROM river_job WHERE kind = 'review' AND id <> $1 AND state = 'available' AND (args->>'number')::int = $2)
			FROM river_job WHERE id = $1`, job.ID, h.number).Scan(&completed, &queued); err != nil {
			return err
		}
		if !completed || queued != 1 {
			t.Fatalf("completed=%v queued=%d; label change was lost", completed, queued)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPreparationWaitsForCurrentHead(t *testing.T) {
	h, job, pr, reviewID, runID := filteredPreparation(t, 1003)
	ctx, cancel := context.WithTimeout(rivertest.WorkContext(h.ctx, h.insertOnly), 10*time.Second)
	defer cancel()
	owner, observer := publicationTestOwner(t), publicationTestOwner(t)
	tx, err := owner.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := tx.Rollback(context.Background()); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			t.Error(err)
		}
	}()
	if _, err := tx.Exec(ctx, `UPDATE pull_requests SET head_sha = 'new-head' WHERE id = $1`, pr.id); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	var decision reviewDecision
	go func() {
		_, d, err := h.review.recordPreparation(ctx, job, pr.id, reviewID, runID, "", json.RawMessage(`[]`), configfile.Confidence{})
		decision = d
		done <- err
	}()
	waitFor(t, 5*time.Second, "preparation waiting on ingest", func() bool {
		var blocked bool
		if err := observer.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE $1 = ANY(pg_blocking_pids(pid)))`, owner.PgConn().PID()).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		return blocked
	})
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if decision.end.Status != store.ReviewSuperseded {
		t.Fatalf("preparation decided %s from a stale head", decision.end.Status)
	}
	row, err := h.insertOnly.JobGet(ctx, job.ID)
	if err != nil || row.State != rivertype.JobStateRunning {
		t.Fatalf("old-head job was completed by the filter: %+v, %v", row, err)
	}
}

func TestFilteredPreparationCancelAfterShutdown(t *testing.T) {
	h, job, pr, reviewID, runID := filteredPreparation(t, 1004)
	jobCtx, stop := context.WithCancelCause(h.ctx)
	stop(errors.New("shutdown"))
	ctx, cancel := preparationContext(rivertest.WorkContext(jobCtx, h.insertOnly))
	defer cancel()
	if err := h.st.WithAccount(h.ctx, h.account.ID(), func(tx pgx.Tx) error {
		return jobs.RequestCancel(h.ctx, tx, h.insertOnly, reviewID)
	}); err != nil {
		t.Fatal(err)
	}
	_, decision, err := h.review.recordPreparation(ctx, job, pr.id, reviewID, runID, "", json.RawMessage(`[]`), configfile.Confidence{})
	if err != nil || decision.end.Status != store.ReviewCanceled {
		t.Fatalf("preparation = %s, %v", decision.end.Status, err)
	}
	row, err := h.insertOnly.JobGet(h.ctx, job.ID)
	if err != nil || row.State == rivertype.JobStateCompleted {
		t.Fatalf("cancelled job was completed by the filter: %+v, %v", row, err)
	}
}
