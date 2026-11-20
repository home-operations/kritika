package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/contextpack"
	"github.com/home-operations/kritika/internal/model"
)

// ActiveGenerationFor is the repository's active, completed index
// generation built with the given embedder, or ErrNotFound.
func ActiveGenerationFor(ctx context.Context, tx pgx.Tx, repositoryID, embedModel string, dims int, documentPrompt string) (string, error) {
	var runID string
	err := tx.QueryRow(ctx, `SELECT r.active_index_run_id::text FROM repositories r JOIN index_runs g ON g.id = r.active_index_run_id
		WHERE r.id = $1 AND g.status = 'completed' AND g.embed_model = $2 AND g.embed_dims = $3 AND g.document_prompt = $4`,
		repositoryID, embedModel, dims, documentPrompt).Scan(&runID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("store: active generation: %w", err)
	}
	return runID, nil
}

// SimilarExclude is the paths that the run reported its similar-code
// searches leave out, none before it reports them.
func SimilarExclude(ctx context.Context, tx pgx.Tx, runID string) ([]string, error) {
	var paths []string
	err := tx.QueryRow(ctx, `SELECT coalesce(similar_exclude, '{}') FROM runner_runs WHERE id = $1`, runID).Scan(&paths)
	if err != nil {
		return nil, fmt.Errorf("store: read similar exclusions: %w", err)
	}
	return paths, nil
}

// SimilarHit is one index chunk near a query, with its cosine similarity.
type SimilarHit struct {
	Chunk      contextpack.Chunk
	Similarity float64
	// Query is the index of the query vector it is near.
	Query int
	// Lexical is the weight of the query's terms that the chunk holds, and
	// Matched those terms, for a hit of LexicalChunks.
	Lexical float64
	Matched []string
}

const similarChunksSQL = `SELECT path, start_line, end_line, language, symbol, kind, scope, text, 1 - (embedding <=> $1::halfvec)
			FROM index_chunks WHERE index_run_id = $2 AND NOT (path = ANY($3))
			ORDER BY embedding <=> $1::halfvec LIMIT $4`

// SimilarChunks finds, for each query vector, the perQuery chunks of the
// generation nearest it, outside the excluded paths, in the order the
// index ranks them. The generation and account filters are strict and
// cheap, which is what VectorChord's prefilter wants: it then skips the
// distance of every chunk outside this repository's generation.
func SimilarChunks(
	ctx context.Context, tx pgx.Tx, runID string, vectors [][]float32, exclude []string, perQuery int,
) ([]SimilarHit, error) {
	if exclude == nil {
		exclude = []string{}
	}
	if _, err := tx.Exec(ctx, `SET LOCAL vchordrq.prefilter = on`); err != nil {
		return nil, fmt.Errorf("store: enable prefilter: %w", err)
	}
	var hits []SimilarHit
	for i, v := range vectors {
		rows, err := tx.Query(ctx, similarChunksSQL, model.VectorLiteral(v), runID, exclude, perQuery)
		if err != nil {
			return nil, fmt.Errorf("store: similar chunks: %w", err)
		}
		hits, err = pgx.AppendRows(hits, rows, func(row pgx.CollectableRow) (SimilarHit, error) {
			h := SimilarHit{Query: i}
			c := &h.Chunk
			err := row.Scan(&c.Path, &c.StartLine, &c.EndLine, &c.Language, &c.Symbol, &c.Kind, &c.Scope, &c.Text, &h.Similarity)
			c.Stage = contextpack.StageSimilar
			return h, err
		})
		if err != nil {
			return nil, fmt.Errorf("store: similar chunks: %w", err)
		}
	}
	return hits, nil
}

// LexicalQuery is one query of a lexical search: its terms, lexemes of
// package lexical, and its vector, which measures each hit's similarity.
type LexicalQuery struct {
	Terms  []string
	Vector []float32
}

