package worker

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/riverqueue/river"

	"github.com/home-operations/kritika/internal/executor"
	"github.com/home-operations/kritika/internal/store"
)

func TestDecideInterruptedReview(t *testing.T) {
	failure := errors.New("write failed")
	stopped := errors.New("stop initiated")
	for _, tt := range []struct {
		name       string
		cause, err error
		want       reviewDecision
	}{
		{name: "live context with no error continues", want: reviewDecision{disposition: reviewContinue}},
		{name: "live context preserves a retryable error", err: failure, want: reviewDecision{disposition: reviewRetry, err: failure}},
		{name: "remote cancellation wins over a write error", cause: fmt.Errorf("wrapped: %w", river.ErrJobCancelledRemotely), err: failure,
			want: reviewDecision{end: store.ReviewEnd{Status: store.ReviewCanceled, OnlyUnfinished: true}, disposition: reviewFinish, description: "kritika: review canceled"}},
		{name: "timeout ends without repeating model work", cause: context.DeadlineExceeded, err: failure,
			want: reviewDecision{end: store.ReviewEnd{Status: store.ReviewFailed, Error: "review timed out: context deadline exceeded", OnlyUnfinished: true},
				disposition: reviewFinish, description: "kritika: review timed out"}},
		{name: "restart supersedes the attempt and retries", cause: stopped, err: failure,
			want: reviewDecision{end: store.ReviewEnd{Status: store.ReviewSuperseded, Error: "cut by a restart and retried", OnlyUnfinished: true}, disposition: reviewRetry, err: stopped}},
		{name: "fencing hands the job back", cause: errJobFenced,
			want: reviewDecision{end: store.ReviewEnd{Status: store.ReviewSuperseded, Error: "cut as its heartbeat stopped landing; its job was handed back", OnlyUnfinished: true},
				disposition: reviewRetry, err: errJobFenced}},
	} {
		t.Run(tt.name, func(t *testing.T) { checkReviewDecision(t, decideInterruptedReview(tt.cause, tt.err), tt.want) })
	}
}

func TestDecideRunnerReview(t *testing.T) {
	runErr, agentErr, stopped := errors.New("runner failed"), errors.New("agent row missing"), errors.New("stop initiated")
	for _, tt := range []struct {
		name                         string
		jobCause, runCause, agentErr error
		result                       executor.Result
		final                        bool
		want                         reviewDecision
	}{
		{name: "successful runner continues", want: reviewDecision{disposition: reviewContinue}},
		{name: "successful runner can survive a restart", jobCause: stopped, want: reviewDecision{disposition: reviewContinue}},
		{name: "cancel beats a successful runner", jobCause: river.ErrJobCancelledRemotely,
			want: reviewDecision{end: store.ReviewEnd{Status: store.ReviewCanceled, OnlyUnfinished: true}, disposition: reviewFinish, description: "kritika: review canceled"}},
		{name: "cancel beats missing agent and superseded runner", jobCause: river.ErrJobCancelledRemotely, runCause: errSuperseded, agentErr: agentErr, result: executor.Result{Err: runErr},
			want: reviewDecision{end: store.ReviewEnd{Status: store.ReviewCanceled, OnlyUnfinished: true}, disposition: reviewFinish, description: "kritika: review canceled"}},
		{name: "restart beats missing agent", jobCause: stopped, agentErr: agentErr, result: executor.Result{Err: runErr},
			want: reviewDecision{end: store.ReviewEnd{Status: store.ReviewSuperseded, Error: "cut by a restart and retried", OnlyUnfinished: true}, disposition: reviewRetry, err: stopped}},
		{name: "unstarted runner retries before inspecting missing agent", agentErr: agentErr, result: executor.Result{Err: runErr, NeverStarted: true},
			want: reviewDecision{end: store.ReviewEnd{Status: store.ReviewSuperseded, Error: "its runner did not start; retried"}, disposition: reviewRetry, err: runErr}},
		{name: "unstarted runner cannot retry on final attempt", final: true, result: executor.Result{Err: runErr, NeverStarted: true},
			want: reviewDecision{end: store.ReviewEnd{Status: store.ReviewFailed, Error: "runner failed"}, disposition: reviewFinish, description: "kritika: review failed"}},
		{name: "supervision prevents unstarted retry", runCause: errSuperseded, result: executor.Result{Err: runErr, NeverStarted: true},
			want: reviewDecision{end: store.ReviewEnd{Status: store.ReviewSuperseded}, disposition: reviewFinish}},
		{name: "missing result fails without rerunning model", agentErr: agentErr,
			want: reviewDecision{end: store.ReviewEnd{Status: store.ReviewFailed, Error: "agent row missing"}, disposition: reviewFinish, description: "kritika: review failed"}},
		{name: "missing result takes precedence over runner supersession", runCause: errSuperseded, result: executor.Result{Err: runErr}, agentErr: agentErr,
			want: reviewDecision{end: store.ReviewEnd{Status: store.ReviewFailed, Error: "agent row missing"}, disposition: reviewFinish, description: "kritika: review failed"}},
		{name: "moved head supersedes a failed run", runCause: fmt.Errorf("wrapped: %w", errSuperseded), result: executor.Result{Err: runErr},
			want: reviewDecision{end: store.ReviewEnd{Status: store.ReviewSuperseded}, disposition: reviewFinish}},
		{name: "lost runner heartbeat has its own failure reason", runCause: errHeartbeatLost, result: executor.Result{Err: runErr},
			want: reviewDecision{end: store.ReviewEnd{Status: store.ReviewFailed, Error: "runner heartbeat lost"}, disposition: reviewFinish, description: "kritika: review failed"}},
		{name: "paid runner failure does not retry", result: executor.Result{Err: runErr},
			want: reviewDecision{end: store.ReviewEnd{Status: store.ReviewFailed, Error: "runner failed"}, disposition: reviewFinish, description: "kritika: review failed"}},
		{name: "timeout does not retry a failed runner", jobCause: context.DeadlineExceeded, result: executor.Result{Err: runErr},
			want: reviewDecision{end: store.ReviewEnd{Status: store.ReviewFailed, Error: "runner failed"}, disposition: reviewFinish, description: "kritika: review failed"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			checkReviewDecision(t, decideRunnerReview(tt.jobCause, tt.runCause, tt.result, tt.agentErr, tt.final), tt.want)
		})
	}
}

