package worker

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/store"
)

const partSlotInterval = 2 * time.Second

// reviewSlots owns only the extra leases; admission retains the first slot.
// Its watch is stopped before close or any other caller accesses the leases.
type reviewSlots struct {
	worker                  *Review
	logger                  *slog.Logger
	accountID, runID, model string
	jobID                   int64
	limit, maxParts         int
	leases                  []*store.Lease
	published               int
}

func (s *reviewSlots) resize(ctx context.Context, remaining int) error {
	want := min(max(1, remaining), s.maxParts, s.limit)
	for len(s.leases)+1 < want {
		lease, err := s.worker.Store.TakeExtraLease(ctx, s.accountID, s.model, s.limit, s.jobID)
		if err != nil {
			s.logger.Warn("extra model slot not taken", "error", err)
			break
		}
		if lease == nil {
			break
		}
		s.leases = append(s.leases, lease)
	}
	granted := min(want, len(s.leases)+1)
	if granted != s.published {
		if err := s.worker.Store.WithAccount(ctx, s.accountID, func(tx pgx.Tx) error {
			return store.SetPartSlots(ctx, tx, s.runID, granted)
		}); err != nil {
			return err
		}
		s.published = granted
		s.logger.Info("review model slots adjusted", "slots", granted, "remaining_parts", remaining, "limit", s.limit)
	}
	for len(s.leases)+1 > granted {
		last := len(s.leases) - 1
		s.worker.releaseLease(ctx, s.logger, s.leases[last], s.model)
		s.leases = s.leases[:last]
	}
	return nil
}

func (s *reviewSlots) watch(ctx context.Context) func() {
	if s.maxParts < 2 {
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
			remaining := s.maxParts
			err := s.worker.Store.WithAccount(ctx, s.accountID, func(tx pgx.Tx) error {
				pending, err := store.RemainingParts(ctx, tx, s.runID)
				if pending != nil {
					remaining = *pending
				}
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
	for _, lease := range s.leases {
		s.worker.releaseLease(ctx, s.logger, lease, s.model)
	}
	s.leases = nil
}
