package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/riverqueue/river"

	"github.com/home-operations/kritika/internal/executor"
)

// fakeState is a run as the database would report it, changed by the test
// while the fake executor blocks.
type fakeState struct {
	mu       sync.Mutex
	started  bool
	age      time.Duration
	head     string
	replaced bool
	// gate, when set, holds the replacement check until it is closed.
	gate chan struct{}
}

func (f *fakeState) heartbeat(context.Context) (time.Duration, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.age, f.started, nil
}

func (f *fakeState) headSHA(context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.head, nil
}

func (f *fakeState) isReplaced(context.Context) (bool, error) {
	f.mu.Lock()
	gate := f.gate
	f.mu.Unlock()
	if gate != nil {
		<-gate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.replaced, nil
}

func (f *fakeState) set(fn func(*fakeState)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

// eventLog keeps the Events an executor that runs Jobs is asked to record.
type eventLog struct {
	mu     sync.Mutex
	events []string
}

func (l *eventLog) RunEvent(_ context.Context, res executor.Result, warning bool, reason, note string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, fmt.Sprintf("%s warning=%v %s: %s", res.JobName, warning, reason, note))
}

func (l *eventLog) recorded() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.events)
}

// blockingExecutor runs until its context ends, as a Job would until the
// executor deletes it.
type blockingExecutor struct {
	started chan struct{}
	eventLog
}

func (b *blockingExecutor) Run(ctx context.Context, _ executor.Spec) executor.Result {
	close(b.started)
	<-ctx.Done()
	return executor.Result{JobName: "kritika-run-x", Err: context.Cause(ctx)}
}

type instantExecutor struct{ eventLog }

func (*instantExecutor) Run(context.Context, executor.Spec) executor.Result {
	return executor.Result{JobName: "kritika-run-x"}
}

func TestSupervise(t *testing.T) {
	const head = "0123456789abcdef0123456789abcdef01234567"
	tests := []struct {
		name      string
		checkHead bool
		exec      executor.Executor
		// change is applied once the run has started.
		change    func(*fakeState)
		wantCause error
		// wantEvent is the Event recorded on the run's Job, if any.
		wantEvent string
	}{
		{name: "normal completion is untouched", checkHead: true, exec: &instantExecutor{}},
		{name: "stale heartbeat after start is lost", exec: &blockingExecutor{started: make(chan struct{})},
			change: func(f *fakeState) { f.started, f.age = true, 2*time.Minute }, wantCause: errHeartbeatLost,
			wantEvent: "kritika-run-x warning=true HeartbeatLost: the runner's heartbeat went stale, so kritika ended the run"},
		{name: "head moves while running is superseded", checkHead: true, exec: &blockingExecutor{started: make(chan struct{})},
			change: func(f *fakeState) { f.head = "89abcdef0123456789abcdef0123456789abcdef" }, wantCause: errSuperseded,
			wantEvent: "kritika-run-x warning=false Superseded: a newer head, 89abcdef, superseded the run"},
		{name: "a newer review starting while running is replaced", checkHead: true, exec: &blockingExecutor{started: make(chan struct{})},
			change: func(f *fakeState) { f.replaced = true }, wantCause: errReplaced,
			wantEvent: "kritika-run-x warning=false Replaced: a newer review of the pull request replaced the run"},
		{name: "a newer head's review that started is a replacement, not a supersession", checkHead: true,
			exec:   &blockingExecutor{started: make(chan struct{})},
			change: func(f *fakeState) { f.head, f.replaced = "89abcdef0123456789abcdef0123456789abcdef", true }, wantCause: errReplaced,
			wantEvent: "kritika-run-x warning=false Replaced: a newer review of the pull request replaced the run"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				state := &fakeState{head: head}
				s := supervision{
					every: 5 * time.Millisecond, stale: 90 * time.Second, heartbeat: state.heartbeat,
					logger: slog.New(slog.DiscardHandler),
				}
				if tt.checkHead {
					s.head, s.want, s.replaced = state.headSHA, head, state.isReplaced
				}
				done := make(chan struct{})
				var res executor.Result
				var cause error
				go func() {
					res, cause = supervise(t.Context(), s, tt.exec, executor.Spec{})
					close(done)
				}()
				if b, ok := tt.exec.(*blockingExecutor); ok {
					<-b.started
					// A fresh heartbeat before start must not end the run.
					synctest.Sleep(20 * time.Millisecond)
					select {
					case <-done:
						t.Fatalf("the run ended before anything changed, cause = %v", cause)
					default:
					}
					state.set(tt.change)
				}
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Fatal("supervise did not return")
				}
				if !errors.Is(cause, tt.wantCause) {
					t.Fatalf("cause = %v, want %v", cause, tt.wantCause)
				}
				if tt.wantCause == nil && res.Err != nil {
					t.Fatalf("result err = %v", res.Err)
				}
				if tt.wantCause != nil && !errors.Is(res.Err, tt.wantCause) {
					t.Fatalf("the executor must see the cause, got %v", res.Err)
				}
				synctest.Wait()
				want := []string{}
				if tt.wantEvent != "" {
					want = []string{tt.wantEvent}
				}
				if got := tt.exec.(interface{ recorded() []string }).recorded(); !slices.Equal(got, want) {
					t.Fatalf("events = %q, want %q", got, want)
				}
			})
		})
	}
}

