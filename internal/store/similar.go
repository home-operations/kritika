package store

import (
	"context"
	"errors"
	"fmt"

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

// Exclusions are the paths that a search leaves out: those that the runner
// run Run reported (SetSimilarExclude), which the statement reads itself,
// and Paths besides, which a runner of an earlier version sends in each
// request instead. A Run of "" reported none.
type Exclusions struct {
	Run   string
	Paths []string
}

// args are the statement's arguments for e: the run, NULL for none, and the
// paths, never NULL.
func (e Exclusions) args() (run any, paths []string) {
	if e.Run != "" {
		run = e.Run
	}
	if e.Paths == nil {
		return run, []string{}
	}
	return run, e.Paths
}

// SweepSimilarExclusions deletes the paths that every account's finished
// runner runs reported (SetSimilarExclude), a batch at a time, and returns
// how many it deleted. A finished run's runner has stopped, so no search
// reads them again. Owner connection, leader only, like SweepModelCalls.
func (s *Store) SweepSimilarExclusions(ctx context.Context) (int64, error) {
	if s.owner == nil {
		return 0, errors.New("store: SweepSimilarExclusions needs the owner connection")
	}
	n, err := s.sweepBatches(ctx, `DELETE FROM similar_exclusions WHERE id IN (
		SELECT e.id FROM similar_exclusions e JOIN runner_runs r ON r.id = e.runner_run_id
		WHERE r.finished_at < now() - make_interval(secs => $1) LIMIT $2)`, 0, modelCallSweepBatch)
	if err != nil {
		return n, fmt.Errorf("store: sweep similar exclusions: %w", err)
	}
	return n, nil
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

// similarChunksSQL is the nearest chunks of generation $2 to vector $1, $4 of
// them, outside paths $3 and those that runner run $5 reported.
const similarChunksSQL = `SELECT c.path, c.start_line, c.end_line, c.language, c.symbol, c.kind, c.scope, c.text,
				1 - (c.embedding <=> $1::halfvec)
			FROM index_chunks c WHERE c.index_run_id = $2 AND NOT (c.path = ANY($3))
				AND NOT EXISTS (SELECT 1 FROM similar_exclusions e WHERE e.runner_run_id = $5::uuid AND md5(e.path) = md5(c.path) AND e.path = c.path)
			ORDER BY c.embedding <=> $1::halfvec LIMIT $4`

// SimilarChunks finds, for each query vector, the perQuery chunks of the
// generation nearest it, outside the excluded paths, in the order the
// index ranks them. The generation and account filters are strict and
// cheap, which is what VectorChord's prefilter wants: it then skips the
// distance of every chunk outside this repository's generation. A run's
// reported paths are left out by a probe of similar_exclusions' index, by
// the path's digest, for each chunk the scan yields.
func SimilarChunks(
	ctx context.Context, tx pgx.Tx, runID string, vectors [][]float32, exclude Exclusions, perQuery int,
) ([]SimilarHit, error) {
	run, paths := exclude.args()
	if _, err := tx.Exec(ctx, `SET LOCAL vchordrq.prefilter = on`); err != nil {
		return nil, fmt.Errorf("store: enable prefilter: %w", err)
	}
	var hits []SimilarHit
	for i, v := range vectors {
		rows, err := tx.Query(ctx, similarChunksSQL, model.VectorLiteral(v), runID, paths, perQuery, run)
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

// LexicalBar is how many of a generation's chunks a word can be in and still
// be searched for: fewer than one in Per of them, or than Least when that is
// more, and fewer than LexicalCommonMax however many that is. With Share, a
// word in more than one in Share of them is too common too, which bars the
// words that nearly every chunk of a small generation holds, whose weight
// says nothing of a chunk. A Per or Share of 0 sets no such bound: without
// Per, Least alone bounds a word, and without Least either, LexicalCommonMax
// alone does.
type LexicalBar struct{ Per, Least, Share int }

// lexicalChunksSQL reads, for each term of every query of a request
// together, the generation's posting of it: how many chunks hold it, its
// document frequency, and, unless that is the count at which it is too
// common to search for, which. The generation's chunks, which the weights
// and the bar are measured against, are read in the same statement, so that
// both are of the snapshot that the postings are: a publication that commits
// during the search is seen whole or not at all. So is whether the
// generation is still searched by name, as ActiveGenerationFor found it: a
// step of another index format, which leaves the postings as they were, can
// publish in between, and the search then finds nothing. Only the excluded
// paths' chunks and the few ranked chunks are read from index_chunks: the
// ranked ones first without their text, to compare the ties by their vectors,
// and then whole, the hits each query keeps alone. A score is rounded so that
// two chunks holding the same terms tie, whatever order they were added in.
//
// $1 is the generation, $2, $3 and $4 each query's index, term and whether
// the term is compound, $5 every term once, $6, $7 and $14 the LexicalBar's
// Per, Least and Share, and $13 LexicalCommonMax, $8 the excluded paths and
// $16 the runner run whose reported paths are excluded too, $9 each query's
// vector, $10 the hits each query keeps, $11 the most of them read when they
// tie, $12 the most chunks a term can be in and still find a chunk alone, and
// $15 postingsIndexFormat. A generation that no publication of this build
// counted, which only a test searches, is counted here: coalesce reads its
// second argument only when the first is null.
const lexicalChunksSQL = `WITH g AS MATERIALIZED (
		SELECT coalesce(g.generation_chunks, (SELECT count(*) FROM index_chunks WHERE index_run_id = $1))::float8 AS n
		FROM index_runs g WHERE g.id = $1 AND g.index_format >= $15 AND NOT ` + steppedInAnotherFormat + `
	), held AS MATERIALIZED (
		SELECT t.t, x.chunk_id, p.chunks AS n
		FROM g, unnest($5::text[]) AS t (t)
		JOIN index_postings p ON p.index_run_id = $1 AND p.lexeme = t.t,
			unnest(p.chunk_ids) AS x (chunk_id)
		WHERE p.chunks < least(greatest(trunc(g.n / nullif($6::int, 0)), nullif($7::int, 0)), $13::int)
			AND ($14::int = 0 OR p.chunks::float8 * $14::int <= g.n)
	), weights AS (
		SELECT q.qi, q.t, q.compound, f.n, ln(1 + (g.n - f.n + 0.5) / (f.n + 0.5)) AS w
		FROM g, unnest($2::int[], $3::text[], $4::bool[]) AS q (qi, t, compound)
		JOIN (SELECT DISTINCT t, n FROM held) f ON f.t = q.t
	), excluded AS MATERIALIZED (
		SELECT id FROM index_chunks WHERE index_run_id = $1 AND path = ANY($8::text[])
		UNION ALL
		SELECT c.id FROM similar_exclusions e JOIN index_chunks c ON c.index_run_id = $1 AND c.path = e.path
		WHERE e.runner_run_id = $16::uuid
	), scored AS (
		SELECT w.qi, h.chunk_id AS id, round(sum(w.w)::numeric, 9)::float8 AS score,
			array_agg(w.t ORDER BY w.w DESC, w.t) AS matched
		FROM held h JOIN weights w ON w.t = h.t
		WHERE NOT EXISTS (SELECT 1 FROM excluded e WHERE e.id = h.chunk_id)
		GROUP BY w.qi, h.chunk_id
		HAVING count(*) > 1 OR bool_or(w.compound) OR min(w.n) <= $12
	), top AS MATERIALIZED (
		SELECT * FROM (
			SELECT s.*, rank() OVER (PARTITION BY s.qi ORDER BY s.score DESC) AS rank,
				row_number() OVER (PARTITION BY s.qi ORDER BY s.score DESC, s.id) AS place
			FROM scored s
		) r WHERE rank <= $10 AND place <= $11
	), kept AS MATERIALIZED (
		SELECT * FROM (
			SELECT r.qi, r.id, r.score, r.matched, c.similarity, c.path, c.start_line,
				row_number() OVER (PARTITION BY r.qi ORDER BY r.score DESC, c.similarity DESC, c.path, c.start_line) AS place
			FROM top r, LATERAL (SELECT c.path, c.start_line, 1 - (c.embedding <=> (($9::text[])[r.qi + 1])::halfvec) AS similarity
				FROM index_chunks c WHERE c.index_run_id = $1 AND c.id = r.id LIMIT 1) c
		) k WHERE place <= $10
	)
	SELECT k.qi, c.path, c.start_line, c.end_line, c.language, c.symbol, c.kind, c.scope, c.text, k.similarity, k.score, k.matched
	FROM kept k, LATERAL (SELECT * FROM index_chunks c WHERE c.index_run_id = $1 AND c.id = k.id LIMIT 1) c
	ORDER BY k.qi, k.place`

// LexicalChunks finds, for each query, the perQuery chunks of the generation
// that hold the most weight of its terms, outside the excluded paths, with
// the terms each holds, the heaviest first. A term weighs what BM25 gives a
// term in so many of the generation's chunks: one that few hold counts for
// more than one that many do. A term that bar puts too many of the
// generation's chunks in is left out as too common to tell them apart.
//
// A chunk is found when it holds two of a query's terms, a compound one, or
// one in at most alone of the generation's chunks. So a query's one common
// word does not find every chunk that holds it. Of chunks of equal weight,
// the one nearest the query's vector comes first, then by path and line.
// Ties are read up to LexicalCommonMax past perQuery, so that every chunk
// that holds one word, which fewer than LexicalCommonMax do, is compared by
// its similarity before the cut; past that, a tie is cut by the chunks' IDs.
func LexicalChunks(
	ctx context.Context, tx pgx.Tx, runID string, queries []LexicalQuery, exclude Exclusions, perQuery int, bar LexicalBar, alone int,
) ([]SimilarHit, error) {
	return lexicalChunks(ctx, tx, lexicalChunksSQL, runID, queries, exclude, perQuery, bar, alone)
}

// lexicalChunks is LexicalChunks by query, a test's or lexicalChunksSQL.
func lexicalChunks(
	ctx context.Context, tx pgx.Tx, query, runID string, queries []LexicalQuery, exclude Exclusions, perQuery int, bar LexicalBar,
	alone int,
) ([]SimilarHit, error) {
	args := lexicalArgs(runID, queries, exclude, perQuery, bar, alone)
	if args == nil {
		return nil, nil
	}
	// The planner cannot tell how few postings a term has, and estimates
	// enough rows to compile the query, which takes longer than running it.
	if _, err := tx.Exec(ctx, `SET LOCAL jit = off`); err != nil {
		return nil, fmt.Errorf("store: lexical chunks: %w", err)
	}
	rows, err := tx.Query(ctx, query, args...)
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

// lexicalArgs are lexicalChunksSQL's arguments, or nil when the queries hold
// no term.
func lexicalArgs(runID string, queries []LexicalQuery, exclude Exclusions, perQuery int, bar LexicalBar, alone int) []any {
	excludeRun, excludePaths := exclude.args()
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
		return nil
	}
	return []any{runID, qis, terms, compound, all, bar.Per, bar.Least, excludePaths,
		vectors, perQuery, perQuery + LexicalCommonMax, alone, LexicalCommonMax, bar.Share, postingsIndexFormat, excludeRun}
}
