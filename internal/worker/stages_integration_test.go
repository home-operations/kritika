//go:build integration

package worker

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertest"
	"github.com/riverqueue/river/rivertype"

	"github.com/home-operations/kritika/internal/forge"
	"github.com/home-operations/kritika/internal/jobs"
	"github.com/home-operations/kritika/internal/metrics"
	"github.com/home-operations/kritika/internal/store"
)

// workStages drives durable handoffs while letting interruption tests control
// the context of each stage. The harness queue must be stopped first.
func (h *agenticHarness) workStages(t *testing.T, ctx context.Context, w *Review, job *river.Job[jobs.ReviewArgs]) error {
	t.Helper()
	ctx = rivertest.WorkContext(ctx, h.insertOnly)
	for {
		job.State = rivertype.JobStateRunning
		if _, err := h.st.App().Exec(h.ctx, `UPDATE river_job SET state = 'running', attempt = $2, finalized_at = NULL WHERE id = $1`,
			job.ID, job.Attempt); err != nil {
			t.Fatal(err)
		}
		var err error
		switch job.Kind {
		case jobs.QueuePrepare:
			err = (&Prepare{Review: w}).Work(ctx, &river.Job[jobs.PrepareArgs]{JobRow: job.JobRow, Args: jobs.PrepareArgs{ReviewArgs: job.Args}})
		case jobs.QueuePublish:
			err = (&Publish{Review: w}).Work(ctx, &river.Job[jobs.PublishArgs]{JobRow: job.JobRow, Args: jobs.PublishArgs{ReviewArgs: job.Args}})
		default:
			err = w.Work(ctx, job)
		}
		if err != nil {
			return err
		}
		var saved store.Publication
		err = h.st.WithAccount(h.ctx, job.Args.AccountID, func(tx pgx.Tx) error {
			var err error
			saved, err = store.ReadPublication(h.ctx, tx, job.ID)
			return err
		})
		// A preparation job no longer owns the review after its handoff.
		if errors.Is(err, store.ErrNotFound) && job.Kind == jobs.QueuePrepare {
			err = h.st.WithAccount(h.ctx, job.Args.AccountID, func(tx pgx.Tx) error {
				return tx.QueryRow(h.ctx, `SELECT river_job_id FROM reviews WHERE pull_request_id IN
					(SELECT id FROM pull_requests WHERE repository_id = $1 AND number = $2) AND status = 'prepared'`,
					job.Args.RepositoryID, job.Args.Number).Scan(&saved.JobID)
			})
		}
		if errors.Is(err, store.ErrNotFound) || saved.JobID == job.ID {
			return nil
		}
		if err != nil {
			return err
		}
		row, err := h.insertOnly.JobGet(h.ctx, saved.JobID)
		if err != nil {
			return err
		}
		job.JobRow = row
		job.Attempt = 1
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
	}
}

