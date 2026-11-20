//go:build integration

package store

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/configfile"
)

// TestLexicalChunks: a chunk holds the words of its path, symbol and text as
// InsertIndexChunks stores them. A lexical search ranks the generation's
// chunks by the weight of the query's terms that each holds, leaves out the
// excluded paths, and finds nothing of another account or generation.
func TestLexicalChunks(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, twoAccounts)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnsureIndexSchema(ctx, "kritika_app", "test-embed", 8, ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = s.owner.Exec(context.Background(), `UPDATE repositories SET active_index_run_id = NULL;
			DROP TABLE IF EXISTS index_chunks; DELETE FROM index_schema`)
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
	alpha := accountID(t, s, "alpha")

	err := s.WithAccount(ctx, alpha, func(tx pgx.Tx) error {
		counts, chunks, err := TermCounts(ctx, tx, run, []string{"acquirelease", "slots", "absent", "internal"}, 3)
		if err != nil {
			return err
		}
		if chunks != 4 {
			t.Errorf("chunks = %d, want 4", chunks)
		}
		// internal is in every chunk's path, and its count stops at the limit.
		if want := map[string]int{"acquirelease": 2, "slots": 2, "absent": 0, "internal": 3}; !maps.Equal(counts, want) {
			t.Errorf("counts = %v, want %v", counts, want)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	query := LexicalQuery{Terms: []string{"acquirelease", "slots", "acquire"}, Weights: []float64{2, 1, 0.5}, Vector: vec}
	for _, tt := range []struct {
		name    string
		account string
		run     string
		queries []LexicalQuery
		exclude []string
		want    []string
	}{
		{"ranked by weight", "alpha", run, []LexicalQuery{query}, nil, []string{
			"0 internal/lease/lease.go 3.5 [acquirelease slots acquire]",
			"0 internal/lease/release.go 2.5 [acquirelease acquire]",
			"0 internal/other/other.go 1 [slots]",
		}},
		{"excluded paths", "alpha", run, []LexicalQuery{query}, []string{"internal/lease/lease.go"}, []string{
			"0 internal/lease/release.go 2.5 [acquirelease acquire]",
			"0 internal/other/other.go 1 [slots]",
		}},
		{"per query", "alpha", run, []LexicalQuery{{Terms: []string{"releaselease"}, Weights: []float64{1}, Vector: vec}, query}, nil, []string{
			"0 internal/lease/release.go 1 [releaselease]",
			"1 internal/lease/lease.go 3.5 [acquirelease slots acquire]",
			"1 internal/lease/release.go 2.5 [acquirelease acquire]",
			"1 internal/other/other.go 1 [slots]",
		}},
		{"no terms", "alpha", run, []LexicalQuery{{Vector: vec}}, nil, nil},
		{"another generation", "alpha", other, []LexicalQuery{query}, nil, []string{"0 internal/lease/lease.go 2.5 [acquirelease acquire]"}},
		{"another account's generation", "alpha", foreign, []LexicalQuery{query}, nil, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := s.WithAccount(ctx, accountID(t, s, tt.account), func(tx pgx.Tx) error {
				hits, err := LexicalChunks(ctx, tx, tt.run, tt.queries, tt.exclude, 3)
				if err != nil {
					return err
				}
				var got []string
				for _, h := range hits {
					got = append(got, fmt.Sprintf("%d %s %g %v", h.Query, h.Chunk.Path, h.Lexical, h.Matched))
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
