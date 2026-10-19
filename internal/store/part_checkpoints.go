package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// PartCheckpoint is a completed part and the run that paid for it.
type PartCheckpoint struct {
	Part      int
	SourceRun string
	Payload   json.RawMessage
}

// ReadPartCheckpoints reads only the current run's matching input snapshot.
func ReadPartCheckpoints(ctx context.Context, tx pgx.Tx, runID, inputHash string) ([]PartCheckpoint, error) {
	rows, err := tx.Query(ctx, `SELECT part, source_run_id::text, payload FROM review_part_checkpoints
		WHERE runner_run_id = $1 AND input_hash = $2 ORDER BY part`, runID, inputHash)
	if err != nil {
		return nil, fmt.Errorf("store: read part checkpoints: %w", err)
	}
	checkpoints, err := pgx.CollectRows(rows, pgx.RowToStructByPos[PartCheckpoint])
	if err != nil {
		return nil, fmt.Errorf("store: read part checkpoints: %w", err)
	}
	return checkpoints, nil
}

// SavePartCheckpoint keeps the first completed answer for this part.
func SavePartCheckpoint(ctx context.Context, tx pgx.Tx, runID, inputHash string, part int, payload json.RawMessage) error {
	_, err := tx.Exec(ctx, `INSERT INTO review_part_checkpoints (runner_run_id, account_id, input_hash, part, source_run_id, payload)
		SELECT id, account_id, $2, $3, id, $4 FROM runner_runs WHERE id = $1
		ON CONFLICT (runner_run_id, input_hash, part) DO NOTHING`, runID, inputHash, part, payload)
	if err != nil {
		return fmt.Errorf("store: save part checkpoint: %w", err)
	}
	return nil
}

// copyPartCheckpoints carries results between attempts of the same River job.
// Runners never receive access to another run's rows or credentials.
func copyPartCheckpoints(ctx context.Context, tx pgx.Tx, runID string, jobID int64) error {
	_, err := tx.Exec(ctx, `INSERT INTO review_part_checkpoints
		(runner_run_id, account_id, input_hash, part, source_run_id, payload)
		SELECT DISTINCT ON (c.input_hash, c.part) $1, c.account_id, c.input_hash, c.part, c.source_run_id, c.payload
		FROM review_part_checkpoints c JOIN runner_runs r ON r.id = c.runner_run_id
		WHERE r.river_job_id = $2 AND r.id <> $1
		ORDER BY c.input_hash, c.part, c.created_at DESC`, runID, jobID)
	if err != nil {
		return fmt.Errorf("store: carry part checkpoints: %w", err)
	}
	return nil
}
