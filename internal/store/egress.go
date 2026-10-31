package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// refusalsPerRun bounds the refusal rows that a run has, one per kind, host
// and reason. Past it, a run's further refusals are counted on the rows it
// has and new ones go unrecorded. So a looping agent cannot fill the table.
// It is a variable for the tests.
var refusalsPerRun = 100

// EgressRefusal is one proxy request the gateway refused a run, with the
// host as the agent asked for it.
type EgressRefusal struct {
	RunID, AccountID string
	// RepositoryID can be empty for a run that has no repository.
	RepositoryID       string
	Kind, Host, Reason string
}

// RecordEgressRefusal counts r against its run: a row for the first
// refusal of a host for a reason, and that row's count after. It reports
// false when the run already has refusalsPerRun rows and this refusal will
// otherwise be another. The run's refusals are recorded one at a time, under an
// advisory lock that the transaction holds. So two at once cannot both pass
// the bound.
func RecordEgressRefusal(ctx context.Context, tx pgx.Tx, r EgressRefusal) (bool, error) {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('egress_refusals:' || $1::text, 0))`, r.RunID); err != nil {
		return false, fmt.Errorf("store: record egress refusal: %w", err)
	}
	tag, err := tx.Exec(ctx, `INSERT INTO egress_refusals (runner_run_id, account_id, repository_id, kind, host, reason)
		SELECT $1::uuid, $2::uuid, NULLIF($3, '')::uuid, $4::text, $5::text, $6::text
		WHERE EXISTS (SELECT 1 FROM egress_refusals WHERE runner_run_id = $1 AND kind = $4 AND host = $5 AND reason = $6)
		   OR (SELECT count(*) FROM egress_refusals WHERE runner_run_id = $1) < $7
		ON CONFLICT (runner_run_id, kind, host, reason) DO UPDATE SET count = egress_refusals.count + 1, last_seen_at = now()`,
		r.RunID, r.AccountID, r.RepositoryID, r.Kind, r.Host, r.Reason, refusalsPerRun)
	if err != nil {
		return false, fmt.Errorf("store: record egress refusal: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// EgressRefusalRow is how often one host was refused over a window, by
// the runs of one repository. A run belongs to one repository, so Runs
// add up across a host's rows.
type EgressRefusalRow struct {
	// Repository is the full name, "" for runs without one.
	Repository string
	Host       string
	// Count is the refusals and Runs the distinct runs they came from.
	Count, Runs int64
	// Reasons counts the refusals by reason.
	Reasons    map[string]int64
	LastSeenAt time.Time
}

// ReadEgressRefusals lists the hosts refused in [from, to), by when they
// were last refused, per repository, the most refused first. It lists every
// row of the limit most refused hosts, counted over every repository. So a
// host refused a little in many repositories is not lost behind one refused
// much in one.
func ReadEgressRefusals(ctx context.Context, tx pgx.Tx, from, to time.Time, limit int) ([]EgressRefusalRow, error) {
	rows, err := tx.Query(ctx, `WITH refused AS (
			SELECT coalesce(r.name, '') AS repository, e.host, e.reason, e.runner_run_id, e.count, e.last_seen_at
			FROM egress_refusals e LEFT JOIN repositories r ON r.id = e.repository_id
			WHERE e.last_seen_at >= $1 AND e.last_seen_at < $2),
		hosts AS (SELECT host FROM refused GROUP BY host ORDER BY sum(count) DESC, host LIMIT $3),
		reasons AS (
			SELECT repository, host, jsonb_object_agg(reason, n) AS reasons
			FROM (SELECT repository, host, reason, sum(count) AS n FROM refused GROUP BY 1, 2, 3) by_reason GROUP BY 1, 2)
		SELECT w.repository, w.host, sum(w.count)::bigint, count(DISTINCT w.runner_run_id), max(w.last_seen_at), rs.reasons
		FROM refused w JOIN hosts h ON h.host = w.host JOIN reasons rs ON rs.repository = w.repository AND rs.host = w.host
		GROUP BY w.repository, w.host, rs.reasons ORDER BY 3 DESC, 2, 1`, from, to, limit)
	if err != nil {
		return nil, fmt.Errorf("store: egress refusals: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (EgressRefusalRow, error) {
		var r EgressRefusalRow
		err := row.Scan(&r.Repository, &r.Host, &r.Count, &r.Runs, &r.LastSeenAt, &r.Reasons)
		return r, err
	})
	if err != nil {
		return nil, fmt.Errorf("store: egress refusals: %w", err)
	}
	return out, nil
}

// SweepEgressRefusals deletes every account's egress refusals last seen
// more than olderThan ago, a batch at a time, and returns how many it
// deleted. Owner connection, leader only, like SweepModelCalls.
func (s *Store) SweepEgressRefusals(ctx context.Context, olderThan time.Duration) (int64, error) {
	if s.owner == nil {
		return 0, errors.New("store: SweepEgressRefusals needs the owner connection")
	}
	if olderThan <= 0 {
		return 0, fmt.Errorf("store: egress refusal retention %s is not positive", olderThan)
	}
	n, err := s.sweepBatches(ctx, `DELETE FROM egress_refusals WHERE (runner_run_id, kind, host, reason) IN (
		SELECT runner_run_id, kind, host, reason FROM egress_refusals WHERE last_seen_at < now() - make_interval(secs => $1) LIMIT $2)`,
		olderThan, modelCallSweepBatch)
	if err != nil {
		return n, fmt.Errorf("store: sweep egress refusals: %w", err)
	}
	return n, nil
}
