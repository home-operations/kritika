package worker

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/home-operations/kritika/internal/tuning"
	"github.com/riverqueue/river/rivertype"
)

// fakeHeartbeater records every beat and fails the first n, and every
// one while down or past its ctx. While hung, a beat waits for its ctx,
// as one on a connection that never answers does. Each beat takes delay.
type fakeHeartbeater struct {
	mu    sync.Mutex
	beats []int64
	fail  int
	down  bool
	hung  bool
	delay time.Duration
}

func (f *fakeHeartbeater) HeartbeatJob(ctx context.Context, jobID int64) error {
	if f.delay > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(f.delay):
		}
	}
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

func (f *fakeHeartbeater) StartJob(ctx context.Context, job *rivertype.JobRow) (time.Time, error) {
	return time.Now(), f.HeartbeatJob(ctx, job.ID)
}

func (f *fakeHeartbeater) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.beats)
}

func TestJobHeartbeatBeatsWhileTheJobRuns(t *testing.T) {
	jt := tuning.Get().JobTimeout
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
			time.Sleep(4*jt.HeartbeatInterval + jt.HeartbeatInterval/2)
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
		time.Sleep(3 * jt.HeartbeatInterval)
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
// beat landed for JobTimeout.JobFenceAfter, before the leader can hand it
// back. No beat is written after, though the job runs on.
func TestJobHeartbeatFencesTheJobOnceBeatsStopLanding(t *testing.T) {
	jt := tuning.Get().JobTimeout
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
			time.Sleep(3 * jt.HeartbeatInterval)
			return context.Cause(ctx)
		})
		if !errors.Is(err, errJobFenced) {
			t.Fatalf("Work = %v, want the job fenced", err)
		}
		if fencedAfter < jt.JobFenceAfter() || fencedAfter >= jt.HeartbeatAbandonedAfter() {
			t.Fatalf("fenced after %s, want from %s and before the leader may hand the job back at %s",
				fencedAfter, jt.JobFenceAfter(), jt.HeartbeatAbandonedAfter())
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
	jt := tuning.Get().JobTimeout
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
		if fencedAfter < jt.JobFenceAfter() || fencedAfter >= jt.HeartbeatAbandonedAfter() {
			t.Fatalf("fenced after %s, want from %s and before the leader may hand the job back at %s",
				fencedAfter, jt.JobFenceAfter(), jt.HeartbeatAbandonedAfter())
		}
	})
}

// TestJobHeartbeatOutlastsAShortOutage: three missed beats a full window
// into the job do not fence it: the window runs from the last beat
// written, not from the job's start.
func TestJobHeartbeatOutlastsAShortOutage(t *testing.T) {
	jt := tuning.Get().JobTimeout
	synctest.Test(t, func(t *testing.T) {
		store := &fakeHeartbeater{}
		h := &JobHeartbeat{Store: store, Logger: slog.New(slog.DiscardHandler)}
		// Half an interval off the ticks, so no beat and no wake fall at
		// the same instant.
		healthy := jt.JobFenceAfter() + jt.HeartbeatInterval/2
		err := h.Work(t.Context(), &rivertype.JobRow{ID: 9}, func(ctx context.Context) error {
			time.Sleep(healthy)
			store.mu.Lock()
			store.fail = 3
			store.mu.Unlock()
			time.Sleep(4 * jt.HeartbeatInterval)
			return context.Cause(ctx)
		})
		if err != nil {
			t.Fatalf("Work = %v, want the job to outlast three missed beats", err)
		}
		if got, want := store.count(), 1+int(healthy/jt.HeartbeatInterval)+1; got != want {
			t.Fatalf("beats written = %d, want %d: the first, one an interval until the outage, and one after it", got, want)
		}
	})
}

