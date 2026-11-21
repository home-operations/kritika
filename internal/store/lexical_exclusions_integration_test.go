//go:build integration

package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/configfile"
)

// TestLexicalExclusionsReadOnlyCandidates: a search by name tests only the
// chunks that its postings find against the excluded paths, however many
// chunks those paths hold, and leaves the excluded ones out before it ranks
// and cuts the hits.
func TestLexicalExclusionsReadOnlyCandidates(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, soloAccount("lexical-exclusions"))); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnsureIndexSchema(ctx, "kritika_app", "test-embed", 8, ""); err != nil {
		t.Fatal(err)
	}
	account := accountID(t, s, "lexical-exclusions")
	repo := configfile.RepositoryID(account, "lexical/exclusions")
	var gen, runner string
	t.Cleanup(func() {
		ctx := context.Background()
		for _, stmt := range []string{
			`DELETE FROM runner_runs WHERE account_id = $1`,
			`UPDATE repositories SET active_index_run_id = NULL WHERE account_id = $1`,
		} {
			if _, err := s.owner.Exec(ctx, stmt, account); err != nil {
				t.Error(err)
			}
		}
		if _, err := s.owner.Exec(ctx, `DROP TABLE IF EXISTS index_chunks; DELETE FROM index_schema; TRUNCATE index_postings`); err != nil {
			t.Error(err)
		}
		for _, stmt := range []string{`DELETE FROM index_runs WHERE repository_id = $1`,
			`DELETE FROM repositories WHERE id = $1`} {
			if _, err := s.owner.Exec(ctx, stmt, repo); err != nil {
				t.Error(err)
			}
		}
	})
	if _, err := s.owner.Exec(ctx, `INSERT INTO repositories (id, account_id, name) VALUES ($1, $2, 'lexical/exclusions')`,
		repo, account); err != nil {
		t.Fatal(err)
	}
	// A large change: every chunk of the generation but two is of a path
	// that the runner reported, and the posting of the query's word is
	// sparse, three chunks.
	const reported = 20000
	if err := s.owner.QueryRow(ctx, `INSERT INTO index_runs (account_id, repository_id, commit_sha, embed_model, embed_dims, mode, status,
		index_format, generation_chunks) VALUES ($1, $2, 'abc', 'test-embed', 8, 'full', 'completed', $3, $4) RETURNING id`,
		account, repo, postingsIndexFormat, reported+2).Scan(&gen); err != nil {
		t.Fatal(err)
	}
	if _, err := s.owner.Exec(ctx, `INSERT INTO index_chunks (account_id, repository_id, index_run_id, path, start_line, end_line, text, embedding)
		SELECT $1::uuid, $2::uuid, $3::uuid, 'f/' || i || '.go', 1, 1, 'func f() {}', '[1,0,0,0,0,0,0,0]'::halfvec FROM generate_series(1, $4) AS i
		UNION ALL SELECT $1, $2, $3, 'drop.go', 1, 1, 'func rareName() {}', '[1,0,0,0,0,0,0,0]'
		UNION ALL SELECT $1, $2, $3, 'keep.go', 1, 1, 'func rareName() {}', '[0,1,0,0,0,0,0,0]'`,
		account, repo, gen, reported); err != nil {
		t.Fatal(err)
	}
	if err := s.owner.QueryRow(ctx, `INSERT INTO runner_runs (account_id, kind) VALUES ($1, 'review') RETURNING id`, account).
		Scan(&runner); err != nil {
		t.Fatal(err)
	}
	if _, err := s.owner.Exec(ctx, `INSERT INTO similar_exclusions (runner_run_id, account_id, path)
		SELECT $1, $2, 'f/' || i || '.go' FROM generate_series(1, $3) AS i`, runner, account, reported); err != nil {
		t.Fatal(err)
	}
	// drop.go, which the request leaves out, and f/1.go, which the runner
	// reported, are nearer the query than keep.go, so either would be the
	// one hit kept were it ranked.
	const candidates = 3
	if _, err := s.owner.Exec(ctx, `INSERT INTO index_postings (index_run_id, lexeme, account_id, chunks, chunk_ids)
		VALUES ($1, 'rarename', $2, $3, (SELECT array_agg(id) FROM index_chunks
			WHERE index_run_id = $1 AND path IN ('f/1.go', 'drop.go', 'keep.go')))`, gen, account, candidates); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"index_chunks", "similar_exclusions", "index_postings"} {
		if _, err := s.owner.Exec(ctx, `ANALYZE `+table); err != nil {
			t.Fatal(err)
		}
	}

	queries := []LexicalQuery{{Terms: []LexicalTerm{{Lexeme: "rarename", Compound: true}}, Vector: []float32{1, 0, 0, 0, 0, 0, 0, 0}}}
	exclude := Exclusions{Run: runner, Paths: []string{"drop.go"}}
	var plan []byte
	var hits []SimilarHit
	if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		args := lexicalArgs(gen, queries, exclude, 1, LexicalBar{Least: LexicalCommonMax}, 4)
		if err := tx.QueryRow(ctx, `EXPLAIN (ANALYZE, FORMAT JSON) `+lexicalChunksSQL, args...).Scan(&plan); err != nil {
			return err
		}
		var err error
		hits, err = LexicalChunks(ctx, tx, gen, queries, exclude, 1, LexicalBar{Least: LexicalCommonMax}, 4)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Chunk.Path != "keep.go" {
		t.Errorf("search by name found %+v, want keep.go alone: the excluded chunks left out before the cut", hits)
	}
	var root []struct{ Plan relationRows }
	if err := json.Unmarshal(plan, &root); err != nil || len(root) != 1 {
		t.Fatalf("plan %s: %v", plan, err)
	}
	// Each candidate is examined to test its path, and the one kept to rank
	// it and to return it: a few rows a candidate, not one for each chunk of
	// the excluded paths, whether a scan returns them or filters them out.
	if read := root[0].Plan.chunksRead(); read > 3*candidates {
		t.Errorf("the search examined %v rows of index_chunks for %d posting candidates, want at most %d:\n%s",
			read, candidates, 3*candidates, plan)
	}
}

