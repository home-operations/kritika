package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/jobs"
)

// LockIndexJob fences database writes from cancelled or superseded attempts.
// Lock it before the run or repository so publication and checkpoints agree
// on lock order. River tables have no RLS, so account ownership is explicit.
func LockIndexJob(ctx context.Context, tx pgx.Tx, accountID string, jobID int64, attempt int) error {
	var owned bool
	err := tx.QueryRow(ctx, `SELECT state = 'running' AND attempt = $3 AND NOT (metadata ? 'cancel_attempted_at')
		FROM river_job WHERE id = $1 AND kind = 'index' AND args->>'account_id' = $2 FOR UPDATE`, jobID, accountID, attempt).Scan(&owned)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !owned {
		return errors.New("store: index job ownership changed")
	}
	if err != nil {
		return fmt.Errorf("store: lock index job: %w", err)
	}
	return nil
}

// ResumeIndexRun reuses completed staging for the same job and input identity.
// Incompatible or incomplete staging is discarded before a new run starts.
// The caller holds the River job lock.
func ResumeIndexRun(ctx context.Context, tx pgx.Tx, jobID int64, key string) (runID, runnerRunID string, err error) {
	rows, err := tx.Query(ctx, `SELECT x.id::text, rr.id::text, x.resume_key = $2 AND x.embedding_ready
		AND x.status IN ('running', 'failed')
		AND x.chunk_count = (SELECT count(*) FROM index_chunks c WHERE c.index_run_id = x.id)
		AND EXISTS (SELECT 1 FROM index_packs p WHERE p.runner_run_id = rr.id
			AND p.chunk_count = (SELECT count(*) FROM index_staging s WHERE s.runner_run_id = rr.id)) AS compatible
		FROM index_runs x JOIN runner_runs rr ON rr.index_run_id = x.id
		WHERE rr.river_job_id = $1 AND x.resume_key <> '' ORDER BY x.created_at DESC`, jobID, key)
	if err != nil {
		return "", "", fmt.Errorf("store: find resumable index: %w", err)
	}
	type candidate struct {
		Run, Runner string
		Compatible  bool
	}
	candidates, err := pgx.CollectRows(rows, pgx.RowToStructByPos[candidate])
	if err != nil {
		return "", "", fmt.Errorf("store: read resumable index: %w", err)
	}
	for _, c := range candidates {
		if c.Compatible && runID == "" {
			runID, runnerRunID = c.Run, c.Runner
			if _, err := tx.Exec(ctx, `UPDATE index_runs SET status = 'running', finished_at = NULL, error = '' WHERE id = $1`, runID); err != nil {
				return "", "", fmt.Errorf("store: resume index: %w", err)
			}
			continue
		}
		if err := DiscardIndexProgress(ctx, tx, c.Run, c.Runner); err != nil {
			return "", "", err
		}
	}
	return runID, runnerRunID, nil
}

// ReadyIndexRun seals staging only after the runner has exited successfully.
func ReadyIndexRun(ctx context.Context, tx pgx.Tx, runID, runnerRunID string) error {
	if _, err := ReadIndexPack(ctx, tx, runID, runnerRunID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE runner_runs SET access_token_hash = NULL WHERE id = $1`, runnerRunID); err != nil {
		return fmt.Errorf("store: seal index staging: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE index_runs SET embedding_ready = true WHERE id = $1 AND status = 'running'`, runID); err != nil {
		return fmt.Errorf("store: mark index ready: %w", err)
	}
	return nil
}

// IndexProgress reads the last atomically committed batch and its chunk count.
func IndexProgress(ctx context.Context, tx pgx.Tx, runID string) (last int64, total int, err error) {
	err = tx.QueryRow(ctx, `SELECT embedding_cursor, chunk_count FROM index_runs WHERE id = $1 AND status = 'running'`, runID).
		Scan(&last, &total)
	if err != nil {
		return 0, 0, fmt.Errorf("store: read index progress: %w", err)
	}
	return last, total, nil
}

// CheckpointIndexBatch must share the vector insertion's transaction. Its
// compare-and-swap rejects duplicate or stale batches without keeping vectors.
func CheckpointIndexBatch(ctx context.Context, tx pgx.Tx, runID string, before, after int64, count int) error {
	tag, err := tx.Exec(ctx, `UPDATE index_runs SET embedding_cursor = $3, chunk_count = chunk_count + $4
		WHERE id = $1 AND embedding_cursor = $2 AND status = 'running'`, runID, before, after, count)
	if err != nil {
		return fmt.Errorf("store: checkpoint index batch: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return errors.New("store: index progress changed")
	}
	return nil
}

// DiscardIndexProgress retains published vectors and historical run records.
func DiscardIndexProgress(ctx context.Context, tx pgx.Tx, runID, runnerRunID string) error {
	if _, err := tx.Exec(ctx, `UPDATE runner_runs SET access_token_hash = NULL WHERE id = $1`, runnerRunID); err != nil {
		return fmt.Errorf("store: revoke discarded index runner: %w", err)
	}
	if err := ClearIndexStaging(ctx, tx, runID, runnerRunID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE index_runs SET resume_key = '', embedding_ready = false,
		status = CASE WHEN status = 'running' THEN 'failed' ELSE status END,
		finished_at = coalesce(finished_at, now()) WHERE id = $1`, runID); err != nil {
		return fmt.Errorf("store: discard index progress: %w", err)
	}
	return nil
}

// SweepIndexProgress releases checkpoints after terminal jobs, including those
// cancelled while waiting for a retry. Owner connection; bounded by limit.
func (s *Store) SweepIndexProgress(ctx context.Context, limit int) error {
	if s.owner == nil {
		return errors.New("store: SweepIndexProgress needs the owner connection")
	}
	rows, err := s.owner.Query(ctx, `SELECT x.id::text, rr.id::text, rr.river_job_id
		FROM index_runs x JOIN runner_runs rr ON rr.index_run_id = x.id
		WHERE x.resume_key <> '' AND NOT EXISTS (SELECT 1 FROM river_job j
			WHERE j.id = rr.river_job_id AND j.state IN (`+jobs.LiveStatesSQL()+`))
		ORDER BY x.created_at LIMIT $1`, limit)
	if err != nil {
		return fmt.Errorf("store: list finished index progress: %w", err)
	}
	type candidate struct {
		Run, Runner string
		Job         int64
	}
	candidates, err := pgx.CollectRows(rows, pgx.RowToStructByPos[candidate])
	if err != nil {
		return fmt.Errorf("store: read finished index progress: %w", err)
	}
	for _, c := range candidates {
		if err := pgx.BeginFunc(ctx, s.owner, func(tx pgx.Tx) error {
			var live bool
			err := tx.QueryRow(ctx, `SELECT state IN (`+jobs.LiveStatesSQL()+`) FROM river_job WHERE id = $1 FOR UPDATE`, c.Job).Scan(&live)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if live {
				return nil
			}
			return DiscardIndexProgress(ctx, tx, c.Run, c.Runner)
		}); err != nil {
			return fmt.Errorf("store: sweep index progress: %w", err)
		}
	}
	return nil
}
