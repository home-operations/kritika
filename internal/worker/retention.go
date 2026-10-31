package worker

import (
	"cmp"
	"context"
	"log/slog"
	"time"

	"github.com/home-operations/kritika/internal/configfile"
)

// retentionSweepInterval is how often the leader deletes these items past
// their retention window: model-call transcripts, egress refusals, settled
// model invocations, review diffs, agent conversations, stopped repositories'
// indexes and dashboard sessions.
const retentionSweepInterval = time.Hour

// conversationRetention is how long an agent's conversation is kept for a
// re-review to carry on: well past any provider's cache window, after which
// carrying it on costs more than starting afresh.
const conversationRetention = 2 * time.Hour

// retentionStore is the subset of *store.Store that Retention needs,
// narrowed so it can be exercised in tests with a fake.
type retentionStore interface {
	SweepModelCalls(ctx context.Context, olderThan time.Duration) (int64, error)
	SweepDiffs(ctx context.Context, olderThan time.Duration) (int64, error)
	SweepSessions(ctx context.Context, now time.Time) (int64, error)
	SweepStoppedIndexes(
		ctx context.Context, grace time.Duration, runs func(accountID string, t configfile.RepoTraits) bool,
	) (int64, error)
	SweepConversations(ctx context.Context, olderThan time.Duration) (int64, error)
	SweepEgressRefusals(ctx context.Context, olderThan time.Duration) (int64, error)
	SweepModelInvocations(ctx context.Context, olderThan time.Duration) (int64, error)
}

// Retention deletes what is kept only so long. The owner pool bypasses
// row-level security. With that pool, Retention deletes model-call
// transcripts, egress refusals, and settled model invocations older than the
// current configuration's transcript retention window. It also deletes review
// diffs past their own retention window. It also deletes the indexes of
// repositories that the configuration did not run for longer than its index
// grace, and agent conversations older than conversationRetention. With the
// app pool, it deletes expired dashboard sessions. A leader duty.
type Retention struct {
	Store   retentionStore
	Current *configfile.Current
	Logger  *slog.Logger
	// every overrides retentionSweepInterval in tests.
	every time.Duration
}

// Run sweeps once immediately, then every retentionSweepInterval until ctx
// ends. A sweep failure is logged, never fatal: it just leaves stale rows
// for the next tick.
func (r *Retention) Run(ctx context.Context) {
	t := time.NewTicker(cmp.Or(r.every, retentionSweepInterval))
	defer t.Stop()
	report := func(what, unit string, n int64, err error) {
		if err != nil {
			if ctx.Err() == nil {
				r.Logger.Warn("retention sweep failed", "sweep", what, "error", err)
			}
			return
		}
		if n > 0 {
			r.Logger.Info("retention sweep removed", "sweep", what, "removed", n, "unit", unit)
		}
	}
	for {
		n, err := r.Store.SweepModelCalls(ctx, r.Current.Get().TranscriptRetention())
		report("model call transcripts", "rows", n, err)
		n, err = r.Store.SweepDiffs(ctx, r.Current.Get().DiffRetention())
		report("review diffs", "packs", n, err)
		n, err = r.Store.SweepSessions(ctx, time.Now())
		report("dashboard sessions", "rows", n, err)
		f := r.Current.Get()
		n, err = r.Store.SweepStoppedIndexes(ctx, f.DisabledIndexGrace(), func(accountID string, traits configfile.RepoTraits) bool {
			a, ok := f.AccountByID(accountID)
			return ok && f.Runs(a, traits)
		})
		report("stopped repositories' indexes", "repositories", n, err)
		n, err = r.Store.SweepConversations(ctx, conversationRetention)
		report("agent conversations", "rows", n, err)
		n, err = r.Store.SweepEgressRefusals(ctx, r.Current.Get().TranscriptRetention())
		report("egress refusals", "rows", n, err)
		n, err = r.Store.SweepModelInvocations(ctx, r.Current.Get().TranscriptRetention())
		report("settled model invocations", "rows", n, err)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