// TestChunksRead: chunksRead counts the rows that a scan of index_chunks
// examined, those it filtered out as well as those it returned, so a scan
// of a whole partition that returns the few candidates alone is counted
// whole.
func TestChunksRead(t *testing.T) {
	for _, tt := range []struct {
		name, plan string
		want       float64
	}{
		{"rows returned, in every loop", `{"Plans": [{"Relation Name": "index_chunks_07", "Actual Rows": 1, "Actual Loops": 3}]}`, 3},
		{"rows filtered out", `{"Plans": [{"Relation Name": "index_chunks_07", "Actual Rows": 1, "Actual Loops": 3,
			"Rows Removed by Filter": 20001}]}`, 60006},
		{"rows an index's recheck removed", `{"Plans": [{"Relation Name": "index_chunks_07", "Actual Rows": 1, "Actual Loops": 1,
			"Rows Removed by Index Recheck": 500, "Rows Removed by Filter": 20}]}`, 521},
		{"another relation's", `{"Plans": [{"Relation Name": "similar_exclusions", "Actual Rows": 1, "Actual Loops": 1,
			"Rows Removed by Filter": 20000}]}`, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var n relationRows
			if err := json.Unmarshal([]byte(tt.plan), &n); err != nil {
				t.Fatal(err)
			}
			if got := n.chunksRead(); got != tt.want {
				t.Errorf("chunksRead = %v, want %v", got, tt.want)
			}
		})
	}
}

// relationRows is a node of a plan that EXPLAIN (ANALYZE, FORMAT JSON)
// gives: the relation that it scans, if any, and, for each time that it
// ran, the rows that it returned and those that it read and removed, by its
// filter or by an index's recheck.
type relationRows struct {
	RelationName     string         `json:"Relation Name"`
	ActualRows       float64        `json:"Actual Rows"`
	ActualLoops      float64        `json:"Actual Loops"`
	RemovedByFilter  float64        `json:"Rows Removed by Filter"`
	RemovedByRecheck float64        `json:"Rows Removed by Index Recheck"`
	Plans            []relationRows `json:"Plans"`
}

// chunksRead is how many rows n's tree examined in index_chunks' partitions
// in all: those that each scan returned and those that it removed.
func (n relationRows) chunksRead() float64 {
	var read float64
	if strings.HasPrefix(n.RelationName, "index_chunks") {
		read = (n.ActualRows + n.RemovedByFilter + n.RemovedByRecheck) * n.ActualLoops
	}
	for _, c := range n.Plans {
		read += c.chunksRead()
	}
	return read
}