func TestDecidePreparedReview(t *testing.T) {
	for _, tt := range []struct {
		name                    string
		superseded, labelsMoved bool
		skip                    string
		want                    reviewDecision
	}{
		{name: "current head prepares publication", want: reviewDecision{end: store.ReviewEnd{Status: store.ReviewPrepared}, disposition: reviewContinue}},
		{name: "supersession takes precedence over a skip", superseded: true, skip: "filtered", labelsMoved: true,
			want: reviewDecision{end: store.ReviewEnd{Status: store.ReviewSuperseded}, disposition: reviewFinish}},
		{name: "stable filter finishes", skip: "filtered",
			want: reviewDecision{end: store.ReviewEnd{Status: store.ReviewSkipped, SkipReason: "filtered"}, disposition: reviewFinish}},
		{name: "changed labels snooze without burning an attempt", skip: "filtered", labelsMoved: true,
			want: reviewDecision{end: store.ReviewEnd{Status: store.ReviewSkipped, SkipReason: "filtered"}, disposition: reviewSnooze}},
		{name: "unchanged patch finishes", skip: "unchanged_patch",
			want: reviewDecision{end: store.ReviewEnd{Status: store.ReviewSkipped, SkipReason: "unchanged_patch"}, disposition: reviewFinish}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			checkReviewDecision(t, decidePreparedReview(tt.superseded, tt.skip, tt.labelsMoved), tt.want)
		})
	}
}