// lexicalChunksSQL reads each chunk of the generation once, for every query
// of a request together. Under row-level security no GIN index of the
// lexemes could serve a search: @@ is not leakproof, so Postgres applies it
// only to the rows that the policy has let through, never as an index
// condition, and each query would read the whole generation anyway.
//
// The terms are joined rather than compared with = ANY: once a cached
// statement runs on a generic plan, = ANY of a parameter checks each of the
// generation's lexemes against every term in turn. The ranked chunks are
// read again by the generation's key, one at a time, rather than by a join
// that the planner, which cannot tell that few are ranked, makes over the
// whole partition.
//
// $1 is the generation, $2 and $3 each query's index and term, $4 every
// term once, $5 the generation's chunks and $6 the count at which a term is
// too common to search for, $7 the excluded paths, $8 each query's vector
// and $9 the hits each query keeps.
const lexicalChunksSQL = `WITH held AS MATERIALIZED (
		SELECT c.id, c.path, c.start_line, l
		FROM index_chunks c, unnest(tsvector_to_array(c.lexemes)) AS l
		WHERE c.index_run_id = $1 AND l IN (SELECT unnest($4::text[]))
	), weights AS (
		SELECT q.qi, q.t, ln(1 + ($5::float8 - f.n + 0.5) / (f.n + 0.5)) AS w
		FROM unnest($2::int[], $3::text[]) AS q (qi, t)
		JOIN (SELECT l, count(*) AS n FROM held GROUP BY l) f ON f.l = q.t
		WHERE f.n < $6
	), ranked AS (
		SELECT w.qi, h.id, sum(w.w) AS score, array_agg(w.t ORDER BY w.w DESC, w.t) AS matched,
			row_number() OVER (PARTITION BY w.qi ORDER BY sum(w.w) DESC, h.path, h.start_line) AS rank
		FROM held h JOIN weights w ON w.t = h.l
		WHERE NOT (h.path = ANY($7::text[]))
		GROUP BY w.qi, h.id, h.path, h.start_line
	), top AS MATERIALIZED (
		SELECT * FROM ranked WHERE rank <= $9
	)
	SELECT r.qi, c.path, c.start_line, c.end_line, c.language, c.symbol, c.kind, c.scope, c.text,
		1 - (c.embedding <=> (($8::text[])[r.qi + 1])::halfvec), r.score, r.matched
	FROM top r, LATERAL (SELECT * FROM index_chunks c WHERE c.index_run_id = $1 AND c.id = r.id) c
	ORDER BY r.qi, r.rank`

// LexicalChunks finds, for each query, the perQuery chunks of the generation
// that hold the most weight of its terms, outside the excluded paths, with
// the terms each holds, the heaviest first. A term weighs what BM25 gives a
// term in so many of the generation's chunks: one that few hold counts for
// more than one that many do. Postgres's own ranking knows nothing of the
// other chunks. A term in common(chunks) or more of the generation's chunks
// is left out as too common to tell them apart.
func LexicalChunks(
	ctx context.Context, tx pgx.Tx, runID string, queries []LexicalQuery, exclude []string, perQuery int, common func(chunks int) int,
) ([]SimilarHit, error) {
	if exclude == nil {
		exclude = []string{}
	}
	var qis []int32
	var terms, all []string
	vectors := make([]string, 0, len(queries))
	seen := map[string]bool{}
	for i, q := range queries {
		vectors = append(vectors, model.VectorLiteral(q.Vector))
		for _, t := range q.Terms {
			qis, terms = append(qis, int32(i)), append(terms, t)
			if !seen[t] {
				seen[t] = true
				all = append(all, t)
			}
		}
	}
	if len(terms) == 0 {
		return nil, nil
	}
	var chunks int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM index_chunks WHERE index_run_id = $1`, runID).Scan(&chunks); err != nil {
		return nil, fmt.Errorf("store: count index chunks: %w", err)
	}
	rows, err := tx.Query(ctx, lexicalChunksSQL, runID, qis, terms, all, chunks, common(chunks), exclude, vectors, perQuery)
	if err != nil {
		return nil, fmt.Errorf("store: lexical chunks: %w", err)
	}
	hits, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (SimilarHit, error) {
		var h SimilarHit
		c := &h.Chunk
		err := row.Scan(&h.Query, &c.Path, &c.StartLine, &c.EndLine, &c.Language, &c.Symbol, &c.Kind, &c.Scope, &c.Text,
			&h.Similarity, &h.Lexical, &h.Matched)
		c.Stage = contextpack.StageSimilar
		return h, err
	})
	if err != nil {
		return nil, fmt.Errorf("store: lexical chunks: %w", err)
	}
	return hits, nil
}
