package worker

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/home-operations/kritika/internal/jobs"
	"github.com/home-operations/kritika/internal/jobtimeout"
	"github.com/home-operations/kritika/internal/store"
)

// Prepare consumes saved runner answers independently of runner admission.
type Prepare struct {
	river.WorkerDefaults[jobs.PrepareArgs]
	Review *Review
}

func (w *Prepare) Work(ctx context.Context, job *river.Job[jobs.PrepareArgs]) error {
	_, err := w.Review.resumePublication(ctx, &river.Job[jobs.ReviewArgs]{JobRow: job.JobRow, Args: job.Args.ReviewArgs},
		w.Review.Current.Get(), w.Review.Logger)
	return err
}

func (*Prepare) Timeout(*river.Job[jobs.PrepareArgs]) time.Duration {
	return jobtimeout.LeaseWaitHeadroom + jobtimeout.PublishHeadroom
}

// Publish consumes finalized answers independently of model capacity.
type Publish struct {
	river.WorkerDefaults[jobs.PublishArgs]
	Review *Review
}

func (w *Publish) Work(ctx context.Context, job *river.Job[jobs.PublishArgs]) error {
	_, err := w.Review.resumePublication(ctx, &river.Job[jobs.ReviewArgs]{JobRow: job.JobRow, Args: job.Args.ReviewArgs},
		w.Review.Current.Get(), w.Review.Logger)
	return err
}

func (*Publish) Timeout(*river.Job[jobs.PublishArgs]) time.Duration {
	return jobtimeout.PublishHeadroom
}

// advance transfers cancellation and rescue ownership with the checkpoint and
// the next job. Completing the current attempt in this transaction fences a
// stale worker and rolls back the entire handoff if it no longer owns the job.
func (p *publishPhase) advance(ctx context.Context, tx pgx.Tx, job *river.Job[jobs.ReviewArgs], next river.JobArgs) error {
	inserted, err := river.ClientFromContext[pgx.Tx](ctx).InsertTx(ctx, tx, next, nil)
	if err != nil {
		return fmt.Errorf("worker: enqueue review stage: %w", err)
	}
	if err := store.AdvancePublication(ctx, tx, p.reviewID, job.ID, inserted.Job.ID); err != nil {
		return err
	}
	return completeJob(ctx, tx, job)
}
