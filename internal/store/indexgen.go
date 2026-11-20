package store

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/lexical"
	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/runspec"
)

// Index modes, as index_runs and index_packs spell them.
const (
	IndexModeFull        = runspec.IndexModeFull
	IndexModeIncremental = runspec.IndexModeIncremental
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

// Generation is a repository's active index generation: what it covers,
// the embedder it was built with, and the index format (runspec.IndexFormat)
// of the build that built it. Mixed says that an incremental step of another
// format advanced it, so it holds chunks of both.
type Generation struct {
	ID, Commit, Model, IgnoreHash, DocumentPrompt string
	Dims, Format                                  int
	Mixed                                         bool
}

// steppedInAnotherFormat holds for a generation, g, that an incremental step
// of another index format advanced. A step reads the active generation
// before its run starts, and a step whose generation changed before it
// published is superseded. So a step run started after g was published,
// which completed, advanced g. A build from before index formats records
// its runs as format 1, and during a rollout it steps the generations that
// the new build publishes.
const steppedInAnotherFormat = `EXISTS (SELECT 1 FROM index_runs st WHERE st.repository_id = g.repository_id
	AND st.mode = 'incremental' AND st.status = 'completed' AND st.index_format <> g.index_format AND st.created_at > g.finished_at)`

// FindGeneration reads a completed index generation, or ErrNotFound.
func FindGeneration(ctx context.Context, tx pgx.Tx, runID string) (Generation, error) {
	g := Generation{ID: runID}
	err := tx.QueryRow(ctx, `SELECT commit_sha, embed_model, embed_dims, ignore_hash, document_prompt, index_format,
		`+steppedInAnotherFormat+` FROM index_runs g WHERE id = $1 AND status = 'completed'`, runID).
		Scan(&g.Commit, &g.Model, &g.Dims, &g.IgnoreHash, &g.DocumentPrompt, &g.Format, &g.Mixed)
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
	// before its chunks are dropped. They count as left behind by a job that
	// failed to clear them.
	StrayAfter time.Duration
}

// StartIndexRun records a running index run, of this build's index format,
// and its runner run and returns their ids. Checkpointed runs are left to SweepIndexProgress, which
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
		 ignore_hash, resume_key, index_format)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'running', $9, $10, $11, $12) RETURNING id`,
		r.AccountID, r.RepositoryID, r.Commit, r.Base, r.Embedding.Model, r.Embedding.Dims, r.Embedding.DocumentPrompt, r.Mode, r.Trigger,
		r.IgnoreHash, r.ResumeKey, runspec.IndexFormat).
		Scan(&runID); err != nil {
		return "", "", fmt.Errorf("store: insert index run: %w", err)
	}
	runnerRunID, err = InsertRunnerRun(ctx, tx, r.AccountID, RunnerKindIndex, runID, r.JobID)
	return runID, runnerRunID, err
}

// FinishIndexRun ends an index run as status. Checkpoints own the chunk count
// until successful publication. Failure bookkeeping cannot overwrite it.
// Publication requires a running run. Failure cleanup leaves terminal runs
// unchanged, so a lost COMMIT acknowledgment cannot overwrite a published run.
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

// IndexPack is what an index runner wrote.
type IndexPack = runspec.IndexPack

// ReadIndexPack reads the index pack that a runner run wrote and records
// its mode, and whether it was truncated, on the run. The runner can fall
// back to a full build.
func ReadIndexPack(ctx context.Context, tx pgx.Tx, runID, runnerRunID string) (IndexPack, error) {
	var p IndexPack
	if err := tx.QueryRow(ctx, `SELECT mode, base_sha, changed_paths, truncated FROM index_packs WHERE runner_run_id = $1`, runnerRunID).
		Scan(&p.Mode, &p.Base, &p.ChangedPaths, &p.Truncated); err != nil {
		return p, fmt.Errorf("store: read index pack: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE index_runs SET mode = $2, base_sha = $3, truncated = $4 WHERE id = $1`,
		runID, p.Mode, p.Base, p.Truncated); err != nil {
		return p, fmt.Errorf("store: record index mode: %w", err)
	}
	return p, nil
}

// StagedChunk is one chunk an index runner staged, before embedding.
type StagedChunk struct {
	ID                                        int64
	Path, Language, Symbol, Kind, Scope, Text string
	StartLine, EndLine                        int
	// Signature is the line that names the declaration of a window that
	// does not show it. Only the embedded text holds it.
	Signature string
	// ContentHash identifies the vector that the worker embeds for the
	// chunk, which it sets before InsertIndexChunks. IndexVectors finds a
	// vector by it. Nil records none.
	ContentHash []byte
}

