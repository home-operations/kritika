package worker

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/configfile/configfiletest"
)

func retentionAccount(t *testing.T) *configfile.File {
	t.Helper()
	t.Setenv("TEST_RETENTION_TOKEN", "tok")
	return configfiletest.Load(t, `
apps:
  acme-bot:
    accounts: [acme]
    clientId: Iv1.test
    privateKey: { env: TEST_RETENTION_TOKEN }
    webhookSecret: { env: TEST_RETENTION_TOKEN }
`)
}

// sweepCall is one call that fakeRetentionStore received, and it carries the
// argument that the call was given. The value is sent on the channel rather
// than stashed in a field that the test reads separately. So the data only
// ever crosses goroutines through the channel op itself. There is no shared
// state for a later, unsynchronized pass to race against.
type sweepCall struct {
	name    string
	role    string
	swept   time.Duration
	sweptAt time.Time
	runs    func(accountID string, t configfile.RepoTraits) bool
}

// fakeRetentionStore signals every call on a channel, so a test can wait for
// a specific pass instead of sleeping.
type fakeRetentionStore struct {
	calls chan sweepCall
	fail  bool
	// drained is what DrainLegacyIndexChunks moves and deletes on its calls
	// in turn, and then none.
	drained [][2]int64
}

func (s *fakeRetentionStore) SweepModelCalls(_ context.Context, olderThan time.Duration) (int64, error) {
	s.calls <- sweepCall{name: "modelCalls", swept: olderThan}
	if s.fail {
		return 0, errors.New("db down")
	}
	return 1, nil
}

func (s *fakeRetentionStore) SweepDiffs(_ context.Context, olderThan time.Duration) (int64, error) {
	s.calls <- sweepCall{name: "diffs", swept: olderThan}
	if s.fail {
		return 0, errors.New("db down")
	}
	return 1, nil
}

func (s *fakeRetentionStore) SweepSessions(_ context.Context, now time.Time) (int64, error) {
	s.calls <- sweepCall{name: "sessions", sweptAt: now}
	if s.fail {
		return 0, errors.New("db down")
	}
	return 1, nil
}

func (s *fakeRetentionStore) SweepStoppedIndexes(
	_ context.Context, grace time.Duration, runs func(accountID string, t configfile.RepoTraits) bool,
) (int64, error) {
	s.calls <- sweepCall{name: "stoppedIndexes", swept: grace, runs: runs}
	if s.fail {
		return 0, errors.New("db down")
	}
	return 1, nil
}

func (s *fakeRetentionStore) SweepConversations(_ context.Context, olderThan time.Duration) (int64, error) {
	s.calls <- sweepCall{name: "conversations", swept: olderThan}
	if s.fail {
		return 0, errors.New("db down")
	}
	return 1, nil
}

func (s *fakeRetentionStore) SweepEgressRefusals(_ context.Context, olderThan time.Duration) (int64, error) {
	s.calls <- sweepCall{name: "egressRefusals", swept: olderThan}
	if s.fail {
		return 0, errors.New("db down")
	}
	return 1, nil
}

func (s *fakeRetentionStore) SweepModelInvocations(_ context.Context, olderThan time.Duration) (int64, error) {
	s.calls <- sweepCall{name: "modelInvocations", swept: olderThan}
	if s.fail {
		return 0, errors.New("db down")
	}
	return 1, nil
}

func (s *fakeRetentionStore) SweepSimilarExclusions(context.Context) (int64, error) {
	s.calls <- sweepCall{name: "similarExclusions"}
	if s.fail {
		return 0, errors.New("db down")
	}
	return 1, nil
}

// warnCounter counts warn-level (or higher) records.
type warnCounter struct{ n atomic.Int32 }

func (h *warnCounter) Enabled(context.Context, slog.Level) bool { return true }
func (h *warnCounter) Handle(_ context.Context, r slog.Record) error {
	if r.Level >= slog.LevelWarn {
		h.n.Add(1)
	}
	return nil
}
func (h *warnCounter) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *warnCounter) WithGroup(string) slog.Handler      { return h }

