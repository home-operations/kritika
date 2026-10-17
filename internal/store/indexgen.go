package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/model"
)

// Index modes, as index_runs and index_packs spell them.
const (
	IndexModeFull        = "full"
	IndexModeIncremental = "incremental"
)

// IndexRepo is a repository as an index job reads it: its name, default
// branch, whether it runs, its active generation ("" for none) and its
// traits.
type IndexRepo struct {
	Name, DefaultBranch string
	Enabled             bool
	ActiveRun           string
	Traits              configfile.RepoTraits
}

// FindIndexRepo reads the repository an index job is for, or ErrNotFound.
func FindIndexRepo(ctx context.Context, tx pgx.Tx, repositoryID string) (IndexRepo, error) {
	var r IndexRepo
	var active *string
	err := tx.QueryRow(ctx, `SELECT name, default_branch, enabled, active_index_run_id::text, archived, fork, turned_on
		FROM repositories WHERE id = $1`, repositoryID).
		Scan(&r.Name, &r.DefaultBranch, &r.Enabled, &active, &r.Traits.Archived, &r.Traits.Fork, &r.Traits.TurnedOn)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrNotFound
	}
	if err != nil {
		return r, fmt.Errorf("store: find repository: %w", err)
	}
	if active != nil {
		r.ActiveRun = *active
	}
	return r, nil
}

// RecordDefaultBranch records the repository's default branch when none
// is known yet.
func RecordDefaultBranch(ctx context.Context, tx pgx.Tx, repositoryID, branch string) error {
	if _, err := tx.Exec(ctx, `UPDATE repositories SET default_branch = $2 WHERE id = $1 AND default_branch = ''`,
		repositoryID, branch); err != nil {
		return fmt.Errorf("store: record default branch: %w", err)
	}
	return nil
}

// Generation is a repository's active index generation: what it covers
// and the embedder it was built with.
type Generation struct {
	ID, Commit, Model, IgnoreHash, DocumentPrompt string
	Dims                                          int
}

// FindGeneration reads a completed index generation, or ErrNotFound.
func FindGeneration(ctx context.Context, tx pgx.Tx, runID string) (Generation, error) {
	g := Generation{ID: runID}
	err := tx.QueryRow(ctx, `SELECT commit_sha, embed_model, embed_dims, ignore_hash, document_prompt FROM index_runs
		WHERE id = $1 AND status = 'completed'`, runID).
		Scan(&g.Commit, &g.Model, &g.Dims, &g.IgnoreHash, &g.DocumentPrompt)
	if errors.Is(err, pgx.ErrNoRows) {
		return g, ErrNotFound
	}
	if err != nil {
		return g, fmt.Errorf("store: find generation: %w", err)
	}
	return g, nil
}

// NewIndexRun is an index run the worker starts.
type NewIndexRun struct {
	AccountID, RepositoryID, Commit, Base, Mode, Trigger string
	Embedding                                            configfile.Embedding
	IgnoreHash                                           string
	ResumeKey                                            string
	// JobID is the River job working the run.
	JobID int64
	// StrayAfter is how old a run other than the active generation must be
	// before its chunks are dropped as left behind by a job that could not
	// clear them.
	StrayAfter time.Duration
}

