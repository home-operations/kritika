package gateway

import (
	"cmp"
	"context"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/store"
)

// pastFullFiles adds to hits, the vector search's depth nearest chunks of
// each query, the next nearest outside the files of which the query's hits
// hold similarPerFile or more, for each query that the file cap would
// otherwise leave short: its hits fill the window, the last of them still
// qualifies, and the cap keeps fewer than similarPerQuery of them. A window
// that is not full has nothing past it, and nothing past a last hit under
// the floor or the margin qualifies either. A query is searched again until
// it is no longer short, at most similarPerQuery times: each full window
// past the files the cap fills adds a chunk that the cap keeps, so that
// many fill the query whenever enough chunks qualify. The cap of one file
// across the queries and the duplicates between them can still leave a
// query short.
func pastFullFiles(
	ctx context.Context, tx pgx.Tx, runID string, vectors [][]float32, exclude store.Exclusions, depth int,
	hits []store.SimilarHit, floor, margin float64,
) ([]store.SimilarHit, error) {
	byQuery := make([][]store.SimilarHit, len(vectors))
	for _, h := range hits {
		byQuery[h.Query] = append(byQuery[h.Query], h)
	}
	out := make([]store.SimilarHit, 0, len(hits))
	for q, found := range byQuery {
		window := found
		for range similarPerQuery {
			full := fullFiles(found, window, depth, floor, margin)
			if full == nil {
				break
			}
			past := store.Exclusions{Run: exclude.Run, Paths: slices.Concat(exclude.Paths, full)}
			next, err := store.SimilarChunks(ctx, tx, runID, vectors[q:q+1], past, depth)
			if err != nil {
				return nil, err
			}
			window = next
			for _, h := range next {
				if !slices.ContainsFunc(found, func(f store.SimilarHit) bool {
					return f.Chunk.Path == h.Chunk.Path && f.Chunk.StartLine == h.Chunk.StartLine
				}) {
					h.Query = q
					found = append(found, h)
				}
			}
		}
		out = append(out, found...)
	}
	return out, nil
}

// fullFiles is the files of which found, a query's hits, holds
// similarPerFile or more, when the file cap leaves the query short and the
// vector search's last window, which found ends with, says that more
// chunks past it can qualify (pastFullFiles), and nil otherwise.
func fullFiles(found, window []store.SimilarHit, depth int, floor, margin float64) []string {
	if len(window) < depth || len(found) == 0 {
		return nil
	}
	best := slices.MaxFunc(found, func(a, b store.SimilarHit) int { return cmp.Compare(a.Similarity, b.Similarity) }).Similarity
	if !closeTo(window[len(window)-1].Similarity, best, floor, margin) {
		return nil
	}
	perFile := map[string]int{}
	for _, h := range found {
		perFile[h.Chunk.Path]++
	}
	var kept int
	var full []string
	for path, n := range perFile {
		kept += min(n, similarPerFile)
		if n >= similarPerFile {
			full = append(full, path)
		}
	}
	if kept >= similarPerQuery {
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
