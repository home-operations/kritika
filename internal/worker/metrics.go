package worker

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/home-operations/kritika/internal/forge/github"
	"github.com/home-operations/kritika/internal/jobs"
	"github.com/home-operations/kritika/internal/metrics"
)

// JobMetrics observes each pickup before work starts, retries and snoozes
// included, so long-running or crashed jobs do not hide queue delays.
type JobMetrics struct {
	river.MiddlewareDefaults
	Metrics *metrics.Metrics
}

// Work implements rivertype.WorkerMiddleware.
func (m *JobMetrics) Work(ctx context.Context, job *rivertype.JobRow, next func(context.Context) error) error {
	if slices.Contains(jobs.Queues(), job.Queue) {
		m.Metrics.QueueWait(job.Queue, max(0, time.Since(job.ScheduledAt)))
	}
	return next(ctx)
}

// ForgeRateLimit snoozes a job that failed on GitHub's rate limit until the
// limit allows it again. A snooze spends no attempt. River's backoff, by
// contrast, brings the job back inside the limit's window to fail again,
// until its attempts run out. A job that cancels itself is left to do so.
type ForgeRateLimit struct {
	river.MiddlewareDefaults
	Logger *slog.Logger
}

// Work implements rivertype.WorkerMiddleware.
func (m *ForgeRateLimit) Work(ctx context.Context, job *rivertype.JobRow, next func(context.Context) error) error {
	err := next(ctx)
	if _, cancelled := errors.AsType[*river.JobCancelError](err); cancelled {
		return err
	}
	wait, limited := github.RetryAfter(err, time.Now())
	if !limited {
		return err
	}
	m.Logger.Warn("job snoozed for GitHub's rate limit", "job", job.ID, "kind", job.Kind, "for", wait, "error", err)
	return river.JobSnooze(wait)
}
