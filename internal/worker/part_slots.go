package worker

import (
	"context"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/review"
	"github.com/home-operations/kritika/internal/store"
)

const partSlotInterval = 2 * time.Second

// reviewSlots owns a split review's extra leases: those beyond the one slot
// that admission took on the review model and on each lens's model, which
// admission retains. Its watch is stopped before close or any other caller
// accesses the leases. It publishes its grant by model, so the runner runs
// the parts of the reviewers on each model in that model's slots.
type reviewSlots struct {
	worker           *Review
	logger           *slog.Logger
	accountID, runID string
	jobID            int64
	// pools are the models the review's reviewers run on, sorted by model.
	pools []*slotPool
	// estimate is how many parts each reviewer is expected to have, which
	// sizes the grant until the runner reports its actual work.
	estimate  map[string]int
	published map[string]int
}

// slotPool is one model's slots: its limit, the reviewers on it, sorted, and
// the extra leases held beyond admission's one.
type slotPool struct {
	model     string
	limit     int
	reviewers []string
	leases    []*store.Lease
}

// newReviewSlots pools the models of chosen's reviewers, the default pass's
// alone when it runs alone. Each reviewer is expected to have perReviewer
// parts.
func newReviewSlots(
	w *Review, logger *slog.Logger, file *configfile.File, account *configfile.Account, settings configfile.Settings,
	chosen reviewers, perReviewer int, runID string, jobID int64,
) *reviewSlots {
	s := &reviewSlots{worker: w, logger: logger, accountID: account.ID(), runID: runID, jobID: jobID, estimate: map[string]int{}}
	byModel := map[string]*slotPool{}
	for _, name := range chosen.names() {
		model := settings.Models.Review
		if r, ok := settings.Reviewers[name]; ok && name != review.DefaultReviewer {
			model = r.Models(settings.Models).Review
		}
		pool, ok := byModel[string(model)]
		if !ok {
			pool = &slotPool{model: string(model), limit: file.Slots(account, model)}
			byModel[string(model)] = pool
			s.pools = append(s.pools, pool)
		}
		pool.reviewers = append(pool.reviewers, name)
		s.estimate[name] = perReviewer
	}
	slices.SortFunc(s.pools, func(a, b *slotPool) int { return strings.Compare(a.model, b.model) })
	for _, pool := range s.pools {
		slices.Sort(pool.reviewers)
	}
	return s
}

// poolOf is each reviewer's pool, by name: its model (runspec.AgentLimits.Pools).
func (s *reviewSlots) poolOf() map[string]string {
	out := map[string]string{}
	for _, pool := range s.pools {
		for _, name := range pool.reviewers {
			out[name] = pool.model
		}
	}
	return out
}

// parts is how many parts the review is expected to have in each pool, from
// its reviewers' estimates.
func (s *reviewSlots) parts() map[string]int {
	out := map[string]int{}
	for _, pool := range s.pools {
		for _, name := range pool.reviewers {
			out[pool.model] += s.estimate[name]
		}
	}
	return out
}

// resize holds, on each model, as many slots as its remaining parts can use,
// within the model's limit and what is free, and publishes the grant by
// model. remaining is by model; nil, before the runner reports, is the
// estimate.
func (s *reviewSlots) resize(ctx context.Context, remaining map[string]int) error {
	if remaining == nil {
		remaining = s.parts()
	}
	grant := map[string]int{}
	for _, pool := range s.pools {
		want := min(max(1, remaining[pool.model]), pool.limit)
		for len(pool.leases)+1 < want {
			lease, err := s.worker.Store.TakeExtraLease(ctx, s.accountID, pool.model, pool.limit, s.jobID)
			if err != nil {
				s.logger.Warn("extra model slot not taken", "model", pool.model, "error", err)
				break
			}
			if lease == nil {
				break
			}
			pool.leases = append(pool.leases, lease)
		}
		grant[pool.model] = min(want, len(pool.leases)+1)
		for len(pool.leases)+1 > grant[pool.model] {
			last := len(pool.leases) - 1
			s.worker.releaseLease(ctx, s.logger, pool.leases[last], pool.model)
			pool.leases = pool.leases[:last]
		}
	}
	if maps.Equal(grant, s.published) {
		return nil
	}
	if err := s.worker.Store.WithAccount(ctx, s.accountID, func(tx pgx.Tx) error {
		return store.SetPartSlots(ctx, tx, s.runID, grant)
	}); err != nil {
		return err
	}
	s.published = grant
	s.logger.Info("review model slots adjusted", "slots", grant, "remaining_parts", remaining)
	return nil
}

func (s *reviewSlots) watch(ctx context.Context) func() {
	total := 0
	for _, n := range s.estimate {
		total += n
	}
	if total < 2 {
		return func() {}
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(partSlotInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			var remaining map[string]int
			err := s.worker.Store.WithAccount(ctx, s.accountID, func(tx pgx.Tx) error {
				var err error
				remaining, err = store.RemainingParts(ctx, tx, s.runID)
				return err
			})
			if err == nil {
				err = s.resize(ctx, remaining)
			}
			if err != nil && ctx.Err() == nil {
				s.logger.Warn("review model slots not adjusted", "error", err)
			}
		}
	}()
	return func() { cancel(); <-done }
}

func (s *reviewSlots) close(ctx context.Context) {
	for _, pool := range s.pools {
		for _, lease := range pool.leases {
			s.worker.releaseLease(ctx, s.logger, lease, pool.model)
		}
		pool.leases = nil
	}
}
