//go:build integration

package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"

	"github.com/home-operations/kritika/internal/jobs"
	"github.com/home-operations/kritika/internal/store/storetest"
)

func TestThreadQueueRollback(t *testing.T) {
	st := storetest.Open(t)
	client, err := river.NewClient(riverpgxv5.New(st.App()), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range rivertype.JobStates() {
		t.Run(string(state), func(t *testing.T) {
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
			args := jobs.ThreadArgs{CommentID: 91801, Resolved: true, Sender: "maintainer"}
			inserted, err := client.InsertTx(ctx, tx, args, nil)
			if err != nil {
				t.Fatal(err)
			}
			terminal := state == rivertype.JobStateCancelled || state == rivertype.JobStateCompleted || state == rivertype.JobStateDiscarded
			if _, err := tx.Exec(ctx, `UPDATE river_job SET state = $2::text::river_job_state,
				finalized_at = CASE WHEN $3 THEN now() END WHERE id = $1`, inserted.Job.ID, state, terminal); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if _, err := tx.Exec(ctx, `UPDATE river_job
					SET queue = 'followup', state = CASE WHEN state = 'running' THEN 'available'::river_job_state ELSE state END
					WHERE kind = 'thread' AND queue = 'thread'
					AND state IN ('available', 'pending', 'retryable', 'running', 'scheduled')`); err != nil {
					t.Fatal(err)
				}
			}
			got, err := client.JobGetTx(ctx, tx, inserted.Job.ID)
			if err != nil {
				t.Fatal(err)
			}
			wantQueue, wantState := jobs.QueueFollowUp, state
			if terminal {
				wantQueue = jobs.QueueThread
			}
			if state == rivertype.JobStateRunning {
				wantState = rivertype.JobStateAvailable
			}
			if got.Queue != wantQueue || got.State != wantState || !bytes.Equal(got.EncodedArgs, inserted.Job.EncodedArgs) ||
				!bytes.Equal(got.UniqueKey, inserted.Job.UniqueKey) || !got.ScheduledAt.Equal(inserted.Job.ScheduledAt) {
				t.Fatalf("rollback job = %+v, want queue %s, state %s and unchanged arguments, uniqueness and schedule", got, wantQueue, wantState)
			}
			if !terminal {
				duplicate, err := client.InsertTx(ctx, tx, args, &river.InsertOpts{Queue: jobs.QueueFollowUp})
				if err != nil {
					t.Fatal(err)
				}
				if !duplicate.UniqueSkippedAsDuplicate || duplicate.Job.ID != got.ID {
					t.Fatalf("rollback lost uniqueness: %+v", duplicate)
				}
			}
		})
	}
}
