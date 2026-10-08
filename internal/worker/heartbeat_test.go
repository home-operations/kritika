package worker

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/riverqueue/river/rivertype"
)

// fakeHeartbeater records every beat and fails the first n, and every
// one while down. While hung, a beat waits for its ctx, as one on a
// connection that never answers does.
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
	store := &fakeHeartbeater{}
	h := &JobHeartbeat{Store: store, Logger: slog.New(slog.DiscardHandler), every: 5 * time.Millisecond}
	job := &rivertype.JobRow{ID: 42}
	var atStart int
	// The job's own context ends before Work does, as a timeout or a
	// cancel ends it; the beats go on until Work returns.
	jctx, cancel := context.WithCancel(t.Context())
	err := h.Work(jctx, job, func(context.Context) error {
		atStart = store.count()
		cancel()
		deadline := time.Now().Add(time.Second)
		for store.count() < 4 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if atStart != 1 {
		t.Fatalf("beats before the job started = %d, want the first one", atStart)
	}
	after := store.count()
	if after < 4 {
		t.Fatalf("beats while the job ran = %d, want at least 4", after)
	}
	time.Sleep(30 * time.Millisecond)
	if got := store.count(); got != after {
		t.Fatalf("beats after Work returned = %d, want none past %d", got, after)
	}
	for _, id := range store.beats {
		if id != 42 {
			t.Fatalf("beat for job %d, want 42", id)
		}
	}
}

func TestJobHeartbeatRefusesToStartUnseen(t *testing.T) {
	store := &fakeHeartbeater{fail: 1}
	h := &JobHeartbeat{Store: store, Logger: slog.New(slog.DiscardHandler), every: time.Millisecond}
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

func TestJobHeartbeatFencesTheJobOnceBeatsStopLanding(t *testing.T) {
	store := &fakeHeartbeater{}
	h := &JobHeartbeat{Store: store, Logger: slog.New(slog.DiscardHandler), every: time.Millisecond, fence: 5 * time.Millisecond}
	err := h.Work(t.Context(), &rivertype.JobRow{ID: 9}, func(ctx context.Context) error {
		store.mu.Lock()
		store.down = true
		store.mu.Unlock()
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-time.After(5 * time.Second):
			return errors.New("the job was never fenced")
		}
	})
	if !errors.Is(err, errJobFenced) {
		t.Fatalf("Work = %v, want the job fenced", err)
	}
	if got := store.count(); got != 1 {
		t.Fatalf("beats written = %d, want only the first", got)
	}
	time.Sleep(10 * time.Millisecond)
	store.mu.Lock()
	store.down = false
	store.mu.Unlock()
	time.Sleep(10 * time.Millisecond)
	if got := store.count(); got != 1 {
		t.Fatalf("beats after the fence = %d, want none: a late beat would hide the cut from the leader", got-1)
	}
}

// TestJobHeartbeatFencesTheJobWhileABeatHangs: a beat that never returns
// still has the job fenced once the window is up.
func TestJobHeartbeatFencesTheJobWhileABeatHangs(t *testing.T) {
	store := &fakeHeartbeater{}
	h := &JobHeartbeat{Store: store, Logger: slog.New(slog.DiscardHandler), every: time.Millisecond, fence: 20 * time.Millisecond}
	err := h.Work(t.Context(), &rivertype.JobRow{ID: 9}, func(ctx context.Context) error {
		store.mu.Lock()
		store.hung = true
		store.mu.Unlock()
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-time.After(5 * time.Second):
			return errors.New("the job was never fenced")
		}
	})
	if !errors.Is(err, errJobFenced) {
		t.Fatalf("Work = %v, want the job fenced", err)
	}
}

func TestJobHeartbeatOutlastsAShortOutage(t *testing.T) {
	store := &fakeHeartbeater{}
	h := &JobHeartbeat{Store: store, Logger: slog.New(slog.DiscardHandler), every: time.Millisecond, fence: time.Second}
	err := h.Work(t.Context(), &rivertype.JobRow{ID: 9}, func(ctx context.Context) error {
		store.mu.Lock()
		store.fail = 3
		store.mu.Unlock()
		deadline := time.Now().Add(time.Second)
		for store.count() < 4 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		return ctx.Err()
	})
	if err != nil {
		t.Fatalf("Work = %v, want the job to outlast three missed beats", err)
	}
}
