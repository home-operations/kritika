package runner

import (
	"cmp"
	"context"
	"log/slog"
	"maps"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/home-operations/kritika/internal/review"
	"github.com/home-operations/kritika/internal/runspec"
	"github.com/home-operations/kritika/internal/tuning"
)

// poolKey is the slot pool a part of reviewer runs in (runspec.AgentLimits):
// its reviewer's, the default pass, named or not, being review.DefaultReviewer.
// A reviewer the worker named no pool for has one of its own.
func poolKey(p runspec.Spec, reviewer string) string {
	key := cmp.Or(reviewer, review.DefaultReviewer)
	if p.Agent != nil {
		if pool, ok := p.Agent.Pools[key]; ok {
			return pool
		}
	}
	return key
}

// reportPartCount tells the worker how many parts the run has in each slot
// pool, once it knows, so the worker sizes its slots to them. A review that
// is not split has one part.
func reportPartCount(ctx context.Context, report *reporter, p runspec.Spec, shares []partShare, logger *slog.Logger) {
	if p.Agent == nil || p.Agent.Parts <= 1 {
		return
	}
	remaining := map[string]int{}
	for _, sh := range shares {
		remaining[poolKey(p, sh.name())]++
	}
	if len(remaining) == 0 {
		remaining[poolKey(p, "")] = 1
	}
	runnerPartSlots(report, logger)(ctx, remaining, map[string]int{})
}

// granted is how many parts run at once in the pool key: what the worker
// granted, or one for a pool it granted nothing to by name.
func granted(slots map[string]int, key string) int {
	if n, ok := slots[key]; ok {
		return n
	}
	return 1
}

// totalSlots is how many parts run at once across the pools.
func totalSlots(slots map[string]int) int {
	total := 0
	for _, n := range slots {
		total += n
	}
	return max(1, total)
}

// partReport reports the run's unfinished parts and those of them that run,
// both by slot pool, and answers with the slots that the worker grants.
type partReport func(ctx context.Context, remaining, active map[string]int) map[string]int

func runnerPartSlots(report *reporter, logger *slog.Logger) partReport {
	return func(ctx context.Context, remaining, active map[string]int) map[string]int {
		queryCtx, cancel := context.WithTimeout(ctx, tuning.Get().Runner.PartSlotsTimeout)
		defer cancel()
		slots, err := report.partsRemaining(queryCtx, remaining, active)
		if err != nil {
			if ctx.Err() == nil {
				logger.Warn("review part capacity not refreshed", "error", err)
			}
			// The gateway may have taken the report though its answer was
			// lost, and the worker then keeps only the slots of that answer.
			// The one slot of each pool that the worker never lets go is all
			// that is safe to start a part in.
			return nil
		}
		return slots
	}
}

// scheduleParts admits more work when capacity grows, and no more than the
// capacity left when it shrinks, without interrupting running parts. Each
// part runs in the slots of its pool, key, as slots grants them. Only this
// goroutine reports remaining work, monotonically, and the parts that run.
func scheduleParts(
	ctx context.Context, pending []int, key func(int) string, slots map[string]int, ticks <-chan time.Time,
	report partReport, run func(context.Context, int) error,
) error {
	type ended struct {
		i   int
		err error
	}
	g, gctx := errgroup.WithContext(ctx)
	done := make(chan ended, len(pending))
	started := make(map[int]bool, len(pending))
	active := map[string]int{}
	for {
		if gctx.Err() != nil {
			return g.Wait()
		}
		remaining, left := map[string]int{}, 0
		for k, n := range active {
			remaining[k] += n
			left += n
		}
		for _, i := range pending {
			if !started[i] {
				remaining[key(i)]++
				left++
			}
		}
		if report != nil {
			slots = report(gctx, remaining, maps.Clone(active))
		}
		if left == 0 {
			return g.Wait()
		}
		for _, i := range pending {
			k := key(i)
			if started[i] || active[k] >= granted(slots, k) || gctx.Err() != nil {
				continue
			}
			started[i] = true
			active[k]++
			g.Go(func() error {
				err := run(gctx, i)
				done <- ended{i, err}
				return err
			})
		}
		select {
		case <-gctx.Done():
			return g.Wait()
		case d := <-done:
			if d.err != nil {
				return g.Wait()
			}
			active[key(d.i)]--
		case <-ticks:
		}
	}
}
