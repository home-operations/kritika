package worker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/configfile/configfiletest"
	"github.com/home-operations/kritika/internal/tuning"
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
	// holdDrain makes DrainLegacyIndexChunks wait for its context to end,
	// as a drain of more chunks than a sweep interval moves does.
	holdDrain bool
}

// call sends c, unless ctx ends first, which no test then waits for.
func (s *fakeRetentionStore) call(ctx context.Context, c sweepCall) {
	select {
	case s.calls <- c:
	case <-ctx.Done():
	}
}

func (s *fakeRetentionStore) SweepModelCalls(ctx context.Context, olderThan time.Duration) (int64, error) {
	s.call(ctx, sweepCall{name: "modelCalls", swept: olderThan})
	if s.fail {
		return 0, errors.New("db down")
	}
	return 1, nil
}

func (s *fakeRetentionStore) SweepDiffs(ctx context.Context, olderThan time.Duration) (int64, error) {
	s.call(ctx, sweepCall{name: "diffs", swept: olderThan})
	if s.fail {
		return 0, errors.New("db down")
	}
	return 1, nil
}

func (s *fakeRetentionStore) SweepSessions(ctx context.Context, now time.Time) (int64, error) {
	s.call(ctx, sweepCall{name: "sessions", sweptAt: now})
	if s.fail {
		return 0, errors.New("db down")
	}
	return 1, nil
}

func (s *fakeRetentionStore) SweepStoppedIndexes(
	ctx context.Context, grace time.Duration, runs func(accountID string, t configfile.RepoTraits) bool,
) (int64, error) {
	s.call(ctx, sweepCall{name: "stoppedIndexes", swept: grace, runs: runs})
	if s.fail {
		return 0, errors.New("db down")
	}
	return 1, nil
}

func (s *fakeRetentionStore) SweepConversations(ctx context.Context, olderThan time.Duration) (int64, error) {
	s.call(ctx, sweepCall{name: "conversations", swept: olderThan})
	if s.fail {
		return 0, errors.New("db down")
	}
	return 1, nil
}

func (s *fakeRetentionStore) SweepEgressRefusals(ctx context.Context, olderThan time.Duration) (int64, error) {
	s.call(ctx, sweepCall{name: "egressRefusals", swept: olderThan})
	if s.fail {
		return 0, errors.New("db down")
	}
	return 1, nil
}

func (s *fakeRetentionStore) SweepModelInvocations(ctx context.Context, olderThan time.Duration) (int64, error) {
	s.call(ctx, sweepCall{name: "modelInvocations", swept: olderThan})
	if s.fail {
		return 0, errors.New("db down")
	}
	return 1, nil
}