// StartIndexRun records a running index run and its runner run and
// returns their ids. Checkpointed runs are left to SweepIndexProgress, which
// locks their jobs before cleanup so a manual retry cannot race deletion.
func StartIndexRun(ctx context.Context, tx pgx.Tx, r NewIndexRun) (runID, runnerRunID string, err error) {
	if _, err := tx.Exec(ctx, `DELETE FROM index_chunks WHERE index_run_id IN (
		SELECT x.id FROM index_runs x JOIN repositories r ON r.id = x.repository_id
		WHERE r.id = $1 AND x.id IS DISTINCT FROM r.active_index_run_id AND x.created_at < now() - make_interval(secs => $2)
		AND x.resume_key = '')`,
		r.RepositoryID, r.StrayAfter.Seconds()); err != nil {
		return "", "", fmt.Errorf("store: drop stray chunks: %w", err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO index_runs
		(account_id, repository_id, commit_sha, base_sha, embed_model, embed_dims, document_prompt, mode, status, trigger,
		 ignore_hash, resume_key)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'running', $9, $10, $11) RETURNING id`,
		r.AccountID, r.RepositoryID, r.Commit, r.Base, r.Embedding.Model, r.Embedding.Dims, r.Embedding.DocumentPrompt, r.Mode, r.Trigger,
		r.IgnoreHash, r.ResumeKey).
		Scan(&runID); err != nil {
		return "", "", fmt.Errorf("store: insert index run: %w", err)
	}
	runnerRunID, err = InsertRunnerRun(ctx, tx, r.AccountID, RunnerKindIndex, runID, r.JobID)
	return runID, runnerRunID, err
}

// FinishIndexRun ends an index run as status. Checkpoints own the chunk count
// until successful publication; failure bookkeeping cannot overwrite it.
// Publication requires a running run; failure cleanup leaves terminal runs
// unchanged so a lost COMMIT acknowledgement cannot overwrite a published run.
func FinishIndexRun(ctx context.Context, tx pgx.Tx, runID string, status IndexRunStatus, chunks int, errText string) error {
	tag, err := tx.Exec(ctx, `UPDATE index_runs SET status = $2,
		chunk_count = CASE WHEN $2 = 'completed' THEN $3 ELSE chunk_count END,
		error = left($4, 2000), finished_at = now(),
		resume_key = CASE WHEN $2 = 'completed' THEN '' ELSE resume_key END,
		embedding_ready = CASE WHEN $2 = 'completed' THEN false ELSE embedding_ready END
		WHERE id = $1 AND status = 'running'`,
		runID, status, chunks, errText)
	if err != nil {
		return fmt.Errorf("store: finish index run: %w", err)
	}
	if status == IndexCompleted && tag.RowsAffected() != 1 {
		return errors.New("store: cannot publish an index run that is no longer running")
	}
	return nil
}

// IndexPack is what an index runner wrote: the mode it built in, which
// may be full where an incremental step was asked for, and the paths that
// changed since the base.
type IndexPack struct {
	Mode, Base   string
	ChangedPaths []string
}

// ReadIndexPack reads the index pack a runner run wrote and records its
// mode on the run, since the runner may have fallen back to a full build.
func ReadIndexPack(ctx context.Context, tx pgx.Tx, runID, runnerRunID string) (IndexPack, error) {
	var p IndexPack
	if err := tx.QueryRow(ctx, `SELECT mode, base_sha, changed_paths FROM index_packs WHERE runner_run_id = $1`, runnerRunID).
		Scan(&p.Mode, &p.Base, &p.ChangedPaths); err != nil {
		return p, fmt.Errorf("store: read index pack: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE index_runs SET mode = $2, base_sha = $3 WHERE id = $1`, runID, p.Mode, p.Base); err != nil {
		return p, fmt.Errorf("store: record index mode: %w", err)
	}
	return p, nil
}

// StagedChunk is one chunk an index runner staged, before embedding.
type StagedChunk struct {
	ID                                        int64
	Path, Language, Symbol, Kind, Scope, Text string
	StartLine, EndLine                        int
}

// ReadStagedChunks reads up to limit of a runner run's staged chunks with
// an id past after, in id order.
func ReadStagedChunks(ctx context.Context, tx pgx.Tx, runnerRunID string, after int64, limit int) ([]StagedChunk, error) {
	rows, err := tx.Query(ctx, `SELECT id, path, start_line, end_line, language, symbol, kind, scope, text
		FROM index_staging WHERE runner_run_id = $1 AND id > $2 ORDER BY id LIMIT $3`, runnerRunID, after, limit)
	if err != nil {
		return nil, fmt.Errorf("store: read staged chunks: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (StagedChunk, error) {
		var c StagedChunk
		err := row.Scan(&c.ID, &c.Path, &c.StartLine, &c.EndLine, &c.Language, &c.Symbol, &c.Kind, &c.Scope, &c.Text)
		return c, err
	})
	if err != nil {
		return nil, fmt.Errorf("store: read staged chunks: %w", err)
	}
	return out, nil
}

