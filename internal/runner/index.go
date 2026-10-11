package runner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/gitfetch"
	"github.com/home-operations/kritika/internal/indexer"
	"github.com/home-operations/kritika/internal/store"
)

// stagingBatch is how many staged chunks one INSERT carries.
const stagingBatch = 500

// runIndex fetches the commit to index and stages its chunks. With Base
// set it tries an incremental step from that commit first; if the base
// cannot be fetched (unreachable after a force push, or older than the
// server will serve by SHA) it falls back to a full build and says so in
// the pack, so the worker never has to retry the Job.
func runIndex(ctx context.Context, st *store.Store, p Spec, secrets Secrets, logger *slog.Logger) error {
	if err := setPhase(ctx, st, p.RunID, "fetching"); err != nil {
		return err
	}
	res, mode, err := fetchForIndex(ctx, p, secrets.GitToken, logger)
	if err != nil {
		return err
	}
	defer func() { _ = res.Close() }()

	if err := setPhase(ctx, st, p.RunID, "parsing"); err != nil {
		return err
	}
	headTree, err := res.Head.Tree()
	if err != nil {
		return fmt.Errorf("runner: head tree: %w", err)
	}
	var baseTree *object.Tree
	if mode == "incremental" {
		if baseTree, err = res.Base.Tree(); err != nil {
			return fmt.Errorf("runner: base tree: %w", err)
		}
	}
	chunks, changed, stats, mode, err := buildIndex(ctx, headTree, baseTree, p.Ignore, indexer.DefaultOptions, logger)
	if err != nil {
		return err
	}
	logger.Info("chunked", "mode", mode, "chunks", len(chunks), "changed_paths", len(changed), "files", stats.Files,
		"parsed", stats.Parsed, "skipped", stats.Skipped, "truncated", stats.Truncated, "elapsed", stats.Elapsed.Round(time.Millisecond))

	if err := setPhase(ctx, st, p.RunID, "writing"); err != nil {
		return err
	}
	if changed == nil {
		changed = []string{}
	}
	err = st.WithRunnerJob(ctx, p.RunID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO index_packs (runner_run_id, account_id, base_sha, mode, changed_paths, chunk_count)
			SELECT id, account_id, $2, $3, $4, $5 FROM runner_runs WHERE id = $1`,
			p.RunID, baseFor(mode, p.Base), mode, changed, len(chunks)); err != nil {
			return fmt.Errorf("runner: write index pack: %w", err)
		}
		for batch := range slices.Chunk(chunks, stagingBatch) {
			if err := stage(ctx, tx, p.RunID, batch); err != nil {
				return err
			}
		}
		return setPhaseTx(ctx, tx, p.RunID, "done")
	})
	if err != nil {
		return err
	}
	logger.Info("index pack written", "run", p.RunID, "chunks", len(chunks))
	return nil
}

func buildIndex(
	ctx context.Context, head, base *object.Tree, ignore []string, opts indexer.Options, logger *slog.Logger,
) ([]indexer.Chunk, []string, indexer.Stats, string, error) {
	mode := store.IndexModeFull
	if base != nil {
		mode = store.IndexModeIncremental
	}
	chunks, changed, stats, err := indexer.Build(ctx, head, base, ignore, opts)
	if errors.Is(err, indexer.ErrBudgetReached) {
		logger.Warn("incremental build over budget, building the index in full", "files", stats.Files, "bytes", stats.Bytes)
		mode = store.IndexModeFull
		chunks, changed, stats, err = indexer.Build(ctx, head, nil, ignore, opts)
	}
	return chunks, changed, stats, mode, err
}

func fetchForIndex(ctx context.Context, p Spec, token string, logger *slog.Logger) (*gitfetch.Result, string, error) {
	if p.Base != "" {
		res, err := gitfetch.Trees(ctx, gitfetch.Fetch{CloneURL: p.CloneURL, Token: token, Head: p.Head, Base: p.Base})
		if err == nil {
			return res, "incremental", nil
		}
		if ctx.Err() != nil {
			return nil, "", err
		}
		logger.Warn("incremental fetch failed, building the index in full", "base", p.Base[:7], "error", err)
	}
	res, err := gitfetch.Trees(ctx, gitfetch.Fetch{CloneURL: p.CloneURL, Token: token, Head: p.Head})
	if err != nil {
		return nil, "", err
	}
	return res, "full", nil
}

func baseFor(mode, base string) string {
	if mode == "full" {
		return ""
	}
	return base
}

// stage inserts one batch of chunks under the run, copying account_id from
// the run row the same way context packs do.
func stage(ctx context.Context, tx pgx.Tx, runID string, chunks []indexer.Chunk) error {
	n := len(chunks)
	paths, langs, symbols := make([]string, n), make([]string, n), make([]string, n)
	kinds, scopes, texts := make([]string, n), make([]string, n), make([]string, n)
	starts, ends := make([]int32, n), make([]int32, n)
	for i, c := range chunks {
		paths[i], starts[i], ends[i] = c.Path, int32(c.StartLine), int32(c.EndLine)
		langs[i], symbols[i], kinds[i], scopes[i], texts[i] = c.Language, c.Symbol, c.Kind, c.Scope, c.Text
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO index_staging (runner_run_id, account_id, path, start_line, end_line, language, symbol, kind, scope, text)
		SELECT r.id, r.account_id, c.path, c.start_line, c.end_line, c.language, c.symbol, c.kind, c.scope, c.text
		FROM runner_runs r, unnest($2::text[], $3::int[], $4::int[], $5::text[], $6::text[], $7::text[], $8::text[], $9::text[])
			AS c (path, start_line, end_line, language, symbol, kind, scope, text)
		WHERE r.id = $1`, runID, paths, starts, ends, langs, symbols, kinds, scopes, texts)
	if err != nil {
		return fmt.Errorf("runner: stage chunks: %w", err)
	}
	return nil
}
