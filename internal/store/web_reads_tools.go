package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// CommandUsage counts executions in saved agent runs, by command name.
// UncountedRuns have no execution counts and contribute nothing to Counts.
type CommandUsage struct {
	Counts                      map[string]int64
	RecordedRuns, UncountedRuns int64
}

// ReadCommandUsage uses the time the agent's result was saved, not the
// start time of each command. Retries have separate runner runs and count
// separately. All names are returned so instance rankings can combine accounts.
func ReadCommandUsage(ctx context.Context, tx pgx.Tx, from, to time.Time) (CommandUsage, error) {
	out := CommandUsage{Counts: map[string]int64{}}
	err := tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE command_counts IS NOT NULL),
		count(*) FILTER (WHERE command_counts IS NULL)
		FROM agent_runs WHERE created_at >= $1 AND created_at < $2`, from, to).
		Scan(&out.RecordedRuns, &out.UncountedRuns)
	if err != nil {
		return out, fmt.Errorf("store: command usage coverage: %w", err)
	}
	rows, err := tx.Query(ctx, `SELECT c.key, sum(c.value::bigint)::bigint
		FROM agent_runs a CROSS JOIN LATERAL jsonb_each_text(a.command_counts) c
		WHERE a.created_at >= $1 AND a.created_at < $2
		GROUP BY c.key HAVING sum(c.value::bigint) > 0`, from, to)
	if err != nil {
		return out, fmt.Errorf("store: command usage: %w", err)
	}
	var name string
	var count int64
	if _, err := pgx.ForEachRow(rows, []any{&name, &count}, func() error {
		out.Counts[name] = count
		return nil
	}); err != nil {
		return out, fmt.Errorf("store: command usage: %w", err)
	}
	return out, nil
}
