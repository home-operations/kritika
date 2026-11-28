package gateway

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/store"
	"github.com/home-operations/kritika/internal/tuning"
)

// pastFullFiles adds to hits, the vector search's depth nearest chunks of
// each query, the next nearest outside the files of which the query's hits
// hold Gateway.SimilarPerFile or more, for each query that the file cap
// would otherwise leave short: its hits fill the window, the last of them
// still qualifies, and the cap keeps fewer than target of them, the most
// chunks a query keeps. A window that is not full has nothing past it, and
// nothing past a last hit under the floor or the margin qualifies either. A
// query is searched again until it is no longer short, at most target times:
// each full window past the files the cap fills adds a chunk that the cap
// keeps, so that many fill the query whenever enough chunks qualify. The cap of one file
// across the queries and the duplicates between them can still leave a
// query short.
//
// The looks are what the hits can do without: each is in a savepoint of tx
// (inSavepoint), within what is left before deadline, and none is made once
// it passes. One that fails or runs out of time ends the looking, and
// pastFullFiles returns the hits found until then with the error, for the
// caller to answer with them unless the request itself ended.
func pastFullFiles(
	ctx context.Context, tx pgx.Tx, runID string, vectors [][]float32, exclude store.Exclusions, depth, target int,
	hits []store.SimilarHit, floor, margin float64, deadline time.Time,
) ([]store.SimilarHit, error) {
	qualifies := func(found []store.SimilarHit, last store.SimilarHit) bool {
		best := slices.MaxFunc(found, func(a, b store.SimilarHit) int { return cmp.Compare(a.Similarity, b.Similarity) })
		return closeTo(last.Similarity, best.Similarity, floor, margin)
	}
	return pastFull(hits, len(vectors), target, func(found, window []store.SimilarHit) []string {
		return fullFiles(found, window, depth, target, qualifies)
	}, func(q int, full []string) ([]store.SimilarHit, error) {
		past := store.Exclusions{Run: exclude.Run, Paths: slices.Concat(exclude.Paths, full)}
		var window []store.SimilarHit
		err := inSavepoint(ctx, tx, deadline, func(sp pgx.Tx) error {
			var err error
			window, err = store.SimilarChunks(ctx, sp, runID, vectors[q:q+1], past, depth)
			return err
		})
		return window, err
	})
}

// errSearchDue ends a search's looks past full files once its time is spent.
var errSearchDue = errors.New("gateway: the search's time is spent")

// inSavepoint runs search in a savepoint of tx, its statements bounded by
// what is left before deadline, in whole milliseconds, which is what a
// statement timeout takes. Under a millisecond left is errSearchDue, with no
// statement made, and not a timeout of 0, which would bound nothing. A
// search that fails, its timeout too, is rolled back to the savepoint, and
// leaves tx to the statements after it; a transaction that the rollback
// cannot save fails those, and its commit.
func inSavepoint(ctx context.Context, tx pgx.Tx, deadline time.Time, search func(sp pgx.Tx) error) error {
	within := max(time.Until(deadline).Truncate(time.Millisecond), 0)
	if within == 0 {
		return errSearchDue
	}
	return pgx.BeginFunc(ctx, tx, func(sp pgx.Tx) error {
		timeout := strconv.FormatInt(within.Milliseconds(), 10)
		if _, err := sp.Exec(ctx, `SELECT set_config('statement_timeout', $1, true)`, timeout); err != nil {
			return err
		}
		return search(sp)
	})
}

// pastFull adds to hits, a search's window of each of queries queries, what
// search finds for a query past the files that full names for it, given the
// query's hits so far and its last window, until full names none, at most
// target times (pastFullFiles). A search's failure ends the looking:
// pastFull returns what it found until then, with the error.
func pastFull(
	hits []store.SimilarHit, queries, target int, full func(found, window []store.SimilarHit) []string,
	search func(q int, full []string) ([]store.SimilarHit, error),
) ([]store.SimilarHit, error) {
	byQuery := make([][]store.SimilarHit, queries)
	for _, h := range hits {
		byQuery[h.Query] = append(byQuery[h.Query], h)
	}
	var err error
	for q := range byQuery {
		found := byQuery[q]
		window := found
		for range target {
			files := full(found, window)
			if files == nil {
				break
			}
			if window, err = search(q, files); err != nil {
				break
			}
			for _, h := range window {
				if !slices.ContainsFunc(found, func(f store.SimilarHit) bool {
					return f.Chunk.Path == h.Chunk.Path && f.Chunk.StartLine == h.Chunk.StartLine
				}) {
					h.Query = q
					found = append(found, h)
				}
			}
		}
		byQuery[q] = found
		if err != nil {
			break
		}
	}
	return slices.Concat(byQuery...), err
}

// fullFiles is the files of which found, a query's hits, holds
// Gateway.SimilarPerFile or more, when the file cap leaves the query short
// of target chunks and the search's last window, which found ends with,
// says that more chunks past it can qualify: it is depth long, and qualifies
// its last hit, given found. It is nil otherwise.
func fullFiles(
	found, window []store.SimilarHit, depth, target int, qualifies func(found []store.SimilarHit, last store.SimilarHit) bool,
) []string {
	if len(window) < depth || len(found) == 0 || !qualifies(found, window[len(window)-1]) {
		return nil
	}
	perFile := map[string]int{}
	for _, h := range found {
		perFile[h.Chunk.Path]++
	}
	fileCap := tuning.Get().Gateway.SimilarPerFile
	var kept int
	var full []string
	for path, n := range perFile {
		kept += min(n, fileCap)
		if n >= fileCap {
			full = append(full, path)
		}
	}
	if kept >= target {
		return nil
	}
	slices.Sort(full)
	return full
}

// closeTo reports whether a similarity is at or above floor and within
// margin of best, its query's best similarity. The tolerance keeps a hit
// right at the margin, which the subtraction's rounding can put a hair
// below it.
func closeTo(similarity, best, floor, margin float64) bool {
	return similarity >= floor && similarity >= best-margin-1e-9
}

// namedBy reports whether a lexical weight is at least Gateway.LexicalShare
// of best, its query's best lexical weight: whether its names alone vouch
// for a hit.
func namedBy(lexical, best float64) bool {
	return lexical >= tuning.Get().Gateway.LexicalShare*best
}
