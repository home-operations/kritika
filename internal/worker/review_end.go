package worker

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
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
	ctx, cctx context.Context, job *river.Job[jobs.ReviewArgs], e endedReview, res executor.Result, cause, agentErr error,
) (bool, error) {
	decision := decideRunnerReview(context.Cause(ctx), cause, res, agentErr, job.Attempt >= job.MaxAttempts)
	if decision.disposition == reviewContinue {
		return false, nil
	}
	if decision.disposition == reviewInterrupted {
		return true, w.finishInterrupted(ctx, e, decision)
	}
	level := slog.LevelInfo
	if decision.end.Error != "" {
		level = slog.LevelWarn
	}
	if agentErr != nil && decision.end.Status == store.ReviewFailed {
		level = slog.LevelError
	}
	e.logger.Log(cctx, level, "review "+string(decision.end.Status), "error", decision.end.Error, "job", res.JobName,
		"reason", res.TerminationReason, "runner_error", res.Err, "attempt", job.Attempt)
	w.Metrics.Review(e.accountKey, string(decision.end.Status), time.Since(e.started))
	if decision.description != "" {
		return true, w.failReview(cctx, e, "", decision.end.Error)
	}
	if _, err := w.endReview(cctx, e.accountID, e.reviewID, decision.end); err != nil {
		return true, err
	}
	return true, decision.jobError()
}

// failReview ends a review as failed and reports that on the head commit:
// a head whose review broke must not read as one still waiting for it.
// The description stays generic since the error can name internal hosts.
// A review a newer one replaced meanwhile keeps that end, and the head's
// status is the newer review's to set.
func (w *Review) failReview(ctx context.Context, e endedReview, patchID, errText string) error {
	ended, err := w.endReview(ctx, e.accountID, e.reviewID, store.ReviewEnd{
		Status: store.ReviewFailed, PatchID: patchID, Error: errText, OnlyUnfinished: true,
	})
	if err != nil || !ended {
		return err
	}
	w.reportEnd(ctx, e, reviewFailedDescription)
	return nil
}

// endedReview is what finishEnded needs to know of the review whose job
// ended.
type endedReview struct {
	accountID, accountKey, reviewID, prID, headSHA, jobName string
	owner, repo                                             string
	client                                                  forge.Client
	started                                                 time.Time
	logger                                                  *slog.Logger
}

// lockRetry is how long lockedWrite waits before it tries a held
// publication lock again.
const lockRetry = 500 * time.Millisecond

// lockedWrite runs write under the publication lock of pull request prID,
// taken with lock, waiting out a holder for as long as ctx allows: the
// holder is a review's start or another review's publication, each brief.
func lockedWrite(
	ctx context.Context, lock func(context.Context, string, func(context.Context) error) error, prID string,
	write func(context.Context) error,
) error {
	for {
		err := lock(ctx, prID, write)
		if !errors.Is(err, store.ErrPublicationBusy) {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(lockRetry):
		}
	}
}

// ownedWrite runs write, what a review that ended short of publishing says
// of its pull request on the forge, under the pull request's publication
// lock and only while the review still speaks for it (store.ReviewCeded):
// a newer review's start sets its own status under the same lock, so the
// write never lands over what a newer review said.
func (w *Review) ownedWrite(ctx context.Context, e endedReview, write func(context.Context) error) {
	err := lockedWrite(ctx, w.Store.WithPublicationLock, e.prID, func(ctx context.Context) error {
		var ceded bool
		err := w.Store.WithAccount(ctx, e.accountID, func(tx pgx.Tx) error {
			var err error
			ceded, err = store.ReviewCeded(ctx, tx, e.reviewID)
			return err
		})
		if err != nil || ceded {
			return err
		}
		return write(ctx)
	})
	if err != nil {
		e.logger.Warn("commit status not set", "error", err)
	}
}

// reportEnd sets the head's error status, desc, for a review that ended
// short of publishing (see ownedWrite).
func (w *Review) reportEnd(ctx context.Context, e endedReview, desc string) {
	w.ownedWrite(ctx, e, func(ctx context.Context) error {
		return e.client.SetStatus(ctx, e.owner, e.repo, e.headSHA, forge.StatusError, desc)
	})
}

// finishEnded ends a review whose job ctx ended before the review could: a
// remote cancel as canceled, River's job timeout as failed, both returning
// nil, since an error would have River retry the review and pay for the
// model again. A stopping worker's cut is the exception: the review ends
// superseded by its retry, and an error is returned for River to retry it.
// A review that is already terminal is left as it is. While ctx is still
// live it returns err as is.
func (w *Review) finishEnded(ctx context.Context, e endedReview, err error) error {
	decision := decideInterruptedReview(context.Cause(ctx), err)
	if decision.disposition != reviewInterrupted {
		return decision.jobError()
	}
	return w.finishInterrupted(ctx, e, decision)
}

func (w *Review) finishInterrupted(ctx context.Context, e endedReview, decision reviewDecision) error {
	cctx, cancel := detach(ctx)
	defer cancel()
	finished, err := w.endReview(cctx, e.accountID, e.reviewID, decision.end)
	if err != nil {
		return err
	}
	if decision.err != nil {
		e.logger.Info("review interrupted", "reason", decision.end.Error, "job", e.jobName)
		w.Metrics.Review(e.accountKey, string(decision.end.Status), time.Since(e.started))
		return decision.jobError()
	}
	if !finished {
		return decision.jobError()
	}
	e.logger.Info("review ended with its job", "status", decision.end.Status, "cause", context.Cause(ctx), "job", e.jobName)
	w.reportEnd(cctx, e, decision.description)
	w.Metrics.Review(e.accountKey, string(decision.end.Status), time.Since(e.started))
	return decision.jobError()
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

// preparationContext gives a completed runner a short opportunity to save its
// answer after shutdown. Live work keeps the job's timeout and cancellation policy.
func preparationContext(job context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancelCause(context.WithoutCancel(job))
	forward := func() {
		cause := context.Cause(job)
		if cause != nil && !shutdownCause(cause) {
			cancel(cause)
		}
	}
	forward()
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-job.Done():
		}
		forward()
		if shutdownCause(context.Cause(job)) {
			timer := time.NewTimer(10 * time.Second)
			defer timer.Stop()
			select {
			case <-ctx.Done():
			case <-timer.C:
				cancel(context.Cause(job))
			}
		}
	}()
	return ctx, func() { cancel(context.Canceled) }
}
