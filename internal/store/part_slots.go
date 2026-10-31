package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// SetPartSlots publishes capacity only after the worker holds its leases.
// Reductions precede lease release and cannot fall below remaining work.
func SetPartSlots(ctx context.Context, tx pgx.Tx, runID string, slots int) error {
	tag, err := tx.Exec(ctx, `UPDATE runner_runs SET part_slots = $2 WHERE id = $1`, runID, slots)
	if err != nil {
		return fmt.Errorf("store: set part slots: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// RemainingParts is nil until the runner knows its actual work. It includes
// active parts, so retaining that many slots cannot interrupt an active part.
func RemainingParts(ctx context.Context, tx pgx.Tx, runID string) (*int, error) {
	var remaining *int
	err := tx.QueryRow(ctx, `SELECT parts_remaining FROM runner_runs WHERE id = $1`, runID).Scan(&remaining)
	if err != nil {
		return nil, fmt.Errorf("store: read remaining parts: %w", err)
	}
	return remaining, nil
}

// ReportRemainingParts reports unfinished work and reads worker-owned capacity.
// A runner can update remaining work, but cannot grant itself slots.
func ReportRemainingParts(ctx context.Context, tx pgx.Tx, runID string, remaining int) (int, error) {
	var slots int
	err := tx.QueryRow(ctx, `UPDATE runner_runs SET parts_remaining = $2 WHERE id = $1 RETURNING part_slots`, runID, remaining).Scan(&slots)
	if err != nil {
		return 0, fmt.Errorf("store: report remaining parts: %w", err)
	}
	return slots, nil
}