func TestReviewStageHandoff(t *testing.T) {
	h := newAgenticHarness(t, 1100)
	reg := prometheus.NewRegistry()
	h.review.Metrics = metrics.New(reg)
	job := queuedOutcomeReview(t, h)
	h.sm.reset(scriptSubmit)
	// The runner's time is the review job's, which the duration the publish
	// job records must count.
	const held = 200 * time.Millisecond
	h.exec.hold = held
	ctx := rivertest.WorkContext(h.ctx, h.insertOnly)
	job.State = rivertype.JobStateRunning
	if _, err := h.st.App().Exec(ctx, `UPDATE river_job SET state = 'running', attempt = $2 WHERE id = $1`, job.ID, job.Attempt); err != nil {
		t.Fatal(err)
	}
	before := time.Now()
	if err := h.review.Work(ctx, job); err != nil {
		t.Fatal(err)
	}
	runnerDone := time.Now()
	var saved store.Publication
	if err := h.st.WithAccount(ctx, h.account.ID(), func(tx pgx.Tx) error {
		var err error
		saved, err = store.ReadPublication(ctx, tx, job.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	original, err := h.insertOnly.JobGet(ctx, job.ID)
	if err != nil || original.State != rivertype.JobStateCompleted {
		t.Fatalf("runner job = %+v, %v", original, err)
	}
	prepared, err := h.insertOnly.JobGet(ctx, saved.JobID)
	if err != nil || prepared.Kind != jobs.QueuePrepare || prepared.State != rivertype.JobStateAvailable || prepared.Attempt != 0 {
		t.Fatalf("preparation job = %+v, %v", prepared, err)
	}
	if saved.Status != store.ReviewPrepared || len(saved.Payload) == 0 {
		t.Fatalf("checkpoint = %+v", saved)
	}
	if carried := startedAt(saved.Payload, time.Time{}); carried.Before(before) || carried.After(runnerDone) {
		t.Fatalf("checkpoint carries a start of %s, want the runner job's pickup between %s and %s", carried, before, runnerDone)
	}
	h.review.Executor = nil
	if err := h.review.Work(ctx, job); err != nil {
		t.Fatalf("replaying the handed-off runner job: %v", err)
	}
	job.JobRow = prepared
	job.Attempt = 1
	job.State = rivertype.JobStateRunning
	if _, err := h.st.App().Exec(ctx, `UPDATE river_job SET state = 'running', attempt = 1 WHERE id = $1`, job.ID); err != nil {
		t.Fatal(err)
	}
	stale := *job.JobRow
	stale.Attempt = 2
	if err := (&Prepare{Review: h.review}).Work(ctx,
		&river.Job[jobs.PrepareArgs]{JobRow: &stale, Args: jobs.PrepareArgs{ReviewArgs: job.Args}}); err == nil {
		t.Fatal("stale attempt handed off publication")
	}
	if err := h.st.WithAccount(ctx, h.account.ID(), func(tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE kind = $1 AND args->>'review_id' = $2`,
			jobs.QueuePublish, saved.ReviewID).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			t.Fatalf("stale attempt committed %d delivery jobs", count)
		}
		checkpoint, err := store.ReadPublication(ctx, tx, job.ID)
		if err == nil && string(checkpoint.Payload) != string(saved.Payload) {
			t.Fatal("stale attempt changed the checkpoint")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	stagesStarted := time.Now()
	if err := h.workStages(t, ctx, h.review, job); err != nil {
		t.Fatal(err)
	}
	if job.Kind != jobs.QueuePublish {
		t.Fatalf("last stage = %s", job.Kind)
	}
	if _, status, why := h.waitReview(t, h.head); status != "completed" {
		t.Fatalf("review = %s: %s", status, why)
	}
	checkPublishedTiming(t, reg)
	// The duration spans the runner's hold and the stages between, which a
	// clock restarted by the publish job would not; one restarted from zero
	// would pass the test's own time.
	checkReviewDuration(t, reg, held+stagesStarted.Sub(runnerDone), time.Since(before))
}

// checkReviewDuration: the review's duration was observed once, from the
// runner job's pickup to the publish job's end, between atLeast and atMost.
func checkReviewDuration(t *testing.T, reg *prometheus.Registry, atLeast, atMost time.Duration) {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() != "kritika_review_duration_seconds" {
			continue
		}
		h := family.GetMetric()[0].GetHistogram()
		if sum := h.GetSampleSum(); h.GetSampleCount() != 1 || sum < atLeast.Seconds() || sum > atMost.Seconds() {
			t.Fatalf("review duration = %v, want one observation between %s and %s", h, atLeast, atMost)
		}
		return
	}
	t.Fatal("review duration not observed")
}

func checkPublishedTiming(t *testing.T, reg *prometheus.Registry) {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() != "kritika_review_stage_duration_seconds" {
			continue
		}
		for _, metric := range family.Metric {
			for _, label := range metric.Label {
				if label.GetName() == "stage" && label.GetValue() == "agent_to_published" {
					h := metric.GetHistogram()
					if h.GetSampleCount() != 1 || h.GetSampleSum() <= 0 {
						t.Fatalf("agent-to-publication timing = %v", h)
					}
					return
				}
			}
		}
	}
	t.Fatal("agent timestamp did not survive preparation and publication handoffs")
}

// TestPreparationFailureSettlesMarks: a preparation stage that ends the
// review itself, with no job error for River, still takes the eyes off the
// pull request and marks it failed, as the runner job did before it handed
// preparation off.
func TestPreparationFailureSettlesMarks(t *testing.T) {
	h := newAgenticHarness(t, 1101)
	h.sm.reset(scriptProse)
	h.dispatch(t, h.head)
	if _, status, errText := h.waitReview(t, h.head); status != "failed" || errText != "agent stopped: no_submit" {
		t.Fatalf("review = %s (%q), want failed without a submission", status, errText)
	}
	waitFor(t, 5*time.Second, "the pull request to be marked as failed", func() bool {
		h.lf.mu.Lock()
		defer h.lf.mu.Unlock()
		on := h.lf.pullReactions[h.number]
		return on != nil && !on[forge.ReactionEyes] && on[forge.ReactionFailed]
	})
}

// TestStageAfterCloseReplacesStatus: a pull request closed once its runner
// is done still gets the review's outcome in place of the pending status it
// set, as it did when one job carried the review through; only a review
// that began after the close sets none. The worker read the pull request
// open just before the review row was made, so a close that lands in
// between is one the review began under all the same.
func TestStageAfterCloseReplacesStatus(t *testing.T) {
	for _, tt := range []struct {
		name     string
		number   int
		closedAt string
	}{
		{"closed after the review began", 1102, "now()"},
		{"closed as the review began", 1103, "(SELECT min(created_at) FROM reviews WHERE pull_request_id = pull_requests.id) - interval '1 second'"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := newAgenticHarness(t, tt.number)
			job := queuedOutcomeReview(t, h)
			h.sm.reset(scriptSubmit)
			closed := false
			h.exec.after = func() {
				err := h.st.WithAccount(h.ctx, h.account.ID(), func(tx pgx.Tx) error {
					tag, err := tx.Exec(h.ctx, `UPDATE pull_requests SET state = 'closed', closed_at = `+tt.closedAt+`
						WHERE repository_id = $1 AND number = $2`, job.Args.RepositoryID, h.number)
					if err == nil && tag.RowsAffected() != 1 {
						err = fmt.Errorf("%d pull requests closed", tag.RowsAffected())
					}
					return err
				})
				if err != nil {
					t.Error(err)
				}
				closed = true
			}
			if err := h.workStages(t, h.ctx, h.review, job); err != nil {
				t.Fatal(err)
			}
			if !closed {
				t.Fatal("the runner did not run")
			}
			if job.Kind != jobs.QueuePublish {
				t.Fatalf("last stage = %s", job.Kind)
			}
			if _, status, why := h.waitReview(t, h.head); status != "completed" {
				t.Fatalf("review = %s: %s", status, why)
			}
			h.waitForgeStatus(t, "success: kritika: 1 finding(s)")
		})
	}
}