func TestDecidePublication(t *testing.T) {
	failure, stopped := errors.New("forge unavailable"), errors.New("stop initiated")
	permanent := fmt.Errorf("restore: %w", river.JobCancel(errors.New("unsupported snapshot")))
	for _, tt := range []struct {
		name                string
		jobCause, err       error
		status              store.ReviewStatus
		checkpointed, final bool
		want                reviewDecision
	}{
		{name: "successful publication finishes", status: store.ReviewCompleted, checkpointed: true,
			want: reviewDecision{end: store.ReviewEnd{Status: store.ReviewCompleted}, disposition: reviewFinish}},
		{name: "success wins over concurrent cancel", jobCause: river.ErrJobCancelledRemotely, status: store.ReviewCompleted, checkpointed: true,
			want: reviewDecision{end: store.ReviewEnd{Status: store.ReviewCompleted}, disposition: reviewFinish}},
		{name: "success wins over timeout", jobCause: context.DeadlineExceeded, status: store.ReviewCompleted, checkpointed: true,
			want: reviewDecision{end: store.ReviewEnd{Status: store.ReviewCompleted}, disposition: reviewFinish}},
		{name: "terminal replay has no new outcome", checkpointed: true, want: reviewDecision{disposition: reviewFinish}},
		{name: "superseded delivery stays superseded", status: store.ReviewSuperseded, checkpointed: true,
			want: reviewDecision{end: store.ReviewEnd{Status: store.ReviewSuperseded}, disposition: reviewFinish}},
		{name: "failure before checkpoint must not repeat model work", err: failure, status: store.ReviewFailed,
			want: reviewDecision{end: store.ReviewEnd{Status: store.ReviewFailed, Error: "forge unavailable"}, disposition: reviewFinish, description: "kritika: review failed"}},
		{name: "restart before checkpoint hands back the job", err: failure, jobCause: stopped,
			want: reviewDecision{end: store.ReviewEnd{Status: store.ReviewSuperseded, Error: "cut by a restart and retried", OnlyUnfinished: true}, disposition: reviewRetry, err: stopped}},
		{name: "saved answer retries delivery only", err: failure, checkpointed: true,
			want: reviewDecision{end: store.ReviewEnd{Status: store.ReviewPrepared}, disposition: reviewRetry, err: failure}},
		{name: "restart retains saved answer", jobCause: stopped, err: failure, checkpointed: true,
			want: reviewDecision{end: store.ReviewEnd{Status: store.ReviewPrepared}, disposition: reviewRetry, err: failure}},
		{name: "fencing retains saved answer", jobCause: errJobFenced, err: errJobFenced, checkpointed: true,
			want: reviewDecision{end: store.ReviewEnd{Status: store.ReviewPrepared}, disposition: reviewRetry, err: errJobFenced}},
		{name: "timeout ends saved publication", jobCause: context.DeadlineExceeded, err: failure, checkpointed: true,
			want: reviewDecision{end: store.ReviewEnd{Status: store.ReviewFailed, Error: "review timed out: context deadline exceeded", OnlyUnfinished: true}, disposition: reviewFinish, description: "kritika: review timed out"}},
		{name: "cancel wins over permanent failure on last attempt", jobCause: river.ErrJobCancelledRemotely, err: permanent, checkpointed: true, final: true,
			want: reviewDecision{end: store.ReviewEnd{Status: store.ReviewCanceled, OnlyUnfinished: true}, disposition: reviewFinish, description: "kritika: review canceled"}},
		{name: "permanent error cancels before attempts exhausted", err: permanent, checkpointed: true,
			want: reviewDecision{end: store.ReviewEnd{Status: store.ReviewFailed, Error: permanent.Error(), OnlyUnfinished: true}, disposition: reviewCancel, err: permanent, description: "kritika: publication failed"}},
		{name: "exhausted delivery attempts fail review", err: failure, checkpointed: true, final: true,
			want: reviewDecision{end: store.ReviewEnd{Status: store.ReviewFailed, Error: "forge unavailable", OnlyUnfinished: true}, disposition: reviewDiscard, err: failure, description: "kritika: publication failed"}},
		{name: "restart cannot extend the attempt limit", jobCause: stopped, err: failure, checkpointed: true, final: true,
			want: reviewDecision{end: store.ReviewEnd{Status: store.ReviewFailed, Error: "forge unavailable", OnlyUnfinished: true}, disposition: reviewDiscard, err: failure, description: "kritika: publication failed"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			checkReviewDecision(t, decidePublication(tt.jobCause, tt.status, tt.err, tt.checkpointed, tt.final), tt.want)
		})
	}
}

func checkReviewDecision(t *testing.T, got, want reviewDecision) {
	t.Helper()
	if got.end != want.end || got.disposition != want.disposition || got.description != want.description {
		t.Fatalf("decision = %+v; want %+v", got, want)
	}
	err := got.jobError()
	if want.disposition == reviewSnooze {
		snooze, ok := errors.AsType[*river.JobSnoozeError](err)
		if !ok || snooze.Duration != time.Second {
			t.Fatalf("job error = %v, want one-second snooze", err)
		}
	} else if !errors.Is(err, want.err) {
		t.Fatalf("job error = %v; want %v", err, want.err)
	}
}
