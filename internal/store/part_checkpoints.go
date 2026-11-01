package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/runspec"
)

// PartCheckpoint is a completed part and the run that paid for it.
type PartCheckpoint = runspec.PartCheckpoint

// maxCheckpointRead bounds what one read of a run's checkpoints holds in
// memory. What a runner saved decides the number and size of the rows, and the
// read runs in the service.
const maxCheckpointRead = 32 << 20

// ReadPartCheckpoints reads only the current run's matching input snapshot,
// within maxCheckpointRead. A part left out runs again, as one with no
// checkpoint does. A row past the bound stays in the database, since one saved
// before the gateway bounded saves can be of any size.
func ReadPartCheckpoints(ctx context.Context, tx pgx.Tx, runID, inputHash string) ([]PartCheckpoint, error) {
	rows, err := tx.Query(ctx, `SELECT part, source_run_id::text, payload FROM review_part_checkpoints
		WHERE runner_run_id = $1 AND input_hash = $2 AND octet_length(payload) <= $3 ORDER BY part`, runID, inputHash, maxCheckpointRead)
	if err != nil {
		return nil, fmt.Errorf("store: read part checkpoints: %w", err)
	}
	defer rows.Close()
	var checkpoints []PartCheckpoint
	total := 0
	for rows.Next() {
		var c PartCheckpoint
		var payload string
		if err := rows.Scan(&c.Part, &c.SourceRun, &payload); err != nil {
			return nil, fmt.Errorf("store: read part checkpoints: %w", err)
		}
		if total += len(payload); total > maxCheckpointRead {
			break
		}
		c.Payload = json.RawMessage(payload)
		checkpoints = append(checkpoints, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: read part checkpoints: %w", err)
	}
	return checkpoints, nil
}

// SavePartCheckpoint keeps the first completed answer for this part.
func SavePartCheckpoint(ctx context.Context, tx pgx.Tx, runID, inputHash string, part int, payload json.RawMessage) error {
	_, err := tx.Exec(ctx, `INSERT INTO review_part_checkpoints (runner_run_id, account_id, input_hash, part, source_run_id, payload)
		SELECT id, account_id, $2, $3, id, $4 FROM runner_runs WHERE id = $1
		ON CONFLICT (runner_run_id, input_hash, part) DO NOTHING`, runID, inputHash, part, string(payload))
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