// ReadStagedChunks reads up to limit of a runner run's staged chunks with
// an id past after, in id order.
func ReadStagedChunks(ctx context.Context, tx pgx.Tx, runnerRunID string, after int64, limit int) ([]StagedChunk, error) {
	rows, err := tx.Query(ctx, `SELECT id, path, start_line, end_line, language, symbol, kind, scope, signature, text
		FROM index_staging WHERE runner_run_id = $1 AND id > $2 ORDER BY id LIMIT $3`, runnerRunID, after, limit)
	if err != nil {
		return nil, fmt.Errorf("store: read staged chunks: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (StagedChunk, error) {
		var c StagedChunk
		err := row.Scan(&c.ID, &c.Path, &c.StartLine, &c.EndLine, &c.Language, &c.Symbol, &c.Kind, &c.Scope, &c.Signature, &c.Text)
		return c, err
	})
	if err != nil {
		return nil, fmt.Errorf("store: read staged chunks: %w", err)
	}
	return out, nil
}

// InsertIndexChunks records a batch of embedded chunks under the run, each
// with its lexemes (package lexical) for the lexical search.
func InsertIndexChunks(
	ctx context.Context, tx pgx.Tx, accountID, repositoryID, runID string, batch []StagedChunk, vectors [][]float32,
) error {
	if len(vectors) != len(batch) {
		return fmt.Errorf("store: %d vectors for %d chunks", len(vectors), len(batch))
	}
	n := len(batch)
	strs := func() []string { return make([]string, n) }
	paths, langs, symbols, kinds, scopes, texts, embeddings, lexemes := strs(), strs(), strs(), strs(), strs(), strs(), strs(), strs()
	starts, ends := make([]int32, n), make([]int32, n)
	hashes := make([][]byte, n)
	for i, c := range batch {
		paths[i], langs[i], symbols[i], kinds[i], scopes[i], texts[i] = c.Path, c.Language, c.Symbol, c.Kind, c.Scope, c.Text
		starts[i], ends[i] = int32(c.StartLine), int32(c.EndLine)
		embeddings[i], hashes[i] = model.VectorLiteral(vectors[i]), c.ContentHash
		// A lexeme holds no space, so one string carries a chunk's lexemes
		// through unnest, which flattens an array of arrays.
		lexemes[i] = strings.Join(lexical.Document(c.Path, c.Scope, c.Symbol, c.Text), " ")
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO index_chunks
			(account_id, repository_id, index_run_id, path, start_line, end_line, language, symbol, kind, scope, text, embedding,
			 content_hash, lexemes)
		SELECT $1, $2, $3, c.path, c.start_line, c.end_line, c.language, c.symbol, c.kind, c.scope, c.text, c.embedding::halfvec,
			c.content_hash, array_to_tsvector(string_to_array(c.lexemes, ' '))
		FROM unnest($4::text[], $5::int[], $6::int[], $7::text[], $8::text[], $9::text[], $10::text[], $11::text[], $12::text[],
			$13::bytea[], $14::text[])
			AS c (path, start_line, end_line, language, symbol, kind, scope, text, embedding, content_hash, lexemes)`,
		accountID, repositoryID, runID, paths, starts, ends, langs, symbols, kinds, scopes, texts, embeddings, hashes,
		lexemes); err != nil {
		return fmt.Errorf("store: insert chunks: %w", err)
	}
	return nil
}

// indexVectorsSQL finds the vectors of IndexVectors. Naming the runs, rather
// than the repository alone, reads their hash partitions alone.
const indexVectorsSQL = `SELECT DISTINCT ON (content_hash) content_hash, embedding::real[] FROM index_chunks
	WHERE repository_id = $1 AND index_run_id IN ($2, $3) AND content_hash = ANY($4::bytea[]) AND content_hash IS NOT NULL`

// IndexVectors returns, by content hash, a vector that the index chunks of
// run, the run being built, or of active, the repository's active generation
// ("" for none), hold for each of hashes that any holds. The hash covers the
// text and the embedder, and index_chunks holds the vectors of one embedder
// alone. A run that a build left behind is not read, so that the lookup
// reads two partitions rather than every one. The vectors are as halfvec
// stores them, which an insert stores again unchanged.
func IndexVectors(ctx context.Context, tx pgx.Tx, repositoryID, run, active string, hashes [][]byte) (map[string][]float32, error) {
	rows, err := tx.Query(ctx, indexVectorsSQL, repositoryID, run, cmp.Or(active, run), hashes)
	if err != nil {
		return nil, fmt.Errorf("store: find index vectors: %w", err)
	}
	found := map[string][]float32{}
	var hash []byte
	var vector []float32
	if _, err := pgx.ForEachRow(rows, []any{&hash, &vector}, func() error {
		found[string(hash)] = vector
		return nil
	}); err != nil {
		return nil, fmt.Errorf("store: find index vectors: %w", err)
	}
	return found, nil
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

// ActivateGeneration makes a full build the active generation. It supersedes
// whichever generation is active when it publishes, and it drops the chunks
// of that generation. A full build replaces every chunk, so it does not
// depend on the generation it started from.
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
// generation in place of the changed paths' chunks. It also moves the
// generation to the commit that the step indexed. It returns
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
