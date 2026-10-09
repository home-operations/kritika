package worker

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/jobs"
	"github.com/home-operations/kritika/internal/store"
)

// SlotWake wakes a review River has just saved as snoozed for a model slot
// when one is free by then: a slot let go between the review finding every
// slot held and River saving its snooze found no waiter to wake (see
// store.SlotWaitKey), and the review would sleep out its snooze with the
// slot free. It reads the queue client's snoozed-job events, sent once the
// snooze is saved.
type SlotWake struct {
	Store   *store.Store
	Current *configfile.Current
	Logger  *slog.Logger
}

// Run checks each snoozed job until ctx ends or events closes. River drops
// the events a slow subscriber leaves in its channel, so each check runs
// on its own; a dropped one leaves its review to wake when its snooze
// ends.
func (s *SlotWake) Run(ctx context.Context, events <-chan *river.Event) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			go s.check(ctx, ev.Job)
		}
	}
}

// check wakes job when it is a review snoozed for a model slot of a
// running account and a slot of that model is free.
func (s *SlotWake) check(ctx context.Context, job *rivertype.JobRow) {
	if job.Kind != (jobs.ReviewArgs{}).Kind() {
		return
	}
	var meta struct {
		SlotWait string `json:"slot_wait"`
	}
	var args jobs.ReviewArgs
	if json.Unmarshal(job.Metadata, &meta) != nil || meta.SlotWait == "" || json.Unmarshal(job.EncodedArgs, &args) != nil {
		return
	}
	file := s.Current.Get()
	account, ok := file.AccountByID(args.AccountID)
	if !ok {
		return
	}
	// Limits are set per account, so the review counted these slots.
	slots := file.Settings(account, "").Limits.Concurrency
	ctx, cancel := context.WithTimeout(ctx, releaseTimeout)
	defer cancel()
	logger := s.Logger.With("job", job.ID, "model", meta.SlotWait)
	woken, err := s.Store.WakeIfSlotFree(ctx, args.AccountID, meta.SlotWait, slots, job.ID)
	if err != nil {
		logger.Warn("snoozed review not checked for a free slot; it wakes when its snooze ends", "error", err)
		return
	}
	if woken {
		logger.Info("snoozed review woken: a model slot was let go while it was being snoozed")
	}
}
