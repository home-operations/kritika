package worker

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/home-operations/kritika/internal/executor"
	"github.com/home-operations/kritika/internal/runspec"
	"github.com/home-operations/kritika/internal/store"
)

// Causes supervision cancels a run with.
var (
	errSuperseded    = errors.New("worker: a newer head superseded the run")
	errReplaced      = errors.New("worker: a newer review of the pull request replaced the run")
	errHeartbeatLost = errors.New("worker: runner heartbeat lost")
)

// supersededBy is errSuperseded naming the newer head, for the run's Event.
type supersededBy string

func (supersededBy) Error() string { return errSuperseded.Error() }
func (supersededBy) Unwrap() error { return errSuperseded }

const (
	// superviseInterval is how often supervision looks at a running run.
	superviseInterval = 10 * time.Second
	// heartbeatStale is how old a runner's heartbeat can get before the run
	// is treated as dead. A heartbeat goes through the gateway as a report
	// does, so an outage silences both. This outlasts a report's last attempt,
	// and the beats either side of it, because ending the run sooner can lose
	// an answer already paid for. A pod that dies is seen sooner, by its Job.
	// Only a wedged runner waits this long.
	heartbeatStale = runspec.ReportRetryWindow + runspec.ReportTimeout + 2*runspec.HeartbeatInterval
)

// supervision is what the worker watches while a runner works.
type supervision struct {
	every, stale time.Duration
	// heartbeat reports the age of the run's last heartbeat, and whether the
	// runner stamped one at all.
	heartbeat func(context.Context) (age time.Duration, started bool, err error)
	// head reads the pull request's current head. It is nil for runs that no
	// newer head can supersede.
	head func(context.Context) (string, error)
	want string
	// replaced reports whether a newer review replaced the run's. It is nil
	// for runs that are not a review's.
	replaced func(context.Context) (bool, error)
	// logger reports a check that failed to run.
	logger *slog.Logger
}

// supervise runs spec and cancels it when its heartbeat goes stale, a newer
// head supersedes it, or a newer review replaces it. It returns the
// executor's result and the cause it canceled with, nil when it did not. An
// executor that runs Jobs records on the run's Job why kritika ended it.
func supervise(ctx context.Context, s supervision, exec executor.Executor, spec executor.Spec) (executor.Result, error) {
	rctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	done := make(chan executor.Result, 1)
	go func() { done <- exec.Run(rctx, spec) }()
	t := time.NewTicker(s.every)
	defer t.Stop()
	var cause error
	for {
		select {
		case res := <-done:
			if r, ok := exec.(executor.EventRecorder); ok {
				if warning, reason, note := runEnd(context.Cause(ctx), res); reason != "" {
					r.RunEvent(ctx, res, warning, reason, note)
				}
			}
			return res, cause
		case <-t.C:
		}
		if cause != nil || rctx.Err() != nil {
			continue
		}
		if cause = s.check(rctx); cause != nil {
			cancel(cause)
		}
	}
}

// check returns a reason to end the run, or nil. A read that fails is logged
// and retried on the next tick rather than ending the run. check looks for a
// replacement before it looks at the head: a newer head's review that started
// already ended this one, and that end stands.
func (s supervision) check(ctx context.Context) error {
	if s.replaced != nil {
		replaced, err := s.replaced(ctx)
		switch {
		case err != nil:
			s.logger.Warn("supervise: read whether the review was replaced", "error", err)
		case replaced:
			return errReplaced
		}
	}
	if s.head != nil {
		head, err := s.head(ctx)
		switch {
		case err != nil:
			s.logger.Warn("supervise: read head", "error", err)
		case head != s.want:
			return supersededBy(head)
		}
	}
	age, started, err := s.heartbeat(ctx)
	switch {
	case err != nil:
		s.logger.Warn("supervise: read heartbeat", "error", err)
	case started && age > s.stale:
		return errHeartbeatLost
	}
	return nil
}

