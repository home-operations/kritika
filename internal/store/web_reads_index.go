package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river/rivertype"
)

// IndexJobRow is one of the account's index jobs still queued or running.
type IndexJobRow struct {
	ID           int64
	RepositoryID string
	State        rivertype.JobState
	Trigger      string
	// Full is a forced rebuild, as a reindex queues.
	Full        bool
	Attempt     int
	CreatedAt   time.Time
	ScheduledAt time.Time
	AttemptedAt *time.Time
}

// LiveIndexJobs lists the transaction's account's index jobs queued or
// running, only repositoryID's unless it is empty: running ones first,
// then in the order they are due. River's tables carry no row-level
// security, so the account comes from the transaction's own setting, as
// jobColumns' does.
func LiveIndexJobs(ctx context.Context, tx pgx.Tx, repositoryID string) ([]IndexJobRow, error) {
	rows, err := tx.Query(ctx, `SELECT j.id, coalesce(j.args->>'repository_id', ''), j.state::text,
			coalesce(j.args->>'trigger', ''), coalesce((j.args->>'full')::boolean, false),
			j.attempt, j.created_at, j.scheduled_at, j.attempted_at
		FROM river_job j
		WHERE j.args->>'account_id' = current_setting('app.account_id', true) AND `+liveIndexJob+`
		  AND ($1 = '' OR j.args->>'repository_id' = $1)
		ORDER BY j.state = 'running' DESC, j.scheduled_at, j.id`, repositoryID)
	if err != nil {
		return nil, fmt.Errorf("store: list live index jobs: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (IndexJobRow, error) {
		var j IndexJobRow
		var state string
		err := row.Scan(&j.ID, &j.RepositoryID, &state, &j.Trigger, &j.Full, &j.Attempt, &j.CreatedAt, &j.ScheduledAt, &j.AttemptedAt)
		j.State = rivertype.JobState(state)
		return j, err
	})
	if err != nil {
		return nil, fmt.Errorf("store: list live index jobs: %w", err)
	}
	return out, nil
}

// IndexContents is what a repository's active index generation holds.
type IndexContents struct {
	RunID      string
	Commit     string
	EmbedModel string
	EmbedDims  int
	CreatedAt  time.Time
	FinishedAt *time.Time
	Chunks     int
	Files      int
	// Current is false when another embedder than the applied one built
	// it: the gateway does not query it, and onboarding rebuilds it.
	Current bool
}

// ReadIndexContents reads what repositoryID's active index generation
// holds, or ErrNotFound when it has none.
func ReadIndexContents(ctx context.Context, tx pgx.Tx, repositoryID string) (IndexContents, error) {
	var c IndexContents
	err := tx.QueryRow(ctx, `SELECT g.id::text, g.commit_sha, g.embed_model, g.embed_dims, g.created_at, g.finished_at,
			EXISTS (SELECT 1 FROM index_schema s
				WHERE (s.embed_model, s.embed_dims, s.document_prompt) = (g.embed_model, g.embed_dims, g.document_prompt))
		FROM repositories r JOIN index_runs g ON g.id = r.active_index_run_id
		WHERE r.id = $1`, repositoryID).
		Scan(&c.RunID, &c.Commit, &c.EmbedModel, &c.EmbedDims, &c.CreatedAt, &c.FinishedAt, &c.Current)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrNotFound
	}
	if err != nil {
		return c, fmt.Errorf("store: read index contents: %w", err)
	}
	// A query apart: index_chunks exists only once an embedder was applied,
	// which a repository with an active generation implies.
	if err := tx.QueryRow(ctx, `SELECT count(*), count(DISTINCT path) FROM index_chunks WHERE index_run_id = $1`, c.RunID).
		Scan(&c.Chunks, &c.Files); err != nil {
		return c, fmt.Errorf("store: count index chunks: %w", err)
	}
	return c, nil
}

// ActiveIndexRun returns the ID of repositoryID's active index generation,
// "" when it has none.
func ActiveIndexRun(ctx context.Context, tx pgx.Tx, repositoryID string) (string, error) {
	var id *string
	err := tx.QueryRow(ctx, `SELECT active_index_run_id::text FROM repositories WHERE id = $1`, repositoryID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("store: read active index run: %w", err)
	}
	if id == nil {
		return "", nil
	}
	return *id, nil
}

// IndexFileRow is one file of an index generation and its chunk count.
type IndexFileRow struct {
	Path     string
	Language string
	Chunks   int
}

// ListIndexFiles returns a page of the files the generation runID holds,
// those whose path contains q in any case when it is not empty, ordered
// by path. The cursor's ID is the generation's, which the keyset does not
// read: a page after the generation changed goes on from the same path.
func ListIndexFiles(ctx context.Context, tx pgx.Tx, runID, q string, p Page) ([]IndexFileRow, *Cursor, error) {
	if err := p.check(); err != nil {
		return nil, nil, err
	}
	rows, err := tx.Query(ctx, `SELECT path, max(language), count(*) FROM index_chunks
		WHERE index_run_id = $1 AND ($2 = '' OR strpos(lower(path), lower($2)) > 0) AND ($3 OR path > $4)
		GROUP BY path ORDER BY path LIMIT $5`, runID, q, p.After.First(), p.After.S, p.Limit+1)
	if err != nil {
		return nil, nil, fmt.Errorf("store: list index files: %w", err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[IndexFileRow])
	if err != nil {
		return nil, nil, fmt.Errorf("store: list index files: %w", err)
	}
	items, next := paged(out, p.Limit, func(f IndexFileRow) Cursor { return Cursor{S: f.Path, ID: runID} })
	return items, next, nil
}

// IndexChunkRow is one chunk of a file in an index generation.
type IndexChunkRow struct {
	StartLine int
	EndLine   int
	Language  string
	Symbol    string
	Kind      string
	Scope     string
	Text      string
}

// ListIndexChunks returns up to limit of the chunks the generation runID
// holds of path, in the file's order.
func ListIndexChunks(ctx context.Context, tx pgx.Tx, runID, path string, limit int) ([]IndexChunkRow, error) {
	rows, err := tx.Query(ctx, `SELECT start_line, end_line, language, symbol, kind, scope, text FROM index_chunks
		WHERE index_run_id = $1 AND path = $2 ORDER BY start_line, end_line, id LIMIT $3`, runID, path, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list index chunks: %w", err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[IndexChunkRow])
	if err != nil {
		return nil, fmt.Errorf("store: list index chunks: %w", err)
	}
	return out, nil
}
