package worker

import (
	"context"
	"slices"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/home-operations/kritika/internal/jobs"
	"github.com/home-operations/kritika/internal/metrics"
)

// JobMetrics observes each pickup, including retries and snoozes, before
// work starts so long-running or crashed jobs do not hide queue delays.
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
