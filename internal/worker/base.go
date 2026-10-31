package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"

	"github.com/home-operations/kritika/internal/adapter"
	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/forge"
	"github.com/home-operations/kritika/internal/metrics"
	"github.com/home-operations/kritika/internal/store"
)

// Base is what every worker shares: the store, the live configuration,
// forge clients, logging and metrics, plus the lookups each job starts
// with.
type Base struct {
	Store   *store.Store
	Current *configfile.Current
	Forges  forge.Clients
	Logger  *slog.Logger
	// Metrics can be nil.
	Metrics *metrics.Metrics
}

// account finds the job's account in the current file. An account that was
// removed cancels the job: it will not come back by retrying.
func (b *Base) account(file *configfile.File, id string) (*configfile.Account, error) {
	if t, ok := file.AccountByID(id); ok {
		return t, nil
	}
	return nil, river.JobCancel(fmt.Errorf("worker: account %s is not in the configuration", id))
}

// client resolves the connection serving account to its forge client for
// repo.
func (b *Base) client(ctx context.Context, file *configfile.File, account *configfile.Account, repo string) (forge.Client, error) {
	in := file.ConnectionFor(account)
	if in == nil {
		return nil, river.JobCancel(fmt.Errorf("worker: no connection serves account %s", account.Key()))
	}
	return b.Forges.For(ctx, in, repo)
}

// runnerLabels are put on a runner Job for kubectl and Grafana. pr is 0 for a
// run that is of no pull request.
func runnerLabels(account, repository, kind string, pr int) map[string]string {
	labels := map[string]string{"account": account, "repository": repository, "kind": kind}
	if pr != 0 {
		labels["pr"] = strconv.Itoa(pr)
	}
	return labels
}

// runnerAnnotations name the River job that started a runner Job and the
// commit it works on.
func runnerAnnotations(jobID int64, sha string) map[string]string {
	return map[string]string{"river-job-id": strconv.FormatInt(jobID, 10), "head-sha": sha}
}

// releaseTimeout bounds the lease release after the job's context is gone.
const releaseTimeout = 10 * time.Second

// detachTimeout bounds work that runs on past the job's ctx once the model
// answers: publishing, and ending a review whose job ended. It must still
// finish then, but a hung forge or database call must not hold the job
// forever.
const detachTimeout = 2 * time.Minute

// completeJob completes job in tx while River still runs this attempt of it.
// River returns a row that is no longer running unchanged, and it completes
// whichever attempt is running. So either way, what tx holds of this attempt
// must not commit.
func completeJob[T river.JobArgs](ctx context.Context, tx pgx.Tx, job *river.Job[T]) error {
	completed, err := river.JobCompleteTx[*riverpgxv5.Driver](ctx, tx, job)
	if err != nil {
		return fmt.Errorf("worker: complete %s job: %w", job.Kind, err)
	}
	if completed.State != rivertype.JobStateCompleted || completed.Attempt != job.Attempt {
		return fmt.Errorf("worker: %s job ownership changed: state %s, attempt %d (want %d)",
			job.Kind, completed.State, completed.Attempt, job.Attempt)
	}
	return nil
}

// detach is ctx without its cancellation, bounded by detachTimeout.
func detach(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), detachTimeout)
}

// detachUnlessFenced is detach, but a fence of the job still ends it, with
// errJobFenced as its cause, even after River ended the job's context: the
// job is being handed to another replica, which must find no post of this
// one's landing after it looked.
func detachUnlessFenced(ctx context.Context) (context.Context, context.CancelFunc) {
	dctx, cancelTimeout := detach(ctx)
	dctx, cancel := context.WithCancelCause(dctx)
	fence := jobFence(ctx)
	stop := context.AfterFunc(fence, func() {
		if errors.Is(context.Cause(fence), errJobFenced) {
			cancel(errJobFenced)
		}
	})
	return dctx, func() {
		stop()
		cancel(nil)
		cancelTimeout()
	}
}

// withLease runs fn while it holds one of the account's slots on key, and
// records the wait. It releases the slot afterward, even when the job's
// context is canceled.
func (b *Base) withLease(
	ctx context.Context, account *configfile.Account, key string, slots int, jobID int64, fn func(ctx context.Context) error,
) error {
	waited := time.Now()
	l, err := b.Store.AcquireLease(ctx, account.ID(), key, slots, jobID)
	if err != nil {
		return err
	}
	b.Metrics.LeaseWait(account.Key(), key, time.Since(waited))
	defer b.releaseLease(ctx, b.Logger, l, key)
	return fn(ctx)
}

// releaseLease releases l on a context of its own, because the job's context
// is usually over by the time a lease is let go. It logs a failure, and the
// snoozed review that the freed slot woke, to logger. logger carries whatever
// the caller knows of the job.
func (b *Base) releaseLease(ctx context.Context, logger *slog.Logger, l *store.Lease, key string) {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), releaseTimeout)
	defer cancel()
	woken, err := l.Release(rctx)
	if err != nil {
		logger.Warn("lease not released", "key", key, "error", err)
	}
	if woken != 0 {
		logger.Info("snoozed review woken: a model slot is free", "key", key, "job", woken)
	}
}

// recorder writes model calls to the transcript view.
func (b *Base) recorder() adapter.Recorder {
	return adapter.Recorder{Store: b.Store, Metrics: b.Metrics}
}
