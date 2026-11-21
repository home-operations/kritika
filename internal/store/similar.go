package store

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/contextpack"
	"github.com/home-operations/kritika/internal/model"
)

// postingsIndexFormat is the first index format (runspec.IndexFormat) whose
// builds write their generation's postings (index_postings).
const postingsIndexFormat = 2

// LexicalCommonMax is the highest count at which a word is too common to
// search by name: a word in this many of a generation's chunks or more
// never is, however large the generation. index_postings lists the chunks
// of a word in fewer, and keeps the count alone of one in more.
const LexicalCommonMax = 200

// ActiveGenerationFor is the repository's active, completed index
// generation built with the given embedder, or ErrNotFound. lexical says
// that each of its chunks has its postings, for a search by name: a build of
// postingsIndexFormat or later built it, and no step of another format
// advanced it. One that does not is rebuilt by the onboarding feeder.
func ActiveGenerationFor(
	ctx context.Context, tx pgx.Tx, repositoryID, embedModel string, dims int, documentPrompt string,
) (runID string, lexical bool, err error) {
	err = tx.QueryRow(ctx, `SELECT r.active_index_run_id::text, g.index_format >= $5 AND NOT `+steppedInAnotherFormat+`
		FROM repositories r JOIN index_runs g ON g.id = r.active_index_run_id
		WHERE r.id = $1 AND g.status = 'completed' AND g.embed_model = $2 AND g.embed_dims = $3 AND g.document_prompt = $4`,
		repositoryID, embedModel, dims, documentPrompt, postingsIndexFormat).Scan(&runID, &lexical)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, ErrNotFound
	}
	if err != nil {
		return "", false, fmt.Errorf("store: active generation: %w", err)
	}
	return runID, lexical, nil
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

// LexicalQuery is one query of a lexical search: its terms and its vector,
// which measures each hit's similarity.
type LexicalQuery struct {
	Terms  []LexicalTerm
	Vector []float32
}

// LexicalTerm is a term of a LexicalQuery: a lexeme of package lexical, and
// whether the query writes it as an identifier of several parts, whole.
type LexicalTerm struct {
	Lexeme   string
	Compound bool
}

// lexicalChunksSQL reads, for each term of every query of a request
// together, the generation's posting of it: how many chunks hold it, its
// document frequency, and, unless that is the count at which it is too
// common to search for, which. Only the excluded paths' chunks and the few
// ranked chunks are read from index_chunks. A score is rounded so that two
// chunks holding the same terms tie, whatever order they were added in.
//
// $1 is the generation, $2, $3 and $4 each query's index, term and whether
// the term is compound, $5 every term once, $6 the generation's chunks and
// $7 the count at which a term is too common to search for, $8 the
// excluded paths, $9 each query's vector, $10 the hits each query keeps,
// $11 the most of them read when they tie and $12 the most chunks a term
// can be in and still find a chunk alone.
const lexicalChunksSQL = `WITH held AS MATERIALIZED (
		SELECT t.t, x.chunk_id, p.chunks AS n
		FROM unnest($5::text[]) AS t (t)
		JOIN index_postings p ON p.index_run_id = $1 AND p.lexeme = t.t AND p.chunks < $7,
			unnest(p.chunk_ids) AS x (chunk_id)
	), weights AS (
		SELECT q.qi, q.t, q.compound, f.n, ln(1 + ($6::float8 - f.n + 0.5) / (f.n + 0.5)) AS w
		FROM unnest($2::int[], $3::text[], $4::bool[]) AS q (qi, t, compound)
		JOIN (SELECT DISTINCT t, n FROM held) f ON f.t = q.t
	), excluded AS MATERIALIZED (
		SELECT id FROM index_chunks WHERE index_run_id = $1 AND path = ANY($8::text[])
	), scored AS (
		SELECT w.qi, h.chunk_id AS id, round(sum(w.w)::numeric, 9)::float8 AS score,
			array_agg(w.t ORDER BY w.w DESC, w.t) AS matched
		FROM held h JOIN weights w ON w.t = h.t
		WHERE h.chunk_id NOT IN (SELECT id FROM excluded)
		GROUP BY w.qi, h.chunk_id
		HAVING count(*) > 1 OR bool_or(w.compound) OR min(w.n) <= $12
	), top AS MATERIALIZED (
		SELECT * FROM (
			SELECT s.*, rank() OVER (PARTITION BY s.qi ORDER BY s.score DESC) AS rank,
				row_number() OVER (PARTITION BY s.qi ORDER BY s.score DESC, s.id) AS place
			FROM scored s
		) r WHERE rank <= $10 AND place <= $11
	)
	SELECT r.qi, c.path, c.start_line, c.end_line, c.language, c.symbol, c.kind, c.scope, c.text,
		1 - (c.embedding <=> (($9::text[])[r.qi + 1])::halfvec) AS similarity, r.score, r.matched
	FROM top r, LATERAL (SELECT * FROM index_chunks c WHERE c.index_run_id = $1 AND c.id = r.id LIMIT 1) c
	ORDER BY r.qi, r.score DESC, similarity DESC, c.path, c.start_line`

