package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/jobs"
)

// QueueStats is shared database state, not a replica's worker count.
type QueueStats struct {
	Queue         string
	Runnable      int64
	OldestSeconds float64
}

// QueueStats includes due retries and scheduled jobs awaiting River's
// scheduler, but excludes future, pending, running and terminal jobs.
func (s *Store) QueueStats(ctx context.Context) ([]QueueStats, error) {
	rows, err := s.app.Query(ctx, `SELECT q.queue, count(j.id),
		greatest(0, coalesce(extract(epoch FROM now() - min(j.scheduled_at)), 0))::float8
		FROM unnest($1::text[]) AS q(queue)
		LEFT JOIN river_job j ON j.queue = q.queue
			AND j.state IN ('available', 'retryable', 'scheduled') AND j.scheduled_at <= now()
		GROUP BY q.queue ORDER BY q.queue`, []string{jobs.QueueReview, jobs.QueueFollowUp, jobs.QueueIndex})
	if err != nil {
		return nil, fmt.Errorf("store: queue stats: %w", err)
	}
	stats, err := pgx.CollectRows(rows, pgx.RowToStructByPos[QueueStats])
	if err != nil {
		return nil, fmt.Errorf("store: queue stats: %w", err)
	}
	return stats, nil
}
