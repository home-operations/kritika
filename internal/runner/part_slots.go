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
	runnerPartSlots(report, p, logger)(ctx, remaining)
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

func runnerPartSlots(report *reporter, p runspec.Spec, logger *slog.Logger) func(context.Context, map[string]int) map[string]int {
	held := maps.Clone(p.Agent.Slots)
	return func(ctx context.Context, remaining map[string]int) map[string]int {
		queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		slots, err := report.partsRemaining(queryCtx, remaining)
		if err != nil {
			if ctx.Err() == nil {
				logger.Warn("review part capacity not refreshed", "error", err)
			}
			// The worker only shrinks to reported unfinished work, which
			// includes active parts. At most that work can run, so the
			// previous grant remains safe when a refresh fails.
			return held
		}
		held = slots
		return held
	}
}

// scheduleParts admits more work when capacity grows, without interrupting
// running parts. Each part runs in the slots of its pool, key, as slots
// grants them. Only this goroutine reports remaining work, monotonically.
func scheduleParts(
	ctx context.Context, pending []int, key func(int) string, slots map[string]int, ticks <-chan time.Time,
	report func(context.Context, map[string]int) map[string]int, run func(context.Context, int) error,
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
			slots = report(gctx, remaining)
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