// InsertIndexChunks records a batch of embedded chunks under the run.
func InsertIndexChunks(
	ctx context.Context, tx pgx.Tx, accountID, repositoryID, runID string, batch []StagedChunk, vectors [][]float32,
) error {
	if len(vectors) != len(batch) {
		return fmt.Errorf("store: %d vectors for %d chunks", len(vectors), len(batch))
	}
	n := len(batch)
	strs := func() []string { return make([]string, n) }
	paths, langs, symbols, kinds, scopes, texts, embeddings := strs(), strs(), strs(), strs(), strs(), strs(), strs()
	starts, ends := make([]int32, n), make([]int32, n)
	for i, c := range batch {
		paths[i], langs[i], symbols[i], kinds[i], scopes[i], texts[i] = c.Path, c.Language, c.Symbol, c.Kind, c.Scope, c.Text
		starts[i], ends[i] = int32(c.StartLine), int32(c.EndLine)
		embeddings[i] = model.VectorLiteral(vectors[i])
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO index_chunks
			(account_id, repository_id, index_run_id, path, start_line, end_line, language, symbol, kind, scope, text, embedding)
		SELECT $1, $2, $3, c.path, c.start_line, c.end_line, c.language, c.symbol, c.kind, c.scope, c.text, c.embedding::halfvec
		FROM unnest($4::text[], $5::int[], $6::int[], $7::text[], $8::text[], $9::text[], $10::text[], $11::text[], $12::text[])
			AS c (path, start_line, end_line, language, symbol, kind, scope, text, embedding)`,
		accountID, repositoryID, runID, paths, starts, ends, langs, symbols, kinds, scopes, texts, embeddings); err != nil {
		return fmt.Errorf("store: insert chunks: %w", err)
	}
	return nil
}

// ErrGenerationChanged means another index run published after an
// incremental step read its base generation. The step must be built again
// against the new base.
var ErrGenerationChanged = errors.New("store: index generation changed")

// lockActiveGeneration locks the repository's row, so that publications to
// it take turns, and returns its active generation, "" for none.
func lockActiveGeneration(ctx context.Context, tx pgx.Tx, repositoryID string) (string, error) {
	var active *string
	err := tx.QueryRow(ctx, `SELECT active_index_run_id::text FROM repositories WHERE id = $1 FOR UPDATE`, repositoryID).Scan(&active)
	if err != nil {
		return "", fmt.Errorf("store: lock generation: %w", err)
	}
	if active == nil {
		return "", nil
	}
	return *active, nil
}

// ActivateGeneration makes a full build the active generation, superseding
// whichever generation is active when it publishes and dropping its chunks.
// A full build replaces every chunk, so it does not depend on the
// generation it started from.
func ActivateGeneration(ctx context.Context, tx pgx.Tx, repositoryID, runID string) error {
	previous, err := lockActiveGeneration(ctx, tx, repositoryID)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE repositories SET active_index_run_id = $2 WHERE id = $1`, repositoryID, runID); err != nil {
		return fmt.Errorf("store: activate generation: %w", err)
	}
	if previous == "" {
		return nil
	}
	if _, err := tx.Exec(ctx, `UPDATE index_runs SET status = 'superseded', finished_at = coalesce(finished_at, now()) WHERE id = $1`,
		previous); err != nil {
		return fmt.Errorf("store: supersede generation: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM index_chunks WHERE index_run_id = $1`, previous); err != nil {
		return fmt.Errorf("store: drop superseded chunks: %w", err)
	}
	return nil
}

// AdvanceGeneration moves an incremental step's chunks into the active
// generation in place of the changed paths' chunks, and moves the
// generation to the commit the step indexed. It returns
// ErrGenerationChanged unless active is still the active generation at
// the same commit.
func AdvanceGeneration(
	ctx context.Context, tx pgx.Tx, repositoryID string, active Generation, stepRunID, commit string, changedPaths []string,
) error {
	current, err := lockActiveGeneration(ctx, tx, repositoryID)
	if err != nil {
		return err
	}
	if current != active.ID {
		return ErrGenerationChanged
	}
	// Read after acquiring the repository lock: an incremental publication
	// changes the commit without changing the active generation's ID.
	g, err := FindGeneration(ctx, tx, current)
	if errors.Is(err, ErrNotFound) {
		return ErrGenerationChanged
	}
	if err != nil {
		return err
	}
	if g.Commit != active.Commit {
		return ErrGenerationChanged
	}
	activeRunID := active.ID
	if _, err := tx.Exec(ctx, `DELETE FROM index_chunks WHERE index_run_id = $1 AND path = ANY($2)`, activeRunID, changedPaths); err != nil {
		return fmt.Errorf("store: drop stale chunks: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE index_chunks SET index_run_id = $2 WHERE index_run_id = $1`, stepRunID, activeRunID); err != nil {
		return fmt.Errorf("store: move chunks into the generation: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE index_runs SET commit_sha = $2 WHERE id = $1`, activeRunID, commit); err != nil {
		return fmt.Errorf("store: advance generation: %w", err)
	}
	return nil
}

// ClearIndexStaging deletes a runner run's staged chunks and, unless its
// run became the active generation, the chunks it embedded.
func ClearIndexStaging(ctx context.Context, tx pgx.Tx, runID, runnerRunID string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM index_staging WHERE runner_run_id = $1`, runnerRunID); err != nil {
		return fmt.Errorf("store: clear staging: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM index_chunks WHERE index_run_id = $1
		AND NOT EXISTS (SELECT 1 FROM repositories WHERE active_index_run_id = $1)`, runID); err != nil {
		return fmt.Errorf("store: drop unused chunks: %w", err)
	}
	return nil
}