func (s *fakeRetentionStore) SweepSimilarExclusions(ctx context.Context) (int64, error) {
	s.call(ctx, sweepCall{name: "similarExclusions"})
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
	// The sweeps and the legacy partition's work are loops of their own, whose
	// calls interleave. Each call is checked against the next of its loop's,
	// and a loop's calls past those are not checked.
	type expected struct {
		name string
		// check says what is wrong with the call's arguments, "" for nothing.
		check func(sweepCall) string
	}
	older := func(want time.Duration) func(sweepCall) string {
		return func(c sweepCall) string {
			if c.swept != want {
				return fmt.Sprintf("older than %s, want %s", c.swept, want)
			}
			return ""
		}
	}
	acme := configfile.AccountID(configfile.ForgeGitHub, "acme")
	sweeps := []expected{
		// The first pass runs immediately, without waiting for a tick.
		{"modelCalls", older(current.Get().TranscriptRetention())},
		{"diffs", older(current.Get().DiffRetention())},
		{"sessions", nil},
		{"conversations", older(tuning.Get().Worker.ConversationRetention)},
		{"egressRefusals", older(current.Get().TranscriptRetention())},
		{"modelInvocations", older(current.Get().TranscriptRetention())},
		{"similarExclusions", nil},
		// No IndexReady: the index work follows at once.
		{"stoppedIndexes", func(c sweepCall) string {
			if want := current.Get().DisabledIndexGrace(); c.swept != want {
				return fmt.Sprintf("grace %s, want %s", c.swept, want)
			}
			if !c.runs(acme, configfile.RepoTraits{}) || c.runs(acme, configfile.RepoTraits{Archived: true}) ||
				c.runs(configfile.AccountID(configfile.ForgeGitHub, "gone"), configfile.RepoTraits{}) {
				return "a repository runs other than as the configuration says, or one of an account it does not serve does"
			}
			return ""
		}},
		// A second pass proves the loop actually re-runs after the interval.
		{"modelCalls", nil},
	}
	index := []expected{
		{"partitionIndex", func(c sweepCall) string {
			if c.role != "kritika_app" {
				return fmt.Sprintf("for role %q, want kritika_app", c.role)
			}
			return ""
		}},
		// The legacy partition is drained until a call moves and deletes
		// nothing, and then dropped.
		{"legacyIndexChunks", nil},
		{"legacyIndexChunks", nil},
		{"legacyIndexChunks", nil},
		{"dropLegacyIndexChunks", nil},
		// Its loop runs again after the interval too.
		{"partitionIndex", nil},
	}
	indexCalls := map[string]bool{"partitionIndex": true, "legacyIndexChunks": true, "dropLegacyIndexChunks": true}
	for len(sweeps)+len(index) > 0 {
		c := next()
		loop := &sweeps
		if indexCalls[c.name] {
			loop = &index
		}
		if len(*loop) == 0 {
			continue
		}
		want := (*loop)[0]
		*loop = (*loop)[1:]
		if c.name != want.name {
			t.Fatalf("call = %q, want %q", c.name, want.name)
		}
		if want.check != nil {
			if wrong := want.check(c); wrong != "" {
				t.Fatalf("%s: %s", c.name, wrong)
			}
		}
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
			// The two run in loops of their own, in either order.
			pending := map[string]bool{"stoppedIndexes": true, "partitionIndex": true}
			for len(pending) > 0 {
				delete(pending, next())
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

	// Every sweep fails on every pass. Wait for two full passes of each loop,
	// the sweeps' eight calls and the legacy partition's one, to prove that a
	// failure stops neither. Wait for each loop's third pass to begin, which
	// its second pass's warnings are logged before.
	for starts := map[string]int{}; starts["modelCalls"] < 3 || starts["partitionIndex"] < 3; {
		select {
		case c := <-st.calls:
			starts[c.name]++
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

func (s *fakeRetentionStore) PartitionIndexChunks(ctx context.Context, appRole string) (bool, error) {
	s.call(ctx, sweepCall{name: "partitionIndex", role: appRole})
	if s.fail {
		return false, errors.New("db down")
	}
	return false, nil
}

func (s *fakeRetentionStore) DrainLegacyIndexChunks(ctx context.Context) (int64, int64, error) {
	s.call(ctx, sweepCall{name: "legacyIndexChunks"})
	if s.holdDrain {
		<-ctx.Done()
		return 0, 0, ctx.Err()
	}
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

func (s *fakeRetentionStore) DropLegacyIndexChunks(ctx context.Context) (bool, error) {
	s.call(ctx, sweepCall{name: "dropLegacyIndexChunks"})
	if s.fail {
		return false, errors.New("db down")
	}
	return true, nil
}

// TestRetentionSweepBesideHeldDrain: a drain of the legacy index partition
// that outlasts the sweep interval holds up none of the other sweeps, whose
// next pass runs while it is under way. Run returns once ctx ends, the
// drain's loop included.
func TestRetentionSweepBesideHeldDrain(t *testing.T) {
	current := configfile.NewCurrent(retentionAccount(t))
	st := &fakeRetentionStore{calls: make(chan sweepCall, 16), holdDrain: true}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		(&Retention{Store: st, Current: current, Logger: slog.New(slog.DiscardHandler), every: 5 * time.Millisecond}).Run(ctx)
		close(done)
	}()
	next := func() string {
		select {
		case c := <-st.calls:
			return c.name
		case <-time.After(5 * time.Second):
			t.Fatal("Retention stopped calling the store")
			return ""
		}
	}
	for next() != "legacyIndexChunks" {
	}
	// The first pass can begin after the drain does: the second cannot.
	for passes := 0; passes < 2; {
		if next() == "modelCalls" {
			passes++
		}
	}
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
}
