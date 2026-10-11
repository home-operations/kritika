package worker

import (
	"context"
	"errors"
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
	ctx, cctx context.Context, job *river.Job[jobs.ReviewArgs], e endedReview, res executor.Result, cause, agentErr error,
) (bool, error) {
	decision := decideRunnerReview(context.Cause(ctx), cause, res, agentErr, job.Attempt >= job.MaxAttempts)
	if decision.disposition == reviewContinue {
		return false, nil
	}
	if decision.end.OnlyUnfinished {
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
		"reason", res.TerminationReason)
	w.Metrics.Review(e.accountKey, string(decision.end.Status), time.Since(e.started))
	if decision.description != "" {
		if err := e.client.SetStatus(cctx, e.owner, e.repo, e.headSHA, forge.StatusError, decision.description); err != nil {
			e.logger.Warn("commit status not set", "error", err)
		}
	}
	if err := w.finishReview(cctx, e.accountID, e.reviewID, decision.end.Status, "", decision.end.Error); err != nil {
		return true, err
	}
	return true, decision.jobError()
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
	decision := decideInterruptedReview(context.Cause(ctx), err)
	if decision.end.Status == "" {
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
	if decision.disposition == reviewRetry {
		e.logger.Info("review "+decision.end.Error, "job", e.jobName)
		w.Metrics.Review(e.accountKey, string(decision.end.Status), time.Since(e.started))
		return decision.jobError()
	}
	if !finished {
		return nil
	}
	e.logger.Info("review "+string(decision.end.Status)+" as its job ended", "cause", context.Cause(ctx), "job", e.jobName)
	if err := e.client.SetStatus(cctx, e.owner, e.repo, e.headSHA, forge.StatusError, decision.description); err != nil {
		e.logger.Warn("commit status not set", "error", err)
	}
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