// runEnd says why kritika ended a run, for an Event on its Job: whether the
// Event is a warning, its reason, and its note. It reads the executor's own
// account of the run, res, whose error carries the cause that a canceled run
// stopped with. It does not read whichever cause landed since: supervision
// can settle on one, or the job's ctx can end, after the run already stopped.
// jobCause is the job's: a run cut by a shutdown or the job's timeout stopped
// with it. A run that ended on its own has no reason. Its Job's own Events
// tell.
func runEnd(jobCause error, res executor.Result) (warning bool, reason, note string) {
	if res.Err == nil {
		return false, "", ""
	}
	if head, ok := errors.AsType[supersededBy](res.Err); ok {
		return false, "Superseded", fmt.Sprintf("a newer head, %s, superseded the run", string(head[:min(len(head), 8)]))
	}
	switch {
	case errors.Is(res.Err, errReplaced):
		return false, "Replaced", "a newer review of the pull request replaced the run"
	case errors.Is(res.Err, errHeartbeatLost):
		return true, "HeartbeatLost", "the runner's heartbeat went stale, so kritika ended the run"
	case errors.Is(res.Err, river.ErrJobCancelledRemotely):
		return false, "Cancelled", "the run was cancelled from the dashboard"
	case errors.Is(res.Err, errJobFenced):
		return true, "Interrupted", "the job's heartbeat stopped landing, so kritika handed the job back to its queue"
	case jobCause != nil && errors.Is(res.Err, jobCause) && errors.Is(jobCause, context.DeadlineExceeded):
		return true, "TimedOut", "the job ran out of time"
	case jobCause != nil && errors.Is(res.Err, jobCause):
		return false, "Interrupted", "kritika serve stopped, and the job runs again"
	case res.Disruption != "":
		return true, "Disrupted", "Kubernetes disrupted its pod: " + res.Disruption
	case res.Abandoned:
		return true, "StatusUnreadable", "kritika lost sight of the Job's status, so it deleted the Job"
	case res.NeverStarted:
		return true, "NeverStarted", "its pod did not start: " + cmp.Or(res.TerminationReason, res.Err.Error())
	}
	return false, "", ""
}

// runSupervision builds the supervision of one runner run. Heartbeat age is
// measured by the database's clock, the same clock that stamped it. prID
// empty skips the supersede and replacement checks of a review's run,
// reviewID.
func runSupervision(st *store.Store, accountID, runID, prID, reviewID, head string, every time.Duration, logger *slog.Logger) supervision {
	s := supervision{
		every: cmp.Or(every, superviseInterval), stale: heartbeatStale, logger: logger,
		heartbeat: func(ctx context.Context) (time.Duration, bool, error) {
			var age time.Duration
			var seen bool
			err := st.WithAccount(ctx, accountID, func(tx pgx.Tx) error {
				var err error
				age, seen, err = store.RunnerHeartbeatAge(ctx, tx, runID)
				return err
			})
			return age, seen, err
		},
	}
	if prID != "" {
		s.want = head
		s.head = func(ctx context.Context) (string, error) {
			var current string
			err := st.WithAccount(ctx, accountID, func(tx pgx.Tx) error {
				var err error
				current, err = store.PullRequestHead(ctx, tx, prID)
				return err
			})
			if err != nil {
				return "", fmt.Errorf("worker: read head: %w", err)
			}
			return current, nil
		}
		s.replaced = func(ctx context.Context) (bool, error) { return reviewReplaced(ctx, st, accountID, reviewID) }
	}
	return s
}

// reviewReplaced reports whether a newer review of the pull request
// replaced the review (see store.StartReview).
func reviewReplaced(ctx context.Context, st *store.Store, accountID, reviewID string) (bool, error) {
	var replaced bool
	err := st.WithAccount(ctx, accountID, func(tx pgx.Tx) error {
		var err error
		replaced, err = store.ReviewReplaced(ctx, tx, reviewID)
		return err
	})
	return replaced, err
}
