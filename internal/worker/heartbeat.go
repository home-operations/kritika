package worker

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

// A replica's liveness on the River jobs it works: a beat every
// jobHeartbeatInterval, and a job whose last beat is older than
// jobAbandonedAfter belongs to a replica that died. Six missed beats, as a
// runner's heartbeat gets, so a slow database alone never has a live job
// rescued and run twice. A replica that cannot write a beat for
// jobFenceAfter fences itself: it ends the job's context an interval
// before the leader may hand the job to another replica, so a replica cut
// off from the database alone, its runner pod still working, never has
// its job worked twice either.
const (
	jobHeartbeatInterval = 30 * time.Second
	jobAbandonedAfter    = 6 * jobHeartbeatInterval
	jobFenceAfter        = jobAbandonedAfter - jobHeartbeatInterval
)

// errJobFenced is the cause a job's context ends with when its replica
// could not write the job's heartbeat for jobFenceAfter.
var errJobFenced = errors.New("worker: job heartbeat not written; the job is handed back")

// jobHeartbeater stamps a job's heartbeat.
type jobHeartbeater interface {
	HeartbeatJob(ctx context.Context, jobID int64) error
}

// JobHeartbeat is River middleware that stamps a heartbeat on every job
// this replica works, once before the job starts and then every
// jobHeartbeatInterval until Work returns, so the leader's Rescuer can tell
// a job whose replica died from one still being worked. A replica that
// stops drains its own jobs; this covers one that does not get to.
type JobHeartbeat struct {
	river.MiddlewareDefaults
	Store  jobHeartbeater
	Logger *slog.Logger
	// every and fence override jobHeartbeatInterval and jobFenceAfter in
	// tests.
	every, fence time.Duration
}

// Work implements rivertype.WorkerMiddleware. A first beat that cannot be
// written fails the attempt: a job nobody could tell was alive must not
// start. Later beats go on past ctx, which a timeout or a cancel ends
// while the job is still this replica's to finish on detached contexts.
// The job gets a context of its own, which beats that cannot be written
// for jobFenceAfter end with errJobFenced.
func (h *JobHeartbeat) Work(ctx context.Context, job *rivertype.JobRow, doInner func(context.Context) error) error {
	if err := h.Store.HeartbeatJob(ctx, job.ID); err != nil {
		return fmt.Errorf("worker: first heartbeat of job %d: %w", job.ID, err)
	}
	jctx, fence := context.WithCancelCause(ctx)
	defer fence(nil)
	hctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	done := make(chan struct{})
	go h.beat(hctx, job.ID, fence, done)
	defer func() {
		cancel()
		<-done
	}()
	return doInner(jctx)
}

// beat writes the job's heartbeat every interval until ctx ends, or until
// none could be written for jobFenceAfter: then it fences the job and
// stops, since a beat written later would have the leader take the job
// for alive after this replica cut it. A beat gets only what is left of
// that window: one stuck waiting for a connection, or on one the network
// dropped, must not hold the fence back.
func (h *JobHeartbeat) beat(ctx context.Context, jobID int64, fence context.CancelCauseFunc, done chan<- struct{}) {
	defer close(done)
	t := time.NewTicker(cmp.Or(h.every, jobHeartbeatInterval))
	defer t.Stop()
	fenceAfter := cmp.Or(h.fence, jobFenceAfter)
	written := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		bctx, cancel := context.WithDeadline(ctx, written.Add(fenceAfter))
		err := h.Store.HeartbeatJob(bctx, jobID)
		cancel()
		if err == nil {
			written = time.Now()
			continue
		}
		if ctx.Err() != nil {
			return
		}
		// A missed beat is not fatal: the job goes on, and only a run of
		// them has the leader rescue it, and this replica let go of it.
		unwritten := time.Since(written)
		h.Logger.Warn("job heartbeat not written", "job", jobID, "error", err, "unwritten_for", unwritten)
		if unwritten >= fenceAfter {
			h.Logger.Error("job heartbeat not written for too long; the job is cut before the leader hands it back", "job", jobID)
			fence(errJobFenced)
			return
		}
	}
}
