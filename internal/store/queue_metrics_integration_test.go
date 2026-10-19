//go:build integration

package store

import (
	"context"
	"testing"
	"time"

	"github.com/riverqueue/river/rivertype"

	"github.com/home-operations/kritika/internal/jobs"
)

func TestQueueStats(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	before, err := s.QueueStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	baseline := make(map[string]QueueStats, len(before))
	for _, q := range before {
		baseline[q.Queue] = q
	}
	for _, tt := range []struct {
		state rivertype.JobState
		delay time.Duration
	}{
		{rivertype.JobStateAvailable, -5 * time.Minute},
		{rivertype.JobStateScheduled, -3 * time.Minute},
		{rivertype.JobStateRetryable, -time.Minute},
		{rivertype.JobStateAvailable, time.Hour},
		{rivertype.JobStateScheduled, time.Hour},
		{rivertype.JobStateRetryable, time.Hour},
		{rivertype.JobStatePending, -time.Hour},
		{rivertype.JobStateRunning, -time.Hour},
		{rivertype.JobStateCompleted, -time.Hour},
		{rivertype.JobStateCancelled, -time.Hour},
		{rivertype.JobStateDiscarded, -time.Hour},
	} {
		var id int64
		if err := s.app.QueryRow(ctx, `INSERT INTO river_job(kind, args, queue, state, scheduled_at, finalized_at)
			VALUES('queue-metrics', '{}', 'thread', $1::text::river_job_state, now() + make_interval(secs => $2),
			CASE WHEN $1::text IN ('completed', 'cancelled', 'discarded') THEN now() ELSE NULL END) RETURNING id`,
			tt.state, tt.delay.Seconds()).Scan(&id); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if _, err := s.app.Exec(context.Background(), `DELETE FROM river_job WHERE id = $1`, id); err != nil {
				t.Error(err)
			}
		})
	}
	stats, err := s.QueueStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := len(jobs.Queues()); len(stats) != want {
		t.Fatalf("queues = %d, want %d", len(stats), want)
	}
	for _, q := range stats {
		wantCount, wantAge := baseline[q.Queue].Runnable, baseline[q.Queue].OldestSeconds
		if q.Queue == jobs.QueueThread {
			wantCount += 3
			wantAge = max(wantAge, 300)
		}
		if q.Runnable != wantCount || q.OldestSeconds < wantAge || q.OldestSeconds > wantAge+10 {
			t.Errorf("queue = %+v, want %d jobs and age near %v seconds", q, wantCount, wantAge)
		}
		if wantCount == 0 && q.OldestSeconds != 0 {
			t.Errorf("empty queue age = %v, want 0", q.OldestSeconds)
		}
	}
}
