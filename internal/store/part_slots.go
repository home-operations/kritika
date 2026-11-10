package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// SetPartSlots publishes the slots a split review's runner can run parts in,
// by model. Only the worker that holds the leases writes it.
func SetPartSlots(ctx context.Context, tx pgx.Tx, runID string, slots map[string]int) error {
	encoded, err := json.Marshal(slots)
	if err != nil {
		return fmt.Errorf("store: encode part slots: %w", err)
	}
	tag, err := tx.Exec(ctx, `UPDATE runner_runs SET part_slots = $2::jsonb WHERE id = $1`, runID, encoded)
	if err != nil {
		return fmt.Errorf("store: set part slots: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// RemainingParts is the run's unfinished parts by model, nil until the
// runner knows its actual work. It includes active parts, so retaining that
// many slots cannot interrupt an active part.
func RemainingParts(ctx context.Context, tx pgx.Tx, runID string) (map[string]int, error) {
	var encoded []byte
	err := tx.QueryRow(ctx, `SELECT parts_remaining FROM runner_runs WHERE id = $1`, runID).Scan(&encoded)
	if err != nil {
		return nil, fmt.Errorf("store: read remaining parts: %w", err)
	}
	return decodeParts(encoded, "remaining parts")
}

// ReportRemainingParts reports unfinished work by model and reads
// worker-owned capacity. The gateway takes the remaining work from a runner,
// and no slots.
func ReportRemainingParts(ctx context.Context, tx pgx.Tx, runID string, remaining map[string]int) (map[string]int, error) {
	encoded, err := json.Marshal(remaining)
	if err != nil {
		return nil, fmt.Errorf("store: encode remaining parts: %w", err)
	}
	var slots []byte
	err = tx.QueryRow(ctx, `UPDATE runner_runs SET parts_remaining = $2::jsonb WHERE id = $1 RETURNING part_slots`,
		runID, encoded).Scan(&slots)
	if err != nil {
		return nil, fmt.Errorf("store: report remaining parts: %w", err)
	}
	return decodeParts(slots, "part slots")
}

// decodeParts decodes a count by model, nil for a null.
func decodeParts(encoded []byte, what string) (map[string]int, error) {
	if encoded == nil {
		return nil, nil
	}
	var out map[string]int
	if err := json.Unmarshal(encoded, &out); err != nil {
		return nil, fmt.Errorf("store: decode %s: %w", what, err)
	}
	return out, nil
}
