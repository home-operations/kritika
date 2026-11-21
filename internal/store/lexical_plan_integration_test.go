//go:build integration

package store

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/configfile"
)

// TestLexicalExclusionsJoinOnce: a lexical search reads the excluded chunks
// once, as one side of a join, and not once for each posting it tests, even
// when there are too many of them to hash in work_mem. A NOT IN over a
// subquery is planned as a subplan, which past that memory scans every
// excluded chunk for each posting.
func TestLexicalExclusionsJoinOnce(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, soloAccount("lexical-plan"))); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnsureIndexSchema(ctx, "kritika_app", "test-embed", 8, ""); err != nil {
		t.Fatal(err)
	}
	account := accountID(t, s, "lexical-plan")
	repo := configfile.RepositoryID(account, "lexical/plan")
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
		if _, err := s.owner.Exec(ctx, `DROP TABLE IF EXISTS index_chunks; DELETE FROM index_schema`); err != nil {
			t.Error(err)
		}
		for _, stmt := range []string{`DELETE FROM index_runs WHERE repository_id = $1`,
			`DELETE FROM repositories WHERE id = $1`} {
			if _, err := s.owner.Exec(ctx, stmt, repo); err != nil {
				t.Error(err)
			}
		}
	})
	if _, err := s.owner.Exec(ctx, `INSERT INTO repositories (id, account_id, name) VALUES ($1, $2, 'lexical/plan')`, repo, account); err != nil {
		t.Fatal(err)
	}
	// Enough chunks, every one of them excluded, that their IDs do not fit
	// the least work_mem. The generation is counted, as a publication counts
	// it, so that the search reads none of them to count them.
	const chunks = 20000
	if err := s.owner.QueryRow(ctx, `INSERT INTO index_runs (account_id, repository_id, commit_sha, embed_model, embed_dims, mode, status,
		index_format, generation_chunks) VALUES ($1, $2, 'abc', 'test-embed', 8, 'full', 'completed', $3, $4) RETURNING id`,
		account, repo, postingsIndexFormat, chunks).Scan(&gen); err != nil {
		t.Fatal(err)
	}
	if _, err := s.owner.Exec(ctx, `INSERT INTO index_chunks (account_id, repository_id, index_run_id, path, start_line, end_line, text, embedding)
		SELECT $1, $2, $3, 'f/' || i || '.go', 1, 1, 'leaseSlots', '[1,0,0,0,0,0,0,0]' FROM generate_series(1, $4) AS i`,
		account, repo, gen, chunks); err != nil {
		t.Fatal(err)
	}
	if err := s.owner.QueryRow(ctx, `INSERT INTO runner_runs (account_id, kind) VALUES ($1, 'review') RETURNING id`, account).
		Scan(&runner); err != nil {
		t.Fatal(err)
	}
	if _, err := s.owner.Exec(ctx, `INSERT INTO similar_exclusions (runner_run_id, account_id, path)
		SELECT $1, $2, 'f/' || i || '.go' FROM generate_series(1, $3) AS i`, runner, account, chunks); err != nil {
		t.Fatal(err)
	}
	// Postings of both of the query's terms, in the same chunks, fewer than
	// the bar leaves out as common, every one of them excluded: each chunk
	// holds both terms, so a search tests each against the excluded paths.
	const postings = LexicalCommonMax - 1
	if _, err := s.owner.Exec(ctx, `INSERT INTO index_postings (index_run_id, lexeme, account_id, chunks, chunk_ids)
		SELECT $1, t, $2, $3::int, (SELECT array_agg(id) FROM (SELECT id FROM index_chunks WHERE index_run_id = $1 ORDER BY id LIMIT $3::int) c)
		FROM unnest(ARRAY['lease', 'slots']) AS t`, gen, account, postings); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := s.owner.Exec(context.Background(), `DELETE FROM index_postings WHERE index_run_id = $1`, gen); err != nil {
			t.Error(err)
		}
	})
	for _, table := range []string{"index_chunks", "similar_exclusions", "index_postings"} {
		if _, err := s.owner.Exec(ctx, `ANALYZE `+table); err != nil {
			t.Fatal(err)
		}
	}

	var plan []byte
	if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SET LOCAL work_mem = '64kB'`); err != nil {
			return err
		}
		args := lexicalArgs(gen, []LexicalQuery{{Terms: []LexicalTerm{{Lexeme: "lease"}, {Lexeme: "slots"}}, Vector: make([]float32, 8)}},
			Exclusions{Run: runner}, 4, LexicalBar{Least: LexicalCommonMax}, 4)
		return tx.QueryRow(ctx, `EXPLAIN (ANALYZE, FORMAT JSON) `+lexicalChunksSQL, args...).Scan(&plan)
	}); err != nil {
		t.Fatal(err)
	}
	var root []struct{ Plan planNode }
	if err := json.Unmarshal(plan, &root); err != nil || len(root) != 1 {
		t.Fatalf("plan %s: %v", plan, err)
	}
	// A scan on the inner side of a nested loop, or in a subplan, runs again
	// for each posting it tests. One that runs once emits the candidates
	// that the postings find, every one of them excluded, and not every
	// excluded chunk of the generation.
	if scans, loops, rows := root[0].Plan.excludedScans(); scans == 0 || loops != 1 || rows != postings {
		t.Errorf("the excluded chunks are read by %d scans, run %v times and emitting %v rows in all; "+
			"want one run, as one side of a join, emitting the %d posting candidates:\n%s",
			scans, loops, rows, postings, plan)
	}
}

// planNode is a node of a plan that EXPLAIN (FORMAT JSON) gives, with
// ANALYZE the times that it ran and the rows that it emitted each time.
type planNode struct {
	NodeType    string  `json:"Node Type"`
	CTEName     string  `json:"CTE Name"`
	IndexName   string  `json:"Index Name"`
	Alias       string  `json:"Alias"`
	ActualRows  float64 `json:"Actual Rows"`
	ActualLoops float64 `json:"Actual Loops"`
	// Filtered is how many rows that a scan read and its filter then
	// removed, each time that it ran.
	Filtered float64    `json:"Rows Removed by Filter"`
	Plans    []planNode `json:"Plans"`
}

// rowsRead counts the rows that the scans of alias in n's tree read, in all
// the times that they ran: those that they emitted, and those that their
// filters removed.
func (n planNode) rowsRead(alias string) float64 {
	var rows float64
	if n.Alias == alias {
		rows = (n.ActualRows + n.Filtered) * n.ActualLoops
	}
	for _, c := range n.Plans {
		rows += c.rowsRead(alias)
	}
	return rows
}

// excludedScans counts the scans of the excluded CTE in n's tree, the times
// that they ran in all, and the rows that they emitted in all.
func (n planNode) excludedScans() (scans int, loops, rows float64) {
	if n.NodeType == "CTE Scan" && n.CTEName == "excluded" {
		scans, loops, rows = 1, n.ActualLoops, n.ActualRows*n.ActualLoops
	}
	for _, c := range n.Plans {
		s, l, r := c.excludedScans()
		scans, loops, rows = scans+s, loops+l, rows+r
	}
	return scans, loops, rows
}

// indexes are the names of the indexes that n's tree reads.
func (n planNode) indexes() []string {
	var names []string
	if n.IndexName != "" {
		names = append(names, n.IndexName)
	}
	for _, c := range n.Plans {
		names = append(names, c.indexes()...)
	}
	return names
}
