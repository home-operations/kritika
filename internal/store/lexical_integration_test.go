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
)

// TestLexicalChunks: a chunk holds the words of its path, symbol and text as
// InsertIndexChunks stores them. A lexical search ranks the generation's
// chunks by the BM25 weight of the query's terms that each holds, leaves out
// the excluded paths and the terms too common to search for, and finds
// nothing of another account or generation.
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
			DELETE FROM index_runs WHERE repository_id IN (SELECT id FROM repositories WHERE name LIKE 'lexical/%');
			DELETE FROM repositories WHERE name LIKE 'lexical/%'`)
	})
	vec := []float32{1, 0, 0, 0, 0, 0, 0, 0}
	build := func(account, name string, chunks []StagedChunk) string {
		t.Helper()
		id := accountID(t, s, account)
		repo := configfile.RepositoryID(id, name)
		if _, err := s.owner.Exec(ctx, `INSERT INTO repositories (id, account_id, name) VALUES ($1, $2, $3)`, repo, id, name); err != nil {
			t.Fatal(err)
		}
		var run string
		if err := s.owner.QueryRow(ctx, `INSERT INTO index_runs (account_id, repository_id, commit_sha, embed_model, embed_dims, mode, status)
			VALUES ($1, $2, 'abc', 'test-embed', 8, 'full', 'completed') RETURNING id`, id, repo).Scan(&run); err != nil {
			t.Fatal(err)
		}
		vectors := make([][]float32, len(chunks))
		for i := range vectors {
			vectors[i] = vec
		}
		if err := s.WithAccount(ctx, id, func(tx pgx.Tx) error {
			return InsertIndexChunks(ctx, tx, id, repo, run, chunks, vectors)
		}); err != nil {
			t.Fatal(err)
		}
		return run
	}
	run := build("alpha", "lexical/target", []StagedChunk{
		{Path: "internal/lease/lease.go", Symbol: "AcquireLease", Kind: "func", Text: "func AcquireLease(slots int) {}"},
		{Path: "internal/lease/release.go", Symbol: "ReleaseLease", Kind: "func", Text: "func ReleaseLease() { AcquireLease(1) }"},
		{Path: "internal/other/other.go", Text: "// Nothing about slots here.\nvar x = 1"},
		{Path: "internal/other/empty.go", Text: "}"},
	})
	other := build("alpha", "lexical/other", []StagedChunk{{Path: "internal/lease/lease.go", Text: "AcquireLease"}})
	foreign := build("beta", "lexical/foreign", []StagedChunk{{Path: "internal/lease/lease.go", Text: "AcquireLease"}})

	// Of the four chunks, two hold each of acquirelease, acquire and slots,
	// one releaselease, and every one internal, in its path.
	two, one := idf(2, 4), idf(1, 4)
	query := LexicalQuery{Terms: []string{"acquirelease", "slots", "acquire", "internal"}, Vector: vec}
	for _, tt := range []struct {
		name    string
		account string
		run     string
		queries []LexicalQuery
		exclude []string
		want    []string
	}{
		{"ranked by weight, without a term every chunk holds", "alpha", run, []LexicalQuery{query}, nil, []string{
			fmt.Sprintf("0 internal/lease/lease.go %.3f [acquire acquirelease slots]", 3*two),
			fmt.Sprintf("0 internal/lease/release.go %.3f [acquire acquirelease]", 2*two),
			fmt.Sprintf("0 internal/other/other.go %.3f [slots]", two),
		}},
		{"excluded paths", "alpha", run, []LexicalQuery{query}, []string{"internal/lease/lease.go"}, []string{
			fmt.Sprintf("0 internal/lease/release.go %.3f [acquire acquirelease]", 2*two),
			fmt.Sprintf("0 internal/other/other.go %.3f [slots]", two),
		}},
		{"per query", "alpha", run, []LexicalQuery{{Terms: []string{"releaselease"}, Vector: vec}, query}, nil, []string{
			fmt.Sprintf("0 internal/lease/release.go %.3f [releaselease]", one),
			fmt.Sprintf("1 internal/lease/lease.go %.3f [acquire acquirelease slots]", 3*two),
			fmt.Sprintf("1 internal/lease/release.go %.3f [acquire acquirelease]", 2*two),
			fmt.Sprintf("1 internal/other/other.go %.3f [slots]", two),
		}},
		{"no terms", "alpha", run, []LexicalQuery{{Vector: vec}}, nil, nil},
		{"a term no chunk holds", "alpha", run, []LexicalQuery{{Terms: []string{"absent"}, Vector: vec}}, nil, nil},
		{"another generation", "alpha", other, []LexicalQuery{query}, nil, []string{
			fmt.Sprintf("0 internal/lease/lease.go %.3f [acquire acquirelease internal]", 3*idf(1, 1)),
		}},
		{"another account's generation", "alpha", foreign, []LexicalQuery{query}, nil, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := s.WithAccount(ctx, accountID(t, s, tt.account), func(tx pgx.Tx) error {
				// A term in every chunk of a generation is too common.
				hits, err := LexicalChunks(ctx, tx, tt.run, tt.queries, tt.exclude, 3, func(chunks int) int { return max(chunks, 2) })
				if err != nil {
					return err
				}
				var got []string
				for _, h := range hits {
					got = append(got, fmt.Sprintf("%d %s %.3f %v", h.Query, h.Chunk.Path, h.Lexical, h.Matched))
					if h.Similarity < 0.99 {
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
