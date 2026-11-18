package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/runspec"
)

// PartProgress is what a split review's runner last reported, by model: its
// unfinished parts, those of them that run, and the slots that it read as it
// reported. Every field is nil until the runner reports.
type PartProgress struct {
	Remaining, Active, Seen map[string]int
}

// InUse is the most slots of model that the runner's parts can be in until
// its next report, out of the held that the review holds. The runner starts a
// part only within the slots that it read, never interrupts one that runs, and
// has no more parts than remain. A runner that has not reported still has the
// slots that its job started with, so every held slot counts.
func (p PartProgress) InUse(model string, held int) int {
	if p.Seen == nil {
		return held
	}
	return min(p.Remaining[model], max(p.Active[model], p.Seen[model]))
}

const partProgressColumns = `parts_remaining, parts_active, part_slots_seen`

func scanPartProgress(row pgx.Row) (PartProgress, error) {
	var remaining, active, seen []byte
	if err := row.Scan(&remaining, &active, &seen); err != nil {
		return PartProgress{}, err
	}
	var p PartProgress
	var err error
	if p.Remaining, err = decodeParts(remaining, "remaining parts"); err != nil {
		return PartProgress{}, err
	}
	if p.Active, err = decodeParts(active, "active parts"); err != nil {
		return PartProgress{}, err
	}
	if p.Seen, err = decodeParts(seen, "seen part slots"); err != nil {
		return PartProgress{}, err
	}
	return p, nil
}

// SetPartSlots publishes the slots a split review's runner can run parts in,
// by model. Only the worker that holds the leases writes it. It returns the
// runner's last report, which was made before the slots changed: a later
// report reads these slots.
func SetPartSlots(ctx context.Context, tx pgx.Tx, runID string, slots map[string]int) (PartProgress, error) {
	encoded, err := json.Marshal(slots)
	if err != nil {
		return PartProgress{}, fmt.Errorf("store: encode part slots: %w", err)
	}
	p, err := scanPartProgress(tx.QueryRow(ctx, `UPDATE runner_runs SET part_slots = $2::jsonb WHERE id = $1
		RETURNING `+partProgressColumns, runID, encoded))
	if errors.Is(err, pgx.ErrNoRows) {
		return PartProgress{}, ErrNotFound
	}
	if err != nil {
		return PartProgress{}, fmt.Errorf("store: set part slots: %w", err)
	}
	return p, nil
}

// ReadPartProgress is the run's last report of its parts. Its unfinished
// parts include those that run, so retaining that many slots cannot interrupt
// one.
func ReadPartProgress(ctx context.Context, tx pgx.Tx, runID string) (PartProgress, error) {
	p, err := scanPartProgress(tx.QueryRow(ctx, `SELECT `+partProgressColumns+` FROM runner_runs WHERE id = $1`, runID))
	if err != nil {
		return PartProgress{}, fmt.Errorf("store: read part progress: %w", err)
	}
	return p, nil
}

// ErrStaleReport is a report of a run's parts that is not later than the last
// one taken.
var ErrStaleReport = errors.New("store: a later report of the run's parts was taken")

// ReportRemainingParts takes a runner's report of its unfinished work, and
// reads worker-owned capacity. The row keeps the capacity read with the
// report. A report that is not later than the last one taken is an
// ErrStaleReport: the runner did not go on with the answer to it, so the
// parts that it names as running and the capacity read now say nothing of
// the slots that its parts are in.
func ReportRemainingParts(ctx context.Context, tx pgx.Tx, runID string, report runspec.PartsReport) (map[string]int, error) {
	remaining, err := json.Marshal(report.Remaining)
	if err != nil {
		return nil, fmt.Errorf("store: encode remaining parts: %w", err)
	}
	active, err := json.Marshal(report.Active)
	if err != nil {
		return nil, fmt.Errorf("store: encode active parts: %w", err)
	}
	var slots []byte
	err = tx.QueryRow(ctx, `UPDATE runner_runs SET parts_remaining = $2::jsonb, parts_active = $3::jsonb, part_slots_seen = part_slots,
		parts_sequence = $4 WHERE id = $1 AND parts_sequence < $4 RETURNING part_slots`,
		runID, remaining, active, report.Sequence).Scan(&slots)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrStaleReport
	}
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