func TestRetentionSweep(t *testing.T) {
	current := configfile.NewCurrent(retentionAccount(t))
	st := &fakeRetentionStore{calls: make(chan sweepCall, 16), drained: [][2]int64{{3, 0}, {0, 2}}}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		(&Retention{Store: st, Current: current, Logger: slog.New(slog.DiscardHandler), AppRole: "kritika_app", every: 5 * time.Millisecond}).Run(ctx)
		close(done)
	}()

	next := func() sweepCall {
		select {
		case c := <-st.calls:
			return c
		case <-time.After(5 * time.Second):
			t.Fatal("Retention never called the store")
			return sweepCall{}
		}
	}
	// The first pass runs immediately, without waiting for a tick.
	first := next()
	if first.name != "modelCalls" {
		t.Fatalf("first call = %q, want modelCalls", first.name)
	}
	if want := current.Get().TranscriptRetention(); first.swept != want {
		t.Fatalf("olderThan = %s, want %s", first.swept, want)
	}
	second := next()
	if second.name != "diffs" {
		t.Fatalf("second call = %q, want diffs", second.name)
	}
	if want := current.Get().DiffRetention(); second.swept != want {
		t.Fatalf("olderThan = %s, want %s", second.swept, want)
	}
	if got := next(); got.name != "sessions" {
		t.Fatalf("third call = %q, want sessions", got.name)
	}
	if fourth := next(); fourth.name != "conversations" || fourth.swept != conversationRetention {
		t.Fatalf("fourth call = %q older than %s, want conversations older than %s", fourth.name, fourth.swept, conversationRetention)
	}
	if fifth := next(); fifth.name != "egressRefusals" || fifth.swept != current.Get().TranscriptRetention() {
		t.Fatalf("fifth call = %q older than %s, want egressRefusals older than the transcript retention", fifth.name, fifth.swept)
	}
	if sixth := next(); sixth.name != "modelInvocations" || sixth.swept != current.Get().TranscriptRetention() {
		t.Fatalf("sixth call = %q older than %s, want modelInvocations older than the transcript retention",
			sixth.name, sixth.swept)
	}
	if got := next(); got.name != "similarExclusions" {
		t.Fatalf("seventh call = %q, want similarExclusions", got.name)
	}
	// No IndexReady: the index work follows at once.
	stopped := next()
	if stopped.name != "stoppedIndexes" {
		t.Fatalf("eighth call = %q, want stoppedIndexes", stopped.name)
	}
	if want := current.Get().DisabledIndexGrace(); stopped.swept != want {
		t.Fatalf("grace = %s, want %s", stopped.swept, want)
	}
	acme := configfile.AccountID(configfile.ForgeGitHub, "acme")
	if !stopped.runs(acme, configfile.RepoTraits{}) || stopped.runs(acme, configfile.RepoTraits{Archived: true}) ||
		stopped.runs(configfile.AccountID(configfile.ForgeGitHub, "gone"), configfile.RepoTraits{}) {
		t.Fatal("a repository runs as the configuration says, and none of an account it does not serve does")
	}
	if got := next(); got.name != "partitionIndex" || got.role != "kritika_app" {
		t.Fatalf("call = %q for role %q, want partitionIndex for kritika_app", got.name, got.role)
	}
	// The legacy partition is drained until a call moves and deletes
	// nothing, and then dropped.
	for range 3 {
		if got := next(); got.name != "legacyIndexChunks" {
			t.Fatalf("call = %q, want legacyIndexChunks", got.name)
		}
	}
	if got := next(); got.name != "dropLegacyIndexChunks" {
		t.Fatalf("call = %q, want dropLegacyIndexChunks", got.name)
	}
	// A second pass proves the loop actually re-runs after the interval.
	if got := next(); got.name != "modelCalls" {
		t.Fatalf("second pass's first call = %q, want modelCalls", got.name)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Retention did not return once ctx ended")
	}
}

