package worker

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/executor"
	"github.com/home-operations/kritika/internal/runner"
	"github.com/home-operations/kritika/internal/store"
)

// Causes supervision cancels a run with.
var (
	errSuperseded    = errors.New("worker: a newer head superseded the run")
	errReplaced      = errors.New("worker: a newer review of the pull request replaced the run")
	errHeartbeatLost = errors.New("worker: runner heartbeat lost")
)

const (
	// superviseInterval is how often a running run is checked.
	superviseInterval = 10 * time.Second
	// heartbeatStale is how old a runner's heartbeat may get before the run
	// is treated as dead: six missed beats, to ride out a slow database.
	heartbeatStale = 6 * runner.HeartbeatInterval
)

// supervision is what the worker watches while a runner works.
type supervision struct {
	every, stale time.Duration
	// heartbeat reports the age of the run's last heartbeat, and whether
	// the runner has stamped one at all.
	heartbeat func(context.Context) (age time.Duration, started bool, err error)
	// head reads the pull request's current head; nil for runs no newer
	// head can supersede.
	head func(context.Context) (string, error)
	want string
	// replaced reports whether a newer review replaced the run's; nil for
	// runs that are not a review's.
	replaced func(context.Context) (bool, error)
	// logger reports a check that could not be made.
	logger *slog.Logger
}

// supervise runs spec, cancelling it when its heartbeat goes stale, a newer
// head supersedes it or a newer review replaces it. It returns the
// executor's result and the cause it cancelled with, nil when it did not.
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

// check returns a reason to end the run, or nil. A read that fails is
// logged and retried on the next tick rather than ending the run. A
// replacement is checked before the head: a newer head's review that
// started already ended this one, and that end stands.
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
			return errSuperseded
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
