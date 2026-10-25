//go:build integration

package main

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/home-operations/kritika/internal/config"
	"github.com/home-operations/kritika/internal/jobs"
	"github.com/home-operations/kritika/internal/store/storetest"
)

func TestThreadQueueWhileFollowupsSaturated(t *testing.T) {
	st := storetest.Open(t)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	cfg := &config.Config{ReviewWorkers: 2, IndexWorkers: 1, ThreadWorkers: 1}
	suffix := "_" + uuid.NewString()
	queues := make(map[string]river.QueueConfig)
	for name, queue := range workerQueues(cfg) {
		queues[name+suffix] = queue
	}
	started := make(chan struct{}, cfg.ReviewWorkers)
	release := make(chan struct{})
	workers := river.NewWorkers()
	river.AddWorker(workers, river.WorkFunc(func(ctx context.Context, _ *river.Job[jobs.FollowUpArgs]) error {
		started <- struct{}{}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}))
	river.AddWorker(workers, river.WorkFunc(func(context.Context, *river.Job[jobs.ThreadArgs]) error { return nil }))
	client, err := river.NewClient(riverpgxv5.New(st.App()), &river.Config{
		Queues: queues, Workers: workers, Logger: slog.New(slog.DiscardHandler),
		FetchCooldown: 10 * time.Millisecond, FetchPollInterval: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]int64, 0, cfg.ReviewWorkers+2)
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		if _, err := st.App().Exec(cleanup, "DELETE FROM river_job WHERE id = ANY($1::bigint[])", ids); err != nil {
			t.Error(err)
		}
	})
	unrelated, err := client.Insert(ctx, jobs.ThreadArgs{CommentID: 91810, Resolved: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ids = append(ids, unrelated.Job.ID)
	for i := range cfg.ReviewWorkers {
		result, err := client.Insert(ctx, jobs.FollowUpArgs{CommentID: int64(91000 + i)}, &river.InsertOpts{Queue: (jobs.FollowUpArgs{}).InsertOpts().Queue + suffix})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, result.Job.ID)
	}
	events, unsubscribe := client.Subscribe(river.EventKindJobCompleted)
	defer unsubscribe()
	if err := client.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		if err := client.StopAndCancel(cleanup); err != nil {
			t.Error(err)
		}
	})
	for range cfg.ReviewWorkers {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("follow-up workers did not saturate")
		}
	}
	// Thread work completes while every follow-up worker is occupied.
	fresh, err := client.Insert(ctx, jobs.ThreadArgs{CommentID: 91011, Resolved: true}, &river.InsertOpts{Queue: (jobs.ThreadArgs{}).InsertOpts().Queue + suffix})
	if err != nil {
		t.Fatal(err)
	}
	ids = append(ids, fresh.Job.ID)
	select {
	case event := <-events:
		if event.Job.ID != fresh.Job.ID || event.Job.Queue != jobs.QueueThread+suffix {
			t.Fatalf("completed job = %+v, want new thread job %d", event.Job, fresh.Job.ID)
		}
	case <-ctx.Done():
		t.Fatal("thread job waited behind saturated follow-ups")
	}
	close(release)
	untouched, err := client.JobGet(ctx, unrelated.Job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if untouched.State != "available" || untouched.Attempt != 0 {
		t.Fatalf("unrelated job was worked: %+v", untouched)
	}
}

func TestThreadUniqueness(t *testing.T) {
	st := storetest.Open(t)
	client, err := river.NewClient(riverpgxv5.New(st.App()), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"available", "running"} {
		t.Run(state, func(t *testing.T) {
			ctx := t.Context()
			tx, err := st.App().Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := tx.Rollback(context.Background()); err != nil {
					t.Error(err)
				}
			}()
			args := jobs.ThreadArgs{CommentID: 91020, Resolved: true}
			first, err := client.InsertTx(ctx, tx, args, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(ctx, "UPDATE river_job SET state = $2::text::river_job_state WHERE id = $1", first.Job.ID, state); err != nil {
				t.Fatal(err)
			}
			duplicate, err := client.InsertTx(ctx, tx, args, nil)
			if err != nil {
				t.Fatal(err)
			}
			if !duplicate.UniqueSkippedAsDuplicate || duplicate.Job.ID != first.Job.ID {
				t.Fatalf("duplicate = %+v, want job %d", duplicate, first.Job.ID)
			}
			restored := args
			restored.Resolved = false
			different, err := client.InsertTx(ctx, tx, restored, nil)
			if err != nil {
				t.Fatal(err)
			}
			if different.UniqueSkippedAsDuplicate {
				t.Fatal("different thread state was deduplicated")
			}
			if _, err := tx.Exec(ctx, "UPDATE river_job SET state = 'completed', finalized_at = now() WHERE id = $1", first.Job.ID); err != nil {
				t.Fatal(err)
			}
			again, err := client.InsertTx(ctx, tx, args, nil)
			if err != nil {
				t.Fatal(err)
			}
			if again.UniqueSkippedAsDuplicate {
				t.Fatal("completed thread job prevented a later update")
			}
		})
	}
}
