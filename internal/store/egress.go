package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// RefusalsPerRun bounds the refusal rows a run has, one per kind, host
// and reason: past it a run's further refusals are counted on the rows it
// has and new ones go unrecorded, so a looping agent cannot fill the
// table. A variable for the tests.
var RefusalsPerRun = 100

// EgressRefusal is one proxy request the gateway refused a run, with the
// host as the agent asked for it.
type EgressRefusal struct {
	RunID, AccountID string
	// RepositoryID may be empty for a run that has no repository.
	RepositoryID       string
	Kind, Host, Reason string
}

// RecordEgressRefusal counts r against its run: a row for the first
// refusal of a host for a reason, and that row's count after. It reports
// false when the run already has RefusalsPerRun rows and this would be
// another.
func RecordEgressRefusal(ctx context.Context, tx pgx.Tx, r EgressRefusal) (bool, error) {
	tag, err := tx.Exec(ctx, `INSERT INTO egress_refusals (runner_run_id, account_id, repository_id, kind, host, reason)
		SELECT $1::uuid, $2::uuid, NULLIF($3, '')::uuid, $4::text, $5::text, $6::text
		WHERE EXISTS (SELECT 1 FROM egress_refusals WHERE runner_run_id = $1 AND kind = $4 AND host = $5 AND reason = $6)
		   OR (SELECT count(*) FROM egress_refusals WHERE runner_run_id = $1) < $7
		ON CONFLICT (runner_run_id, kind, host, reason) DO UPDATE SET count = egress_refusals.count + 1, last_seen_at = now()`,
		r.RunID, r.AccountID, r.RepositoryID, r.Kind, r.Host, r.Reason, RefusalsPerRun)
	if err != nil {
		return false, fmt.Errorf("store: record egress refusal: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// EgressRefusalRow is how often one host was refused for one reason over
// a window, by the runs of one repository.
type EgressRefusalRow struct {
	// Repository is the full name, "" for runs without one.
	Repository   string
	Host, Reason string
	// Count is the refusals and Runs the runs they came from.
	Count, Runs int64
	LastSeenAt  time.Time
}

// ReadEgressRefusals lists the hosts refused in [from, to), by when they
// were last refused, per repository and reason, the most refused first,
// at most limit rows.
func ReadEgressRefusals(ctx context.Context, tx pgx.Tx, from, to time.Time, limit int) ([]EgressRefusalRow, error) {
	rows, err := tx.Query(ctx, `SELECT coalesce(r.name, ''), e.host, e.reason, sum(e.count)::bigint, count(DISTINCT e.runner_run_id),
			max(e.last_seen_at)
		FROM egress_refusals e LEFT JOIN repositories r ON r.id = e.repository_id
		WHERE e.last_seen_at >= $1 AND e.last_seen_at < $2
		GROUP BY 1, 2, 3 ORDER BY 4 DESC, 2, 3, 1 LIMIT $3`, from, to, limit)
	if err != nil {
		return nil, fmt.Errorf("store: egress refusals: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (EgressRefusalRow, error) {
		var r EgressRefusalRow
		err := row.Scan(&r.Repository, &r.Host, &r.Reason, &r.Count, &r.Runs, &r.LastSeenAt)
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
