package worker

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/riverqueue/river/rivertype"
)

// fakeHeartbeater records every beat and fails the first n, and every
// one while down or past its ctx. While hung, a beat waits for its ctx,
// as one on a connection that never answers does.
type fakeHeartbeater struct {
	mu    sync.Mutex
	beats []int64
	fail  int
	down  bool
	hung  bool
}

func (f *fakeHeartbeater) HeartbeatJob(ctx context.Context, jobID int64) error {
	f.mu.Lock()
	if f.hung {
		f.mu.Unlock()
		<-ctx.Done()
		return ctx.Err()
	}
	defer f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if f.fail > 0 {
		f.fail--
		return errors.New("db down")
	}
	if f.down {
		return errors.New("db down")
	}
	f.beats = append(f.beats, jobID)
	return nil
}

func (f *fakeHeartbeater) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.beats)
}

func TestJobHeartbeatBeatsWhileTheJobRuns(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := &fakeHeartbeater{}
		h := &JobHeartbeat{Store: store, Logger: slog.New(slog.DiscardHandler)}
		job := &rivertype.JobRow{ID: 42}
		var atStart, during int
		// The job's own context ends before Work does, as a timeout or a
		// cancel ends it. The beats go on until Work returns.
		jctx, cancel := context.WithCancel(t.Context())
		err := h.Work(jctx, job, func(context.Context) error {
			atStart = store.count()
			cancel()
			time.Sleep(4*jobHeartbeatInterval + jobHeartbeatInterval/2)
			during = store.count()
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if atStart != 1 {
			t.Fatalf("beats before the job started = %d, want the first one", atStart)
		}
		if during != 5 {
			t.Fatalf("beats over four and a half intervals = %d, want the first and one an interval", during)
		}
		time.Sleep(3 * jobHeartbeatInterval)
		if got := store.count(); got != during {
			t.Fatalf("beats after Work returned = %d, want none past %d", got, during)
		}
		for _, id := range store.beats {
			if id != 42 {
				t.Fatalf("beat for job %d, want 42", id)
			}
		}
	})
}

func TestJobHeartbeatRefusesToStartUnseen(t *testing.T) {
	store := &fakeHeartbeater{fail: 1}
	h := &JobHeartbeat{Store: store, Logger: slog.New(slog.DiscardHandler)}
	ran := false
	err := h.Work(t.Context(), &rivertype.JobRow{ID: 7}, func(context.Context) error {
		ran = true
		return nil
	})
	if err == nil || ran {
		t.Fatalf("Work = %v, ran = %v; want an error and the job not run", err, ran)
	}
	if store.count() != 0 {
		t.Fatalf("beats = %d, want none", store.count())
	}
}

// TestJobHeartbeatFencesTheJobOnceBeatsStopLanding: the job is cut once no
// beat landed for jobFenceAfter, before the leader can hand it back. No beat
// is written after, though the job runs on.
func TestJobHeartbeatFencesTheJobOnceBeatsStopLanding(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := &fakeHeartbeater{}
		h := &JobHeartbeat{Store: store, Logger: slog.New(slog.DiscardHandler)}
		start := time.Now()
		var fencedAfter time.Duration
		err := h.Work(t.Context(), &rivertype.JobRow{ID: 9}, func(ctx context.Context) error {
			store.mu.Lock()
			store.down = true
			store.mu.Unlock()
			<-ctx.Done()
			fencedAfter = time.Since(start)
			// A cut job can finish on contexts of its own. The database, back
			// meanwhile, takes no beat.
			store.mu.Lock()
			store.down = false
			store.mu.Unlock()
			time.Sleep(3 * jobHeartbeatInterval)
			return context.Cause(ctx)
		})
		if !errors.Is(err, errJobFenced) {
			t.Fatalf("Work = %v, want the job fenced", err)
		}
		if fencedAfter < jobFenceAfter || fencedAfter >= jobAbandonedAfter {
			t.Fatalf("fenced after %s, want from %s and before the leader may hand the job back at %s",
				fencedAfter, jobFenceAfter, jobAbandonedAfter)
		}
		if got := store.count(); got != 1 {
			t.Fatalf("beats written = %d, want only the first: a beat after the fence would hide the cut from the leader", got)
		}
	})
}

// TestJobHeartbeatFencesAJobRiverEnded: a fence that comes after River
// ended the job's context, whose cause it then cannot change, still shows
// through jobFence.
func TestJobHeartbeatFencesAJobRiverEnded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := &fakeHeartbeater{}
		h := &JobHeartbeat{Store: store, Logger: slog.New(slog.DiscardHandler)}
		ctx, stop := context.WithCancel(t.Context())
		defer stop()
		err := h.Work(ctx, &rivertype.JobRow{ID: 9}, func(ctx context.Context) error {
			stop()
			store.mu.Lock()
			store.down = true
			store.mu.Unlock()
			<-jobFence(ctx).Done()
			if errors.Is(context.Cause(ctx), errJobFenced) {
				return errors.New("the job's context took the fence's cause after River's")
			}
			return context.Cause(jobFence(ctx))
		})
		if !errors.Is(err, errJobFenced) {
			t.Fatalf("Work = %v, want the fence seen after River's stop", err)
		}
	})
}

// TestJobHeartbeatFencesTheJobWhileABeatHangs: a beat that never returns
// still has the job fenced once the window is up.
func TestJobHeartbeatFencesTheJobWhileABeatHangs(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := &fakeHeartbeater{}
		h := &JobHeartbeat{Store: store, Logger: slog.New(slog.DiscardHandler)}
		start := time.Now()
		var fencedAfter time.Duration
		err := h.Work(t.Context(), &rivertype.JobRow{ID: 9}, func(ctx context.Context) error {
			store.mu.Lock()
			store.hung = true
			store.mu.Unlock()
			<-ctx.Done()
			fencedAfter = time.Since(start)
			return context.Cause(ctx)
		})
		if !errors.Is(err, errJobFenced) {
			t.Fatalf("Work = %v, want the job fenced", err)
		}
		if fencedAfter < jobFenceAfter || fencedAfter >= jobAbandonedAfter {
			t.Fatalf("fenced after %s, want from %s and before the leader may hand the job back at %s",
				fencedAfter, jobFenceAfter, jobAbandonedAfter)
		}
	})
}

// TestJobHeartbeatOutlastsAShortOutage: three missed beats a full window
// into the job do not fence it: the window runs from the last beat
// written, not from the job's start.
func TestJobHeartbeatOutlastsAShortOutage(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := &fakeHeartbeater{}
		h := &JobHeartbeat{Store: store, Logger: slog.New(slog.DiscardHandler)}
		// Half an interval off the ticks, so no beat and no wake fall at
		// the same instant.
		healthy := jobFenceAfter + jobHeartbeatInterval/2
		err := h.Work(t.Context(), &rivertype.JobRow{ID: 9}, func(ctx context.Context) error {
			time.Sleep(healthy)
			store.mu.Lock()
			store.fail = 3
			store.mu.Unlock()
			time.Sleep(4 * jobHeartbeatInterval)
			return context.Cause(ctx)
		})
		if err != nil {
			t.Fatalf("Work = %v, want the job to outlast three missed beats", err)
		}
		if got, want := store.count(), 1+int(healthy/jobHeartbeatInterval)+1; got != want {
			t.Fatalf("beats written = %d, want %d: the first, one an interval until the outage, and one after it", got, want)
		}
	})
}
