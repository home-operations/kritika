package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/home-operations/kritika/internal/tuning"
)

// errJobFenced is the cause a job's context ends with when its replica failed
// to write the job's heartbeat for JobTimeout.JobFenceAfter.
var errJobFenced = errors.New("worker: job heartbeat not written; the job is handed back")

// jobFenceKey carries, in the context of a job JobHeartbeat works, a
// context only a fence of the job ends.
type jobFenceKey struct{}

// jobFence is a context that a fence of the job ends, with errJobFenced as
// its cause. A fence ends it even after River ended the job's own context,
// whose cause then stays River's. Outside JobHeartbeat, it is ctx itself.
func jobFence(ctx context.Context) context.Context {
	if f, ok := ctx.Value(jobFenceKey{}).(context.Context); ok {
		return f
	}
	return ctx
}

// jobHeartbeater stamps a job's heartbeat, and records its rescue horizon.
type jobHeartbeater interface {
	HeartbeatJob(ctx context.Context, jobID int64) error
	StartJob(ctx context.Context, job *rivertype.JobRow) error
}

// JobHeartbeat is River middleware that stamps a heartbeat on every job this
// replica works. It stamps once before the job starts and then every
// tuning's JobTimeout.HeartbeatInterval until Work returns. So the leader's
// Rescuer can tell a job whose replica died, its last beat older than
// JobTimeout.HeartbeatAbandonedAfter, from one still being worked. A replica
// that stops drains its own jobs. This covers one that does not get to.
type JobHeartbeat struct {
	river.MiddlewareDefaults
	Store  jobHeartbeater
	Logger *slog.Logger
}

// Work implements rivertype.WorkerMiddleware. A first beat that cannot be
// written fails the attempt: a job that nobody can tell is alive must not
// start. Later beats go on past ctx, which a timeout or a cancel ends while
// the job is still this replica's to finish on detached contexts. The job
// gets a context of its own, which beats that cannot be written for
// JobTimeout.JobFenceAfter end with errJobFenced, as they end its jobFence.
func (h *JobHeartbeat) Work(ctx context.Context, job *rivertype.JobRow, doInner func(context.Context) error) error {
	// The first beat, with the rescue horizon that River's rescuer waits out
	// (see riverTimeout), and only while the attempt is still this replica's.
	started := time.Now()
	if err := h.Store.StartJob(ctx, job); err != nil {
		return fmt.Errorf("worker: first heartbeat of job %d: %w", job.ID, err)
	}
	fenced, markFenced := context.WithCancelCause(context.WithoutCancel(ctx))
	defer markFenced(nil)
	jctx, fence := context.WithCancelCause(context.WithValue(ctx, jobFenceKey{}, fenced))
	defer fence(nil)
	hctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	done := make(chan struct{})
	go h.beat(hctx, job.ID, started, func(cause error) {
		markFenced(cause)
		fence(cause)
	}, done)
	defer func() {
		cancel()
		<-done
	}()
	return doInner(jctx)
}

// beat writes the job's heartbeat every interval until ctx ends, or until
// none was written for tuning's JobTimeout.JobFenceAfter. Then it fences
// the job and stops, because a beat written later has the leader take the job
// for alive after this replica cut it. A beat gets only what is left of that
// window. A beat stuck waiting for a connection, or on a connection that the
// network dropped, must not hold the fence back. A beat counts from when it
// was sent, not when its answer came: the database dates it no earlier, and
// the leader counts from that date.
func (h *JobHeartbeat) beat(ctx context.Context, jobID int64, written time.Time, fence context.CancelCauseFunc, done chan<- struct{}) {
	defer close(done)
	jt := tuning.Get().JobTimeout
	next := written
	cut := func() {
		h.Logger.Error("job heartbeat not written for too long; the job is cut before the leader hands it back", "job", jobID)
		fence(errJobFenced)
	}
	for {
		next = next.Add(jt.HeartbeatInterval)
		fenceAt := written.Add(jt.JobFenceAfter())
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Until(fenceAt)):
			cut()
			return
		case <-time.After(time.Until(next)):
		}
		sent := time.Now()
		bctx, cancel := context.WithDeadline(ctx, fenceAt)
		err := h.Store.HeartbeatJob(bctx, jobID)
		cancel()
		if err == nil {
			written = sent
			continue
		}
		if ctx.Err() != nil {
			return
		}
		// A missed beat is not fatal: the job goes on, and only a run of
		// them has the leader rescue it, and this replica let go of it.
		unwritten := time.Since(written)
		h.Logger.Warn("job heartbeat not written", "job", jobID, "error", err, "unwritten_for", unwritten)
		if unwritten >= jt.JobFenceAfter() {
			cut()
			return
		}
	}
}