// lexicalTies is how many times perQuery LexicalChunks reads of a query's
// hits when they tie at its cut.
const lexicalTies = 4

// LexicalChunks finds, for each query, the perQuery chunks of the generation
// that hold the most weight of its terms, outside the excluded paths, with
// the terms each holds, the heaviest first. A term weighs what BM25 gives a
// term in so many of the generation's chunks: one that few hold counts for
// more than one that many do. A term in common(chunks) or more of the
// generation's chunks, or in LexicalCommonMax or more, is left out as too
// common to tell them apart.
//
// A chunk is found when it holds two of a query's terms, a compound one, or
// one in at most alone of the generation's chunks. So a query's one common
// word does not find every chunk that holds it. Of chunks of equal weight,
// the one nearest the query's vector comes first, then by path and line.
func LexicalChunks(
	ctx context.Context, tx pgx.Tx, runID string, queries []LexicalQuery, exclude []string, perQuery int,
	common func(chunks int) int, alone int,
) ([]SimilarHit, error) {
	return lexicalChunks(ctx, tx, lexicalChunksSQL, runID, queries, exclude, perQuery, common, alone)
}

// lexicalChunks is LexicalChunks by query, a test's or lexicalChunksSQL.
func lexicalChunks(
	ctx context.Context, tx pgx.Tx, query, runID string, queries []LexicalQuery, exclude []string, perQuery int,
	common func(chunks int) int, alone int,
) ([]SimilarHit, error) {
	if exclude == nil {
		exclude = []string{}
	}
	var qis []int32
	var terms, all []string
	var compound []bool
	vectors := make([]string, 0, len(queries))
	seen := map[string]bool{}
	for i, q := range queries {
		vectors = append(vectors, model.VectorLiteral(q.Vector))
		for _, t := range q.Terms {
			qis, terms, compound = append(qis, int32(i)), append(terms, t.Lexeme), append(compound, t.Compound)
			if !seen[t.Lexeme] {
				seen[t.Lexeme] = true
				all = append(all, t.Lexeme)
			}
		}
	}
	if len(terms) == 0 {
		return nil, nil
	}
	// A generation that no publication of this build counted, which only a
	// test searches, is counted here: COALESCE reads its second argument only
	// when the first is null.
	var chunks int
	err := tx.QueryRow(ctx, `SELECT coalesce(generation_chunks, (SELECT count(*) FROM index_chunks WHERE index_run_id = $1))
		FROM index_runs WHERE id = $1`, runID).Scan(&chunks)
	if errors.Is(err, pgx.ErrNoRows) {
		// Row-level security hides another account's generation.
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: count index chunks: %w", err)
	}
	// The planner cannot tell how few postings a term has, and estimates
	// enough rows to compile the query, which takes longer than running it.
	if _, err := tx.Exec(ctx, `SET LOCAL jit = off`); err != nil {
		return nil, fmt.Errorf("store: lexical chunks: %w", err)
	}
	rows, err := tx.Query(ctx, query, runID, qis, terms, compound, all, chunks, min(common(chunks), LexicalCommonMax), exclude,
		vectors, perQuery, lexicalTies*perQuery, alone)
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
	taken := map[int]int{}
	return slices.DeleteFunc(hits, func(h SimilarHit) bool {
		taken[h.Query]++
		return taken[h.Query] > perQuery
	}), nil
}
