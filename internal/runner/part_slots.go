package runner

import (
	"context"
	"log/slog"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/home-operations/kritika/internal/runspec"
)

func reportPartCount(ctx context.Context, report *reporter, p runspec.Spec, count int, logger *slog.Logger) {
	if p.Agent != nil && p.Agent.Parts > 1 {
		runnerPartSlots(report, p, logger)(ctx, max(1, count))
	}
}

func runnerPartSlots(report *reporter, p runspec.Spec, logger *slog.Logger) func(context.Context, int) int {
	granted := max(1, p.Agent.Parallel)
	return func(ctx context.Context, remaining int) int {
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
			return granted
		}
		granted = slots
		return granted
	}
}

// scheduleParts admits more work when capacity grows, without interrupting
// running parts. Only this goroutine reports remaining work, monotonically.
func scheduleParts(
	ctx context.Context, pending []int, parallel int, ticks <-chan time.Time,
	report func(context.Context, int) int, run func(context.Context, int) error,
) error {
	g, gctx := errgroup.WithContext(ctx)
	done := make(chan error, len(pending))
	next, active := 0, 0
	for {
		if gctx.Err() != nil {
			return g.Wait()
		}
		remaining := len(pending) - next + active
		if report != nil {
			parallel = max(1, report(gctx, remaining))
		}
		if remaining == 0 {
			return g.Wait()
		}
		for next < len(pending) && active < parallel && gctx.Err() == nil {
			i := pending[next]
			next++
			active++
			g.Go(func() error {
				err := run(gctx, i)
				done <- err
				return err
			})
		}
		select {
		case <-gctx.Done():
			return g.Wait()
		case err := <-done:
			if err != nil {
				return g.Wait()
			}
			active--
		case <-ticks:
		}
	}
}
