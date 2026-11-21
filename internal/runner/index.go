package runner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/go-git/go-git/v6/plumbing/object"

	"github.com/home-operations/kritika/internal/gitfetch"
	"github.com/home-operations/kritika/internal/indexer"
	"github.com/home-operations/kritika/internal/runspec"
)

// runIndex fetches the commit to index and stages its chunks. With Base
// set, it tries an incremental step from that commit first. When the remote
// does not serve the base, it falls back to a full build and says so in the
// pack: the base is unreachable after a force push, or older than the server
// will serve by SHA, and no retry changes that. Any other failure to fetch
// fails the run, and the worker tries the job again.
func runIndex(ctx context.Context, report *reporter, p runspec.Spec, secrets runspec.Secrets, logger *slog.Logger) error {
	if err := report.phase(ctx, "fetching"); err != nil {
		return err
	}
	res, mode, err := fetchForIndex(ctx, p, secrets.GitToken, gitfetch.Trees, logger)
	if err != nil {
		return err
	}
	defer func() { _ = res.Close() }()

	if err := report.phase(ctx, "parsing"); err != nil {
		return err
	}
	headTree, err := res.Head.Tree()
	if err != nil {
		return fmt.Errorf("runner: head tree: %w", err)
	}
	var baseTree *object.Tree
	if mode == runspec.IndexModeIncremental {
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
	if stats.Truncated {
		logger.Warn("index truncated at the indexer's budget: the files past it are left out", "files", stats.Files, "bytes", stats.Bytes,
			"chunks", len(chunks))
	}

	if err := report.phase(ctx, "writing"); err != nil {
		return err
	}
	if changed == nil {
		changed = []string{}
	}
	staged := make([]runspec.IndexChunk, len(chunks))
	for n, c := range chunks {
		staged[n] = runspec.IndexChunk{Path: c.Path, StartLine: c.StartLine, EndLine: c.EndLine,
			Language: c.Language, Symbol: c.Symbol, Kind: c.Kind, Scope: c.Scope, Signature: c.Signature, Text: c.Text}
	}
	err = report.index(ctx, runspec.IndexPack{
		Base: baseFor(mode, p.Base), Mode: mode, ChangedPaths: changed, Truncated: stats.Truncated,
	}, staged)
	if err != nil {
		return err
	}
	logger.Info("index pack written", "run", p.RunID, "chunks", len(chunks))
	return nil
}

func buildIndex(
	ctx context.Context, head, base *object.Tree, ignore []string, opts indexer.Options, logger *slog.Logger,
) ([]indexer.Chunk, []string, indexer.Stats, string, error) {
	mode := runspec.IndexModeFull
	if base != nil {
		mode = runspec.IndexModeIncremental
	}
	chunks, changed, stats, err := indexer.Build(ctx, head, base, ignore, opts)
	if errors.Is(err, indexer.ErrBudgetReached) {
		logger.Warn("incremental build over budget, building the index in full", "files", stats.Files, "bytes", stats.Bytes)
		mode = runspec.IndexModeFull
		chunks, changed, stats, err = indexer.Build(ctx, head, nil, ignore, opts)
	}
	return chunks, changed, stats, mode, err
}

// fetchForIndex fetches head, and base for an incremental step, with trees.
// A refusal that names the base, or names no commit, falls back to a full
// build: a remote that does not say which commit it refused would otherwise
// fail every incremental step, while a head it refused fails the full build's
// fetch as well, one fetch later. A refusal that names the head, and any
// other failure, is returned, so a fetch cut short is tried again rather
// than the whole repository embedded anew.
func fetchForIndex(
	ctx context.Context, p runspec.Spec, token string, trees func(context.Context, gitfetch.Fetch) (*gitfetch.Result, error),
	logger *slog.Logger,
) (*gitfetch.Result, string, error) {
	if p.Base != "" {
		res, err := trees(ctx, gitfetch.Fetch{CloneURL: p.CloneURL, Token: token, Head: p.Head, Base: p.Base})
		if err == nil {
			return res, runspec.IndexModeIncremental, nil
		}
		if e, ok := errors.AsType[*gitfetch.UnavailableError](err); !ok || e.SHA != "" && e.SHA != p.Base {
			return nil, "", err
		}
		logger.Warn("the remote refused the base or an unnamed commit, building the index in full", "base", p.Base[:7], "error", err)
	}
	res, err := trees(ctx, gitfetch.Fetch{CloneURL: p.CloneURL, Token: token, Head: p.Head})
	if err != nil {
		return nil, "", err
	}
	return res, runspec.IndexModeFull, nil
}

func baseFor(mode, base string) string {
	if mode == runspec.IndexModeFull {
		return ""
	}
	return base
}