// TestRetentionSweepHoldsIndexWorkUntilReady: until IndexReady closes, every
// pass sweeps all but the stopped indexes and the legacy partition, which
// take the index schema's lock. Once it closes, they run: at once, not at the
// next tick.
func TestRetentionSweepHoldsIndexWorkUntilReady(t *testing.T) {
	ordinary := []string{"modelCalls", "diffs", "sessions", "conversations", "egressRefusals", "modelInvocations", "similarExclusions"}
	for _, tc := range []struct {
		name string
		// every is the sweep interval, and passes how many passes run before
		// IndexReady closes: an hour's interval leaves only the close to
		// start another.
		every  time.Duration
		passes int
	}{
		{name: "across passes", every: 5 * time.Millisecond, passes: 2},
		{name: "at once", every: time.Hour, passes: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			current := configfile.NewCurrent(retentionAccount(t))
			st := &fakeRetentionStore{calls: make(chan sweepCall, 16)}
			ready := make(chan struct{})
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan struct{})
			go func() {
				(&Retention{Store: st, Current: current, Logger: slog.New(slog.DiscardHandler), IndexReady: ready, every: tc.every}).Run(ctx)
				close(done)
			}()
			defer func() {
				cancel()
				for {
					select {
					case <-done:
						return
					case <-st.calls:
					case <-time.After(5 * time.Second):
						t.Fatal("Retention did not return once ctx ended")
					}
				}
			}()
			next := func() string {
				select {
				case c := <-st.calls:
					return c.name
				case <-time.After(5 * time.Second):
					t.Fatal("Retention never called the store")
					return ""
				}
			}
			for pass := range tc.passes {
				for _, want := range ordinary {
					if got := next(); got != want {
						t.Fatalf("pass %d called %q, want %q before IndexReady closes", pass+1, got, want)
					}
				}
			}
			close(ready)
			for got := next(); got != "stoppedIndexes"; got = next() {
				if got == "partitionIndex" {
					t.Fatal("the legacy partition came before the stopped indexes")
				}
			}
			if got := next(); got != "partitionIndex" {
				t.Fatalf("call after stoppedIndexes = %q, want partitionIndex", got)
			}
		})
	}
}

// lockedBuffer is a log destination that a test reads while a sweep writes.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestRetentionSweepLogsLegacyDrain: the chunks that the drain moved to the
// hash partitions are logged apart from those it deleted.
func TestRetentionSweepLogsLegacyDrain(t *testing.T) {
	current := configfile.NewCurrent(retentionAccount(t))
	st := &fakeRetentionStore{calls: make(chan sweepCall, 64), drained: [][2]int64{{3, 1}, {2, 0}}}
	var logs lockedBuffer
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		(&Retention{Store: st, Current: current, Logger: slog.New(slog.NewTextHandler(&logs, nil)), every: time.Hour}).Run(ctx)
		close(done)
	}()
	for {
		select {
		case c := <-st.calls:
			if c.name != "dropLegacyIndexChunks" {
				continue
			}
		case <-time.After(5 * time.Second):
			t.Fatal("Retention never dropped the legacy partition")
		}
		break
	}
	// The pass logs the drain after the drop's call and before Run returns.
	cancel()
	<-done
	text := logs.String()
	for _, want := range []string{
		`msg="retention sweep moved" sweep="legacy index partition" moved=5 unit=chunks`,
		`msg="retention sweep removed" sweep="legacy index partition" removed=1 unit=chunks`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("logs lack %s:\n%s", want, text)
		}
	}
}

func TestRetentionSweepLogsErrorsWithoutStopping(t *testing.T) {
	current := configfile.NewCurrent(retentionAccount(t))
	st := &fakeRetentionStore{calls: make(chan sweepCall, 16), fail: true}
	logs := &warnCounter{}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		(&Retention{Store: st, Current: current, Logger: slog.New(logs), every: 5 * time.Millisecond}).Run(ctx)
		close(done)
	}()

	// Every sweep fails on every pass. Wait for two full passes (18 calls) to
	// prove that a failure does not stop the loop. Wait for one call more,
	// which the eighteenth call's warning is logged before.
	for range 19 {
		select {
		case <-st.calls:
		case <-time.After(5 * time.Second):
			t.Fatal("Retention stopped calling the store after an error")
		}
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Retention did not return once ctx ended")
	}
	if n := logs.n.Load(); n < 18 {
		t.Fatalf("logged %d warnings for two failed passes, want >= 18", n)
	}
}

func (s *fakeRetentionStore) PartitionIndexChunks(_ context.Context, appRole string) (bool, error) {
	s.calls <- sweepCall{name: "partitionIndex", role: appRole}
	if s.fail {
		return false, errors.New("db down")
	}
	return false, nil
}

func (s *fakeRetentionStore) DrainLegacyIndexChunks(context.Context) (int64, int64, error) {
	s.calls <- sweepCall{name: "legacyIndexChunks"}
	if s.fail {
		return 0, 0, errors.New("db down")
	}
	if len(s.drained) == 0 {
		return 0, 0, nil
	}
	n := s.drained[0]
	s.drained = s.drained[1:]
	return n[0], n[1], nil
}

func (s *fakeRetentionStore) DropLegacyIndexChunks(context.Context) (bool, error) {
	s.calls <- sweepCall{name: "dropLegacyIndexChunks"}
	if s.fail {
		return false, errors.New("db down")
	}
	return true, nil
}
