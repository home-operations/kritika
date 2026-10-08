package worker

import (
	"context"
	"errors"
	"log/slog"
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

// sweepCall is one call fakeRetentionStore received, carrying the argument
// it was given. Sending the value on the channel (rather than stashing it in
// a field the test reads separately) means the data only ever crosses
// goroutines through the channel op itself, so there's no shared state for
// a later, unsynchronized pass to race against.
type sweepCall struct {
	name    string
	swept   time.Duration
	sweptAt time.Time
	runs    func(accountID, fullName string, t configfile.RepoTraits) bool
}

// fakeRetentionStore signals every call on a channel, so a test can wait for
// a specific pass instead of sleeping.
type fakeRetentionStore struct {
	calls chan sweepCall
	fail  bool
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
	_ context.Context, grace time.Duration, runs func(accountID, fullName string, t configfile.RepoTraits) bool,
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
	st := &fakeRetentionStore{calls: make(chan sweepCall, 16)}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		(&Retention{Store: st, Current: current, Logger: slog.New(slog.DiscardHandler), every: 5 * time.Millisecond}).Run(ctx)
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
	fourth := next()
	if fourth.name != "stoppedIndexes" {
		t.Fatalf("fourth call = %q, want stoppedIndexes", fourth.name)
	}
	if want := current.Get().DisabledIndexGrace(); fourth.swept != want {
		t.Fatalf("grace = %s, want %s", fourth.swept, want)
	}
	acme := configfile.AccountID(configfile.ForgeGitHub, "acme")
	if !fourth.runs(acme, "acme/app", configfile.RepoTraits{}) || fourth.runs(acme, "acme/app", configfile.RepoTraits{Archived: true}) ||
		fourth.runs(configfile.AccountID(configfile.ForgeGitHub, "gone"), "gone/app", configfile.RepoTraits{}) {
		t.Fatal("a repository runs as the configuration says, and none of an account it does not serve does")
	}
	if fifth := next(); fifth.name != "conversations" || fifth.swept != conversationRetention {
		t.Fatalf("fifth call = %q older than %s, want conversations older than %s", fifth.name, fifth.swept, conversationRetention)
	}
	// A second pass proves the loop actually re-runs after the interval.
	if got := next(); got.name != "modelCalls" {
		t.Fatalf("sixth call = %q, want modelCalls", got.name)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Retention did not return once ctx ended")
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

	// Every sweep fails on every pass; wait for two full passes (10 calls)
	// to prove a failure doesn't stop the loop, and one call more, which
	// the tenth call's warning is logged before.
	for range 11 {
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
	if n := logs.n.Load(); n < 10 {
		t.Fatalf("logged %d warnings for two failed passes, want >= 10", n)
	}
}
