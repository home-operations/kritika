package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// DependencyDrift is how the updates of one dependency were rated over a
// window: per update, oldest first, the risk the judge gave before any
// hold, the reach it gave the dependency, and the step the update took.
type DependencyDrift struct {
	Repository string
	// Key names the dependency as the judge's verdicts record it (see
	// review.Bump), and Path is the file the key starts with.
	Key     string
	Path    string
	Risks   []string
	Reaches []string
	Steps   []string
	LastAt  time.Time
}

// ReadDependencyDrift reads, over the newest completed whole scored review
// of each pull request reviewed in [from, to), the dependencies that more
// than one pull request moved and how each update was rated, the ones rated
// at the most levels first, then the most updated, at most limit of them.
// A pull request counts once per dependency, by the first of its lines
// that share a key. A dependency rated at one level throughout is in the
// list too, as the measure of a consistent judge; the levels order puts
// the drift first.
func ReadDependencyDrift(ctx context.Context, tx pgx.Tx, from, to time.Time, limit int) ([]DependencyDrift, error) {
	rows, err := tx.Query(ctx, `WITH latest AS (
			SELECT DISTINCT ON (p.id) p.id AS pull_id, p.repository_id, r.created_at, r.confidence
			FROM pull_requests p JOIN reviews r ON r.pull_request_id = p.id
			WHERE r.status = 'completed' AND NOT r.partial AND r.confidence IS NOT NULL AND r.created_at >= $1 AND r.created_at < $2
			ORDER BY p.id, r.created_at DESC, r.id DESC),
		moved AS (
			SELECT DISTINCT ON (l.pull_id, b->>'key') l.pull_id, l.repository_id, l.created_at, b->>'key' AS key,
				coalesce(b->>'path', '') AS path, coalesce(b->>'step', '') AS step, b->>'reach' AS reach,
				coalesce(nullif(l.confidence->>'stepped', ''), nullif(l.confidence->>'rated', ''), l.confidence->>'risk', '') AS risk
			FROM latest l CROSS JOIN LATERAL jsonb_array_elements(l.confidence->'bumps') WITH ORDINALITY AS b(b, at)
			WHERE coalesce(b->>'reach', '') <> ''
			ORDER BY l.pull_id, b->>'key', b.at)
		SELECT repo.name, m.key, min(m.path), array_agg(m.risk ORDER BY m.created_at, m.pull_id),
			array_agg(m.reach ORDER BY m.created_at, m.pull_id), array_agg(m.step ORDER BY m.created_at, m.pull_id), max(m.created_at)
		FROM moved m JOIN repositories repo ON repo.id = m.repository_id
		GROUP BY repo.name, m.key HAVING count(*) > 1
		ORDER BY count(DISTINCT m.risk) DESC, count(*) DESC, repo.name, m.key LIMIT $3`, from, to, limit)
	if err != nil {
		return nil, fmt.Errorf("store: dependency drift: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (DependencyDrift, error) {
		var d DependencyDrift
		err := row.Scan(&d.Repository, &d.Key, &d.Path, &d.Risks, &d.Reaches, &d.Steps, &d.LastAt)
		d.LastAt = d.LastAt.UTC()
		return d, err
	})
	if err != nil {
		return nil, fmt.Errorf("store: dependency drift: %w", err)
	}
	return out, nil
}
