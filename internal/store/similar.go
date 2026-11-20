package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

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

// TermCounts counts, for each term, the chunks of the generation that hold
// it, up to limit. A count at limit says that the term is too common to
// tell chunks apart, and limit bounds what counting a common one costs. It
// also counts the generation's chunks.
func TermCounts(ctx context.Context, tx pgx.Tx, runID string, terms []string, limit int) (map[string]int, int, error) {
	var chunks int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM index_chunks WHERE index_run_id = $1`, runID).Scan(&chunks); err != nil {
		return nil, 0, fmt.Errorf("store: count index chunks: %w", err)
	}
	rows, err := tx.Query(ctx, `SELECT q.t, (SELECT count(*) FROM (SELECT 1 FROM index_chunks
			WHERE index_run_id = $1 AND lexemes @@ q.q LIMIT $3) s)
		FROM unnest($2::text[], $4::text[]::tsquery[]) AS q (t, q)`, runID, terms, limit, tsLexemes(terms))
	if err != nil {
		return nil, 0, fmt.Errorf("store: count terms: %w", err)
	}
	counts := map[string]int{}
	var term string
	var n int
	if _, err := pgx.ForEachRow(rows, []any{&term, &n}, func() error {
		counts[term] = n
		return nil
	}); err != nil {
		return nil, 0, fmt.Errorf("store: count terms: %w", err)
	}
	return counts, chunks, nil
}

// LexicalQuery is one query of a lexical search: its terms, lexemes of
// package lexical, with their weights, and its vector, which measures each
// hit's similarity.
type LexicalQuery struct {
	Terms   []string
	Weights []float64
	Vector  []float32
}

const lexicalChunksSQL = `WITH top AS (
		SELECT c.id, m.score, m.matched
		FROM index_chunks c,
		LATERAL (SELECT sum(q.w) AS score, array_agg(q.t ORDER BY q.i) AS matched
			FROM unnest($4::text[], $5::float8[], $6::text[]::tsquery[]) WITH ORDINALITY AS q (t, w, q, i) WHERE c.lexemes @@ q.q) m
		WHERE c.index_run_id = $2 AND NOT (c.path = ANY($3)) AND c.lexemes @@ $7::tsquery
		ORDER BY m.score DESC, c.path, c.start_line LIMIT $8
	)
	SELECT c.path, c.start_line, c.end_line, c.language, c.symbol, c.kind, c.scope, c.text,
		1 - (c.embedding <=> $1::halfvec), top.score, top.matched
	FROM top JOIN index_chunks c ON c.id = top.id
	ORDER BY top.score DESC, c.path, c.start_line`

// LexicalChunks finds, for each query, the perQuery chunks of the generation
// that hold the most weight of its terms, outside the excluded paths. It
// matches whole lexemes alone: the weights, which the caller derives from
// how many chunks hold each term, rank the chunks, as Postgres's own ranking
// knows nothing of the other chunks. A query without terms finds nothing.
func LexicalChunks(
	ctx context.Context, tx pgx.Tx, runID string, queries []LexicalQuery, exclude []string, perQuery int,
) ([]SimilarHit, error) {
	if exclude == nil {
		exclude = []string{}
	}
	var hits []SimilarHit
	for i, q := range queries {
		if len(q.Terms) == 0 {
			continue
		}
		if len(q.Weights) != len(q.Terms) {
			return nil, fmt.Errorf("store: %d weights for %d terms", len(q.Weights), len(q.Terms))
		}
		lexemes := tsLexemes(q.Terms)
		rows, err := tx.Query(ctx, lexicalChunksSQL, model.VectorLiteral(q.Vector), runID, exclude, q.Terms, q.Weights, lexemes,
			strings.Join(lexemes, " | "), perQuery)
		if err != nil {
			return nil, fmt.Errorf("store: lexical chunks: %w", err)
		}
		hits, err = pgx.AppendRows(hits, rows, func(row pgx.CollectableRow) (SimilarHit, error) {
			h := SimilarHit{Query: i}
			c := &h.Chunk
			err := row.Scan(&c.Path, &c.StartLine, &c.EndLine, &c.Language, &c.Symbol, &c.Kind, &c.Scope, &c.Text, &h.Similarity,
				&h.Lexical, &h.Matched)
			c.Stage = contextpack.StageSimilar
			return h, err
		})
		if err != nil {
			return nil, fmt.Errorf("store: lexical chunks: %w", err)
		}
	}
	return hits, nil
}

// tsLexemes are the tsquery texts of terms, each of the one lexeme. A
// lexeme of package lexical is letters and digits alone, which quoting
// needs no escaping for.
func tsLexemes(terms []string) []string {
	out := make([]string, len(terms))
	for i, t := range terms {
		out[i] = "'" + t + "'"
	}
	return out
}
