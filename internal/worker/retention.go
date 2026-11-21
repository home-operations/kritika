package worker

import (
	"cmp"
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/home-operations/kritika/internal/configfile"
)

// retentionSweepInterval is how often the leader deletes these items past
// their retention window: model-call transcripts, egress refusals, settled
// model invocations, review diffs, agent conversations, stopped repositories'
// indexes, finished runs' similar-code exclusions and dashboard sessions.
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
	SweepSimilarExclusions(ctx context.Context) (int64, error)
	PartitionIndexChunks(ctx context.Context, appRole string) (bool, error)
	DrainLegacyIndexChunks(ctx context.Context) (moved, removed int64, err error)
	DropLegacyIndexChunks(ctx context.Context) (bool, error)
}

// Retention deletes what is kept only so long. The owner pool bypasses
// row-level security. With that pool, Retention deletes model-call
// transcripts, egress refusals, and settled model invocations older than the
// current configuration's transcript retention window. It also deletes review
// diffs past their own retention window, and the paths that finished runs'
// similar-code searches left out. It also deletes the indexes of
// repositories that the configuration did not run for longer than its index
// grace, and agent conversations older than conversationRetention. It also
// partitions an index table that a build before partitioning created. It
// moves the chunks that its legacy partition holds to the hash partitions,
// or deletes those that no build needs, and drops the emptied partition.
// With the app pool, it deletes expired dashboard sessions. A leader duty.
type Retention struct {
	Store   retentionStore
	Current *configfile.Current
	Logger  *slog.Logger
	// AppRole is the application role, which a partitioned index table grants.
	AppRole string
	// IndexReady, unless nil, holds back the work that takes the index
	// schema's lock until it is closed: dropping stopped repositories'
	// indexes and partitioning a legacy index table. The leader closes it
	// once the configuration is applied or refused and the index schema is
	// ensured, which can replace the table. The other sweeps run meanwhile,
	// so a schema change that waits for a lock holds none of them up.
	IndexReady <-chan struct{}
	// every overrides retentionSweepInterval in tests.
	every time.Duration
}

// Run sweeps once immediately, then every retentionSweepInterval until ctx
// ends, and once more when IndexReady closes. A sweep failure is logged,
// never fatal: it just leaves stale rows for the next tick.
//
// The legacy index partition's conversion and drain run in a loop of their
// own (migrateIndex), which Run waits for before it returns. A drain goes on
// until the legacy partition is empty or held, which can take longer than
// the interval: the other sweeps keep to it meanwhile.
func (r *Retention) Run(ctx context.Context) {
	every := cmp.Or(r.every, retentionSweepInterval)
	var wg sync.WaitGroup
	defer wg.Wait()
	wg.Go(func() { r.migrateIndex(ctx, every) })
	t := time.NewTicker(every)
	defer t.Stop()
	// A nil channel never receives: ready is nil once the index work runs.
	ready := r.IndexReady
	for {
		n, err := r.Store.SweepModelCalls(ctx, r.Current.Get().TranscriptRetention())
		r.report(ctx, "model call transcripts", "rows", n, err)
		n, err = r.Store.SweepDiffs(ctx, r.Current.Get().DiffRetention())
		r.report(ctx, "review diffs", "packs", n, err)
		n, err = r.Store.SweepSessions(ctx, time.Now())
		r.report(ctx, "dashboard sessions", "rows", n, err)
		n, err = r.Store.SweepConversations(ctx, conversationRetention)
		r.report(ctx, "agent conversations", "rows", n, err)
		n, err = r.Store.SweepEgressRefusals(ctx, r.Current.Get().TranscriptRetention())
		r.report(ctx, "egress refusals", "rows", n, err)
		n, err = r.Store.SweepModelInvocations(ctx, r.Current.Get().TranscriptRetention())
		r.report(ctx, "settled model invocations", "rows", n, err)
		n, err = r.Store.SweepSimilarExclusions(ctx)
		r.report(ctx, "finished runs' similar-code exclusions", "rows", n, err)
		select {
		case <-ready:
			ready = nil
		default:
		}
		if ready == nil {
			f := r.Current.Get()
			n, err = r.Store.SweepStoppedIndexes(ctx, f.DisabledIndexGrace(), func(accountID string, traits configfile.RepoTraits) bool {
				a, ok := f.AccountByID(accountID)
				return ok && f.Runs(a, traits)
			})
			r.report(ctx, "stopped repositories' indexes", "repositories", n, err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-ready:
		}
	}
}

// migrateIndex runs partitionIndex once IndexReady closes, and then every
// interval until ctx ends.
func (r *Retention) migrateIndex(ctx context.Context, every time.Duration) {
	if r.IndexReady != nil {
		select {
		case <-ctx.Done():
			return
		case <-r.IndexReady:
		}
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		moved, n, err := r.partitionIndex(ctx)
		r.report(ctx, "legacy index partition", "chunks", n, err)
		if moved > 0 {
			r.Logger.Info("retention sweep moved", "sweep", "legacy index partition", "moved", moved, "unit", "chunks")
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// report logs a sweep's failure, unless ctx ended, or what it removed.
func (r *Retention) report(ctx context.Context, what, unit string, n int64, err error) {
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

// partitionIndex partitions an index table that a build before partitioning
// created. It then drains the table's legacy partition a slice at a time,
// until it is empty or another holds the lock of each repository or chunk
// left, and drops it once it is empty. It returns how many chunks it moved
// to the hash partitions and how many it deleted.
func (r *Retention) partitionIndex(ctx context.Context) (moved, removed int64, err error) {
	if _, err := r.Store.PartitionIndexChunks(ctx, r.AppRole); err != nil {
		return 0, 0, err
	}
	for {
		m, d, err := r.Store.DrainLegacyIndexChunks(ctx)
		moved, removed = moved+m, removed+d
		if err != nil {
			return moved, removed, err
		}
		if m+d == 0 {
			break
		}
	}
	_, err = r.Store.DropLegacyIndexChunks(ctx)
	return moved, removed, err
}