// TestJobHeartbeatAtTheFewestBeats: at the fewest abandoned beats that
// Validate allows, renewals that each take half an interval to write keep
// the job: the first has a beat of room before the fence.
func TestJobHeartbeatAtTheFewestBeats(t *testing.T) {
	tuning.Override(t, func(v *tuning.Values) { v.JobTimeout.AbandonedBeats = 3 })
	if err := tuning.Get().Validate(); err != nil {
		t.Fatalf("3 abandoned beats invalid: %v", err)
	}
	synctest.Test(t, func(t *testing.T) {
		interval := tuning.Get().JobTimeout.HeartbeatInterval
		store := &fakeHeartbeater{delay: interval / 2}
		h := &JobHeartbeat{Store: store, Logger: slog.New(slog.DiscardHandler)}
		err := h.Work(t.Context(), &rivertype.JobRow{ID: 9}, func(ctx context.Context) error {
			time.Sleep(5 * interval)
			return context.Cause(ctx)
		})
		if err != nil {
			t.Fatalf("Work = %v, want the job left to finish", err)
		}
		if got := store.count(); got < 5 {
			t.Fatalf("beats written = %d, want the first and a renewal an interval", got)
		}
	})
}

// slowAck stamps its first renewal at once but answers it late, then fails
// every renewal after it.
type slowAck struct {
	renewals int
	stamped  time.Time
	late     time.Duration
}

func (s *slowAck) StartJob(context.Context, *rivertype.JobRow) (time.Time, error) {
	return time.Now(), nil
}

func (s *slowAck) HeartbeatJob(ctx context.Context, _ int64) error {
	if s.renewals++; s.renewals > 1 {
		return errors.New("db down")
	}
	s.stamped = time.Now()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(s.late):
	}
	return nil
}

// TestJobHeartbeatFencesFromTheStampNotTheAnswer: a renewal that the
// database dates at once but answers late does not push the fence past the
// leader's count from that date.
func TestJobHeartbeatFencesFromTheStampNotTheAnswer(t *testing.T) {
	tuning.Override(t, func(v *tuning.Values) {
		v.JobTimeout.HeartbeatInterval, v.JobTimeout.AbandonedBeats = 10*time.Second, 3
	})
	synctest.Test(t, func(t *testing.T) {
		store := &slowAck{late: 16 * time.Second}
		h := &JobHeartbeat{Store: store, Logger: slog.New(slog.DiscardHandler)}
		var fenced time.Time
		err := h.Work(t.Context(), &rivertype.JobRow{ID: 9}, func(ctx context.Context) error {
			<-ctx.Done()
			fenced = time.Now()
			return context.Cause(ctx)
		})
		if !errors.Is(err, errJobFenced) {
			t.Fatalf("Work = %v, want the job fenced", err)
		}
		if abandoned := store.stamped.Add(tuning.Get().JobTimeout.HeartbeatAbandonedAfter()); !fenced.Before(abandoned) {
			t.Fatalf("fenced at %v, not before the leader may hand the job back at %v", fenced, abandoned)
		}
	})
}

// slowStart waits before it stamps the first beat, as a wait for a pool
// connection or River's row does, then beats at once.
type slowStart struct{ wait time.Duration }

func (s slowStart) StartJob(context.Context, *rivertype.JobRow) (time.Time, error) {
	time.Sleep(s.wait)
	return time.Now(), nil
}

func (slowStart) HeartbeatJob(context.Context, int64) error { return nil }

// TestJobHeartbeatFencesFromTheFirstBeatSent: a start that waits longer than
// the fence window before its first beat is not fenced for it: the fence
// counts from when that beat was sent.
func TestJobHeartbeatFencesFromTheFirstBeatSent(t *testing.T) {
	tuning.Override(t, func(v *tuning.Values) {
		v.JobTimeout.HeartbeatInterval, v.JobTimeout.AbandonedBeats = 10*time.Second, 3
	})
	synctest.Test(t, func(t *testing.T) {
		h := &JobHeartbeat{Store: slowStart{wait: 25 * time.Second}, Logger: slog.New(slog.DiscardHandler)}
		err := h.Work(t.Context(), &rivertype.JobRow{ID: 9}, func(ctx context.Context) error {
			time.Sleep(15 * time.Second)
			return context.Cause(ctx)
		})
		if err != nil {
			t.Fatalf("Work = %v, want the job left to finish", err)
		}
	})
}