// TestRunEnd: kritika names why it ended a run for the Event on its Job by
// the cause the executor stopped the run with, whatever cause landed
// since, and records nothing of a run that ended on its own.
func TestRunEnd(t *testing.T) {
	failed := errors.New("executor: job kritika-run-x failed: Error")
	for _, tt := range []struct {
		name     string
		jobCause error
		res      executor.Result
		want     string
	}{
		{name: "completed", res: executor.Result{}},
		{name: "failed on its own", res: executor.Result{Err: failed}},
		{name: "failed on its own as its job was cancelled", jobCause: river.ErrJobCancelledRemotely, res: executor.Result{Err: failed}},
		{name: "superseded", res: executor.Result{Err: supersededBy("89abcdef0123456789abcdef0123456789abcdef")},
			want: "false Superseded: a newer head, 89abcdef, superseded the run"},
		{name: "replaced, the job ending after", jobCause: context.Canceled, res: executor.Result{Err: errReplaced},
			want: "false Replaced: a newer review of the pull request replaced the run"},
		{name: "heartbeat lost", res: executor.Result{Err: errHeartbeatLost},
			want: "true HeartbeatLost: the runner's heartbeat went stale, so kritika ended the run"},
		{name: "cancelled from the dashboard", jobCause: river.ErrJobCancelledRemotely, res: executor.Result{Err: river.ErrJobCancelledRemotely},
			want: "false Cancelled: the run was cancelled from the dashboard"},
		{name: "fenced", jobCause: errJobFenced, res: executor.Result{Err: errJobFenced},
			want: "true Interrupted: the job's heartbeat stopped landing, so kritika handed the job back to its queue"},
		{name: "timed out", jobCause: context.DeadlineExceeded, res: executor.Result{Err: context.DeadlineExceeded},
			want: "true TimedOut: the job ran out of time"},
		{name: "shutdown", jobCause: context.Canceled, res: executor.Result{Err: context.Canceled},
			want: "false Interrupted: kritika serve stopped, and the job runs again"},
		{name: "never started", res: executor.Result{NeverStarted: true, TerminationReason: "ImagePullBackOff: back-off", Err: failed},
			want: "true NeverStarted: its pod did not start: ImagePullBackOff: back-off"},
		{name: "never started, with no waiting reason", res: executor.Result{NeverStarted: true, Err: errors.New("executor: create secret: refused")},
			want: "true NeverStarted: its pod did not start: executor: create secret: refused"},
		{name: "disrupted", res: executor.Result{Disruption: "EvictionByEvictionAPI: Eviction API: evicting", Err: failed},
			want: "true Disrupted: Kubernetes disrupted its pod: EvictionByEvictionAPI: Eviction API: evicting"},
		{name: "abandoned", res: executor.Result{Abandoned: true, Err: errors.New(`executor: job kritika-run-x: job.batch "kritika-run-x" not found`)},
			want: "true StatusUnreadable: kritika lost sight of the Job's status, so it deleted the Job"},
		{name: "cancelled from the dashboard while it was set up", jobCause: river.ErrJobCancelledRemotely,
			res:  executor.Result{NeverStarted: true, Err: fmt.Errorf("%w (%w)", fmt.Errorf("executor: create secret: %w", context.Canceled), river.ErrJobCancelledRemotely)},
			want: "false Cancelled: the run was cancelled from the dashboard"},
		{name: "a request's own timeout in setup is not the job's", res: executor.Result{NeverStarted: true,
			Err: fmt.Errorf("executor: create job: %w", context.DeadlineExceeded)},
			want: "true NeverStarted: its pod did not start: executor: create job: context deadline exceeded"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			warning, reason, note := runEnd(tt.jobCause, tt.res)
			got := ""
			if reason != "" {
				got = fmt.Sprintf("%v %s: %s", warning, reason, note)
			}
			if got != tt.want {
				t.Fatalf("runEnd = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestSuperviseRecordsWhatStoppedTheRun: a cancel from the dashboard that
// lands while supervision is still reading whether the review was replaced
// stops the run, and its Event says so, though supervision then settles on
// the replacement.
func TestSuperviseRecordsWhatStoppedTheRun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		state := &fakeState{gate: make(chan struct{})}
		ctx, cancel := context.WithCancelCause(t.Context())
		b := &blockingExecutor{started: make(chan struct{})}
		s := supervision{
			every: 5 * time.Millisecond, stale: time.Minute, heartbeat: state.heartbeat, replaced: state.isReplaced,
			logger: slog.New(slog.DiscardHandler),
		}
		done := make(chan error, 1)
		go func() {
			_, cause := supervise(ctx, s, b, executor.Spec{})
			done <- cause
		}()
		<-b.started
		// Supervision's tick is held in the replacement check.
		synctest.Sleep(10 * time.Millisecond)
		synctest.Wait()
		cancel(river.ErrJobCancelledRemotely)
		synctest.Wait()
		state.set(func(f *fakeState) { f.replaced = true })
		close(state.gate)
		if cause := <-done; !errors.Is(cause, errReplaced) {
			t.Fatalf("supervision's cause = %v, want the replacement it settled on", cause)
		}
		synctest.Wait()
		if got, want := b.recorded(), []string{"kritika-run-x warning=false Cancelled: the run was cancelled from the dashboard"}; !slices.Equal(got, want) {
			t.Fatalf("events = %q, want %q", got, want)
		}
	})
}

func TestSuperviseLeavesParentCancellation(t *testing.T) {
	state := &fakeState{}
	ctx, cancel := context.WithCancel(t.Context())
	b := &blockingExecutor{started: make(chan struct{})}
	go func() { <-b.started; cancel() }()
	res, cause := supervise(ctx, supervision{every: time.Hour, stale: time.Minute, heartbeat: state.heartbeat, logger: slog.New(slog.DiscardHandler)}, b, executor.Spec{})
	if cause != nil || !errors.Is(res.Err, context.Canceled) {
		t.Fatalf("cause = %v, err = %v", cause, res.Err)
	}
}
