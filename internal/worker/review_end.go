package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/riverqueue/river"

	"github.com/home-operations/kritika/internal/executor"
	"github.com/home-operations/kritika/internal/forge"
	"github.com/home-operations/kritika/internal/jobs"
	"github.com/home-operations/kritika/internal/store"
)

// judgeRun ends a review whose run ended before its agent could finish it,
// or whose agent run cannot be read, and says what River does next: it
// reports whether it ended the review, and the error River retries on.
// A run that finished is judged by afterRun.
func (w *Review) judgeRun(
	ctx, cctx context.Context, job *river.Job[jobs.ReviewArgs], e endedReview, canceled bool, res executor.Result, cause, agentErr error,
) (bool, error) {
	// canceled takes priority over both agentErr and the run's own result: a
	// job River canceled must never be reported failed or retried, whether
	// or not the agent run record could be read. A run that finished without
	// a cancel is judged below by agentErr, then by its result; the head
	// check after that switch still catches a supersede that raced with a
	// normal finish.
	if canceled {
		return true, w.finishEnded(ctx, e, nil)
	}
	// A run a stopping worker cut is retried, not judged: its failure, and
	// a missing agent row, are the stop's doing.
	if res.Err != nil && workerStopping(ctx) {
		return true, w.finishEnded(ctx, e, res.Err)
	}
	// A runner whose pod never started spent nothing: the job is retried,
	// and the review ends superseded by the retry's, unless this was the
	// last attempt, which the failure below ends for good. A run the
	// supervision ended is judged by why it ended.
	if res.Err != nil && cause == nil && res.NeverStarted && job.Attempt < job.MaxAttempts {
		e.logger.Warn("runner did not start; its job is retried", "error", res.Err, "job", res.JobName, "attempt", job.Attempt)
		w.Metrics.Review(e.accountKey, string(store.ReviewSuperseded), time.Since(e.started))
		if err := w.finishReview(cctx, e.accountID, e.reviewID, store.ReviewSuperseded, "", "its runner did not start; retried"); err != nil {
			return true, err
		}
		return true, fmt.Errorf("worker: runner did not start: %w", res.Err)
	}
	if agentErr != nil {
		e.logger.Error("agent run not read", "error", agentErr)
		w.Metrics.Review(e.accountKey, string(store.ReviewFailed), time.Since(e.started))
		// A retry would run the agent again; the review ends here.
		return true, w.failReview(cctx, e, "", agentErr.Error())
	}
	switch {
	case res.Err != nil && errors.Is(cause, errSuperseded):
		e.logger.Info("review superseded while running", "job", res.JobName)
		w.Metrics.Review(e.accountKey, string(store.ReviewSuperseded), time.Since(e.started))
		return true, w.finishReview(cctx, e.accountID, e.reviewID, store.ReviewSuperseded, "", "")
	case res.Err != nil && errors.Is(cause, errHeartbeatLost):
		e.logger.Warn("runner heartbeat lost", "job", res.JobName)
		w.Metrics.Review(e.accountKey, string(store.ReviewFailed), time.Since(e.started))
		return true, w.failReview(cctx, e, "", "runner heartbeat lost")
	case res.Err != nil:
		e.logger.Warn("runner failed", "error", res.Err, "job", res.JobName, "reason", res.TerminationReason)
		w.Metrics.Review(e.accountKey, string(store.ReviewFailed), time.Since(e.started))
		return true, w.failReview(cctx, e, "", res.Err.Error())
	}
	return false, nil
}

// finishReview ends a review as status, recording its patch id when known
// and why it failed.
func (w *Review) finishReview(ctx context.Context, accountID, reviewID string, status store.ReviewStatus, patchID, errText string) error {
	_, err := w.endReview(ctx, accountID, reviewID, store.ReviewEnd{Status: status, PatchID: patchID, Error: errText})
	return err
}

// failReview ends a review as failed and reports that on the head commit:
// a head whose review broke must not read as one still waiting for it.
// The description stays generic since the error can name internal hosts.
func (w *Review) failReview(ctx context.Context, e endedReview, patchID, errText string) error {
	if err := e.client.SetStatus(ctx, e.owner, e.repo, e.headSHA, forge.StatusError, "kritika: review failed"); err != nil {
		e.logger.Warn("commit status not set", "error", err)
	}
	return w.finishReview(ctx, e.accountID, e.reviewID, store.ReviewFailed, patchID, errText)
}

// endedReview is what finishEnded needs to know of the review whose job
// ended.
type endedReview struct {
	accountID, accountKey, reviewID, headSHA, jobName string
	owner, repo                                       string
	client                                            forge.Client
	started                                           time.Time
	logger                                            *slog.Logger
}

// finishEnded ends a review whose job ctx ended before the review could: a
// remote cancel as canceled, River's job timeout as failed, both returning
// nil, since an error would have River retry the review and pay for the
// model again. A stopping worker's cut is the exception: the review ends
// superseded by its retry, and an error is returned for River to retry it.
// A review that is already terminal is left as it is. While ctx is still
// live it returns err as is.
func (w *Review) finishEnded(ctx context.Context, e endedReview, err error) error {
	if ctx.Err() == nil {
		return err
	}
	cctx, cancel := detach(ctx)
	defer cancel()
	cause := context.Cause(ctx)
	if workerStopping(ctx) {
		if _, ferr := w.endReview(cctx, e.accountID, e.reviewID, store.ReviewEnd{
			Status: store.ReviewSuperseded, Error: "cut by a restart and retried", OnlyUnfinished: true,
		}); ferr != nil {
			return ferr
		}
		e.logger.Info("review cut by a restart; its job is retried", "job", e.jobName)
		w.Metrics.Review(e.accountKey, string(store.ReviewSuperseded), time.Since(e.started))
		return fmt.Errorf("worker: review cut by a restart: %w", cause)
	}
	status, errText, desc := store.ReviewCanceled, "", "kritika: review canceled"
	if !errors.Is(cause, river.ErrJobCancelledRemotely) {
		status, errText, desc = store.ReviewFailed, "review timed out: "+cause.Error(), "kritika: review timed out"
	}
	finished, ferr := w.endReview(cctx, e.accountID, e.reviewID, store.ReviewEnd{Status: status, Error: errText, OnlyUnfinished: true})
	if ferr != nil || !finished {
		return ferr
	}
	e.logger.Info("review "+string(status)+" as its job ended", "cause", cause, "job", e.jobName)
	if err := e.client.SetStatus(cctx, e.owner, e.repo, e.headSHA, forge.StatusError, desc); err != nil {
		e.logger.Warn("commit status not set", "error", err)
	}
	w.Metrics.Review(e.accountKey, string(status), time.Since(e.started))
	return nil
}

// agentSpecFailed ends a review whose runner never started because its
// agent spec could not be built, and the run made for it. When the job's
// ctx ended meanwhile (a remote cancel, River's timeout, a stopping
// worker), that is why, and the review ends as finishEnded ends it;
// otherwise err is returned for River to retry.
func (w *Review) agentSpecFailed(ctx context.Context, e endedReview, runID string, err error) error {
	dctx, cancel := detach(ctx)
	defer cancel()
	runErr := failRun(dctx, w.Store, e.accountID, runID, err.Error())
	if ctx.Err() != nil {
		if runErr != nil {
			e.logger.Warn("runner run not ended", "error", runErr)
		}
		return w.finishEnded(ctx, e, err)
	}
	return errors.Join(err, w.failReview(dctx, e, "", err.Error()), runErr)
}
