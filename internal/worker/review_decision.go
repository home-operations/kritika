package worker

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/riverqueue/river"

	"github.com/home-operations/kritika/internal/executor"
	"github.com/home-operations/kritika/internal/store"
)

type reviewDisposition uint8

const reviewFailedDescription = "kritika: review failed"

const (
	reviewContinue reviewDisposition = iota + 1
	reviewFinish
	reviewRetry
	reviewInterrupted
	reviewPublicationFailed
	reviewSnooze
)

// A decision describes policy only. Callers still own transaction boundaries,
// forge writes and metrics; a failure to persist an outcome remains retryable.
type reviewDecision struct {
	end         store.ReviewEnd
	disposition reviewDisposition
	err         error
	description string
}

func (d reviewDecision) jobError() error {
	switch d.disposition {
	case reviewRetry, reviewInterrupted, reviewPublicationFailed:
		return d.err
	case reviewSnooze:
		return river.JobSnooze(time.Second)
	case reviewContinue, reviewFinish:
		return nil
	default:
		panic("worker: invalid review disposition")
	}
}

func stoppingCause(cause error) bool {
	return cause != nil && !errors.Is(cause, river.ErrJobCancelledRemotely) && !errors.Is(cause, context.DeadlineExceeded)
}

func shutdownCause(cause error) bool {
	return stoppingCause(cause) && !errors.Is(cause, errJobFenced)
}

func reviewAttemptFailed(cause, err error, finalAttempt bool) bool {
	if _, snoozed := errors.AsType[*river.JobSnoozeError](err); snoozed {
		return false
	}
	// River refunds a shutdown attempt only when the returned error carries
	// the stop cause or context.Canceled. Fencing is our own cancellation.
	refunded := shutdownCause(cause) &&
		(errors.Is(err, cause) || errors.Is(err, context.Canceled))
	return err != nil && finalAttempt && !refunded
}

func decideInterruptedReview(cause, err error) reviewDecision {
	if cause == nil {
		if err == nil {
			return reviewDecision{disposition: reviewContinue}
		}
		return reviewDecision{disposition: reviewRetry, err: err}
	}
	end := store.ReviewEnd{OnlyUnfinished: true}
	if stoppingCause(cause) {
		end.Status, end.Error = store.ReviewSuperseded, "cut by a restart and retried"
		if errors.Is(cause, errJobFenced) {
			end.Error = "cut as its heartbeat stopped landing; its job was handed back"
		}
		return reviewDecision{end: end, disposition: reviewInterrupted, err: fmt.Errorf("worker: review %s: %w", end.Error, cause)}
	}
	end.Status = store.ReviewCanceled
	desc := "kritika: review canceled"
	if !errors.Is(cause, river.ErrJobCancelledRemotely) {
		end.Status, end.Error, desc = store.ReviewFailed, "review timed out: "+cause.Error(), "kritika: review timed out"
	}
	return reviewDecision{end: end, disposition: reviewInterrupted, description: desc}
}

func decideRunnerReview(jobCause, runCause error, res executor.Result, agentErr error, finalAttempt bool) reviewDecision {
	// River will not retry a remotely canceled job, so its review must end
	// canceled even when the runner answered or its result is lost.
	if errors.Is(jobCause, river.ErrJobCancelledRemotely) || (res.Err != nil && stoppingCause(jobCause)) {
		return decideInterruptedReview(jobCause, res.Err)
	}
	// A runner that never started spent nothing; its retry may run the model.
	if res.Err != nil && runCause == nil && res.NeverStarted && !finalAttempt {
		end := store.ReviewEnd{Status: store.ReviewSuperseded, Error: "its runner did not start; retried", OnlyUnfinished: true}
		return reviewDecision{end: end, disposition: reviewRetry, err: fmt.Errorf("worker: runner did not start: %w", res.Err)}
	}
	// A runner whose pod Kubernetes disrupted, by a node drain, a preemption
	// or an eviction, did not fail on its own: its retry runs it again,
	// paying the model again, so the pull request still gets its review. A
	// disrupted pod slow to terminate may lose its heartbeat first.
	if res.Err != nil && (runCause == nil || errors.Is(runCause, errHeartbeatLost)) && res.Disruption != "" && !finalAttempt {
		end := store.ReviewEnd{Status: store.ReviewSuperseded, Error: "its runner's pod was disrupted; retried", OnlyUnfinished: true}
		return reviewDecision{end: end, disposition: reviewRetry, err: fmt.Errorf("worker: runner pod disrupted: %w", res.Err)}
	}
	if agentErr != nil {
		// Retrying a missing result would pay for the agent again.
		return reviewDecision{end: store.ReviewEnd{Status: store.ReviewFailed, Error: agentErr.Error()},
			disposition: reviewFinish, description: reviewFailedDescription}
	}
	if res.Err == nil {
		return reviewDecision{disposition: reviewContinue}
	}
	// An end a newer review's start recorded meanwhile stands.
	if errors.Is(runCause, errSuperseded) {
		return reviewDecision{end: store.ReviewEnd{Status: store.ReviewSuperseded, OnlyUnfinished: true}, disposition: reviewFinish}
	}
	if errors.Is(runCause, errReplaced) {
		return reviewDecision{end: store.ReviewEnd{Status: store.ReviewCanceled, OnlyUnfinished: true}, disposition: reviewFinish}
	}
	detail := res.Err.Error()
	if errors.Is(runCause, errHeartbeatLost) {
		detail = "runner heartbeat lost"
	}
	return reviewDecision{end: store.ReviewEnd{Status: store.ReviewFailed, Error: detail},
		disposition: reviewFinish, description: reviewFailedDescription}
}

func decidePreparedReview(superseded bool, skipReason string, labelsMoved bool) reviewDecision {
	switch {
	case superseded:
		return reviewDecision{end: store.ReviewEnd{Status: store.ReviewSuperseded}, disposition: reviewFinish}
	case skipReason != "":
		disposition := reviewFinish
		if labelsMoved {
			disposition = reviewSnooze
		}
		return reviewDecision{end: store.ReviewEnd{Status: store.ReviewSkipped, SkipReason: skipReason}, disposition: disposition}
	default:
		return reviewDecision{end: store.ReviewEnd{Status: store.ReviewPrepared}, disposition: reviewContinue}
	}
}

func decidePublication(jobCause error, status store.ReviewStatus, err error, checkpointed, finalAttempt bool) reviewDecision {
	// Successful detached delivery stands even if the job ended meanwhile.
	if err == nil {
		return reviewDecision{end: store.ReviewEnd{Status: status}, disposition: reviewFinish}
	}
	if jobCause != nil && (!checkpointed || !stoppingCause(jobCause)) {
		return decideInterruptedReview(jobCause, err)
	}
	if !checkpointed {
		return reviewDecision{end: store.ReviewEnd{Status: status, Error: err.Error()}, disposition: reviewFinish}
	}
	_, permanent := errors.AsType[*river.JobCancelError](err)
	if permanent || reviewAttemptFailed(jobCause, err, finalAttempt) {
		return reviewDecision{end: store.ReviewEnd{Status: store.ReviewFailed, Error: err.Error(), OnlyUnfinished: true},
			disposition: reviewPublicationFailed, err: err, description: "kritika: publication failed"}
	}
	return reviewDecision{end: store.ReviewEnd{Status: store.ReviewPrepared}, disposition: reviewRetry, err: err}
}
