//go:build integration

package store

import (
	"context"
	"fmt"
	"math"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/runspec"
)

// TestLexicalChunks: a chunk holds the words of its path, symbol and text as
// InsertIndexChunks stores them. A lexical search ranks the generation's
// chunks by the BM25 weight of the query's terms that each holds, leaves out
// the excluded paths and the terms too common to search for, and finds
// nothing of another account or generation. A chunk needs two of a query's
// terms, a compound one, or one rare enough to be found by it alone, and of
// chunks of equal weight the one nearest the query comes first.
func TestLexicalChunks(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, twoAccounts)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnsureIndexSchema(ctx, "kritika_app", "test-embed", 8, ""); err != nil {
		t.Fatal(err)
	}
	// The repositories go too: other suites count the accounts' own.
	t.Cleanup(func() {
		_, _ = s.owner.Exec(context.Background(), `UPDATE repositories SET active_index_run_id = NULL;
			DROP TABLE IF EXISTS index_chunks; DELETE FROM index_schema;
			DELETE FROM index_postings WHERE index_run_id IN (SELECT x.id FROM index_runs x JOIN repositories r ON r.id = x.repository_id
				WHERE r.name LIKE 'lexical/%');
			DELETE FROM index_runs WHERE repository_id IN (SELECT id FROM repositories WHERE name LIKE 'lexical/%');
			DELETE FROM repositories WHERE name LIKE 'lexical/%'`)
	})
	vec := []float32{1, 0, 0, 0, 0, 0, 0, 0}
	near := []float32{0, 1, 0, 0, 0, 0, 0, 0}
	build := func(account, name string, chunks []StagedChunk, vectors ...[]float32) string {
		t.Helper()
		id := accountID(t, s, account)
		repo := configfile.RepositoryID(id, name)
		if _, err := s.owner.Exec(ctx, `INSERT INTO repositories (id, account_id, name) VALUES ($1, $2, $3)`, repo, id, name); err != nil {
			t.Fatal(err)
		}
		var run string
		if err := s.owner.QueryRow(ctx, `INSERT INTO index_runs (account_id, repository_id, commit_sha, embed_model, embed_dims, mode, status,
			index_format) VALUES ($1, $2, 'abc', 'test-embed', 8, 'full', 'completed', $3) RETURNING id`, id, repo, runspec.IndexFormat).
			Scan(&run); err != nil {
			t.Fatal(err)
		}
		for len(vectors) < len(chunks) {
			vectors = append(vectors, vec)
		}
		if err := s.WithAccount(ctx, id, func(tx pgx.Tx) error {
			if err := InsertIndexChunks(ctx, tx, id, repo, run, chunks, vectors); err != nil {
				return err
			}
			return ActivateGeneration(ctx, tx, repo, run)
		}); err != nil {
			t.Fatal(err)
		}
		return run
	}
	run := build("alpha", "lexical/target", []StagedChunk{
		{Path: "internal/lease/lease.go", Language: "go", Symbol: "AcquireLease", Kind: "func", Text: "func AcquireLease(slots int) {}"},
		{Path: "internal/lease/release.go", Language: "go", Symbol: "ReleaseLease", Kind: "func", Text: "func ReleaseLease() { AcquireLease(1) }"},
		{Path: "internal/other/other.go", Language: "go", Text: "// Nothing about slots here.\nvar x = 1"},
		{Path: "internal/other/empty.go", Language: "go", Text: "}"},
	})
	other := build("alpha", "lexical/other", []StagedChunk{{Path: "internal/lease/lease.go", Text: "AcquireLease"}})
	foreign := build("beta", "lexical/foreign", []StagedChunk{{Path: "internal/lease/lease.go", Text: "AcquireLease"}})
	// Two chunks that hold the same words, and a third that holds them in
	// a path the search leaves out. The query's vector is the second's.
	ties := build("alpha", "lexical/ties", []StagedChunk{
		{Path: "a.go", Text: "leaseSlots"}, {Path: "b.go", Text: "leaseSlots"}, {Path: "c.go", Text: "leaseSlots"}, {Path: "d.go", Text: "other"},
	}, vec, near, near, vec)

	// Of the four chunks, two hold each of acquirelease, acquire and slots,
	// one releaselease, and every one internal, in its path.
	two, one := idf(2, 4), idf(1, 4)
	query := LexicalQuery{Terms: []LexicalTerm{{"acquirelease", true}, {"slots", false}, {"acquire", false}, {"internal", false}}, Vector: vec}
	for _, tt := range []struct {
		name     string
		account  string
		run      string
		queries  []LexicalQuery
		exclude  []string
		perQuery int
		alone    int
		want     []string
	}{
		{"ranked by weight, without a term every chunk holds or one word alone", "alpha", run, []LexicalQuery{query}, nil, 3, 1, []string{
			fmt.Sprintf("0 internal/lease/lease.go %.3f [acquire acquirelease slots]", 3*two),
			fmt.Sprintf("0 internal/lease/release.go %.3f [acquire acquirelease]", 2*two),
		}},
		{"a word rare enough alone", "alpha", run, []LexicalQuery{query}, nil, 3, 2, []string{
			fmt.Sprintf("0 internal/lease/lease.go %.3f [acquire acquirelease slots]", 3*two),
			fmt.Sprintf("0 internal/lease/release.go %.3f [acquire acquirelease]", 2*two),
			fmt.Sprintf("0 internal/other/other.go %.3f [slots]", two),
		}},
		{"excluded paths", "alpha", run, []LexicalQuery{query}, []string{"internal/lease/lease.go"}, 3, 2, []string{
			fmt.Sprintf("0 internal/lease/release.go %.3f [acquire acquirelease]", 2*two),
			fmt.Sprintf("0 internal/other/other.go %.3f [slots]", two),
		}},
		{"a compound word alone", "alpha", run, []LexicalQuery{{Terms: []LexicalTerm{{"releaselease", true}}, Vector: vec}, query}, nil, 3, 0,
			[]string{
				fmt.Sprintf("0 internal/lease/release.go %.3f [releaselease]", one),
				fmt.Sprintf("1 internal/lease/lease.go %.3f [acquire acquirelease slots]", 3*two),
				fmt.Sprintf("1 internal/lease/release.go %.3f [acquire acquirelease]", 2*two),
			}},
		{"a word of one part alone", "alpha", run, []LexicalQuery{{Terms: []LexicalTerm{{"releaselease", false}}, Vector: vec}}, nil, 3, 0,
			nil},
		{"no terms", "alpha", run, []LexicalQuery{{Vector: vec}}, nil, 3, 1, nil},
		{"a term no chunk holds", "alpha", run, []LexicalQuery{{Terms: []LexicalTerm{{"absent", true}}, Vector: vec}}, nil, 3, 1, nil},
		{"another generation", "alpha", other, []LexicalQuery{query}, nil, 3, 1, []string{
			fmt.Sprintf("0 internal/lease/lease.go %.3f [acquire acquirelease internal]", 3*idf(1, 1)),
		}},
		{"another account's generation", "alpha", foreign, []LexicalQuery{query}, nil, 3, 1, nil},
		{"ties nearest the query first", "alpha", ties, []LexicalQuery{{Terms: []LexicalTerm{{"leaseslots", true}}, Vector: near}},
			[]string{"c.go"}, 2, 0, []string{
				fmt.Sprintf("0 b.go %.3f [leaseslots]", idf(3, 4)),
				fmt.Sprintf("0 a.go %.3f [leaseslots]", idf(3, 4)),
			}},
		{"the nearest of ties at the cut", "alpha", ties, []LexicalQuery{{Terms: []LexicalTerm{{"leaseslots", true}}, Vector: vec}},
			[]string{"c.go"}, 1, 0, []string{
				fmt.Sprintf("0 a.go %.3f [leaseslots]", idf(3, 4)),
			}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := s.WithAccount(ctx, accountID(t, s, tt.account), func(tx pgx.Tx) error {
				// A term in every chunk of a generation is too common.
				hits, err := LexicalChunks(ctx, tx, tt.run, tt.queries, tt.exclude, tt.perQuery, LexicalBar{Per: 1, Least: 2},
					tt.alone)
				if err != nil {
					return err
				}
				var got []string
				for _, h := range hits {
					got = append(got, fmt.Sprintf("%d %s %.3f %v", h.Query, h.Chunk.Path, h.Lexical, h.Matched))
					if q := tt.queries[h.Query]; h.Similarity < 0.99 && slices.Equal(q.Vector, vec) && h.Chunk.Path != "b.go" {
						t.Errorf("%s: similarity %g, want the query vector's", h.Chunk.Path, h.Similarity)
					}
				}
				if !slices.Equal(got, tt.want) {
					t.Errorf("hits = %q, want %q", got, tt.want)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

// idf is BM25's inverse document frequency of a term in n of chunks.
func idf(n, chunks int) float64 {
	return math.Log(1 + (float64(chunks-n)+0.5)/(float64(n)+0.5))
}
