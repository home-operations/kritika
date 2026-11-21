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
	if err := s.owner.QueryRow(ctx, `INSERT INTO index_runs (account_id, repository_id, commit_sha, embed_model, embed_dims, mode, status, index_format)
		VALUES ($1, $2, 'abc', 'test-embed', 8, 'full', 'completed', $3) RETURNING id`, account, repo, postingsIndexFormat).Scan(&gen); err != nil {
		t.Fatal(err)
	}
	// Enough chunks, every one of them excluded, that their IDs do not fit
	// the least work_mem.
	const chunks = 20000
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
	for _, table := range []string{"index_chunks", "similar_exclusions"} {
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
		return tx.QueryRow(ctx, `EXPLAIN (FORMAT JSON) `+lexicalChunksSQL, args...).Scan(&plan)
	}); err != nil {
		t.Fatal(err)
	}
	var root []struct{ Plan planNode }
	if err := json.Unmarshal(plan, &root); err != nil || len(root) != 1 {
		t.Fatalf("plan %s: %v", plan, err)
	}
	scans, perRow := root[0].Plan.excludedScans(false)
	if scans == 0 || perRow > 0 {
		t.Errorf("the excluded chunks are scanned %d times, %d of them in a subplan run per posting; want once, in a join:\n%s",
			scans, perRow, plan)
	}
}

// planNode is a node of a plan that EXPLAIN (FORMAT JSON) gives.
type planNode struct {
	NodeType           string     `json:"Node Type"`
	CTEName            string     `json:"CTE Name"`
	IndexName          string     `json:"Index Name"`
	ParentRelationship string     `json:"Parent Relationship"`
	Plans              []planNode `json:"Plans"`
}

// excludedScans counts the scans of the excluded CTE in n's tree, and those
// of them under a subplan, inSubPlan saying whether n is.
func (n planNode) excludedScans(inSubPlan bool) (scans, perRow int) {
	inSubPlan = inSubPlan || n.ParentRelationship == "SubPlan"
	if n.NodeType == "CTE Scan" && n.CTEName == "excluded" {
		scans++
		if inSubPlan {
			perRow++
		}
	}
	for _, c := range n.Plans {
		s, p := c.excludedScans(inSubPlan)
		scans, perRow = scans+s, perRow+p
	}
	return scans, perRow
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
