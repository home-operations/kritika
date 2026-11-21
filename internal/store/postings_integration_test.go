//go:build integration

package store

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/runspec"
)

// postingsFixture is a repository whose generations a postings test builds.
type postingsFixture struct {
	t             *testing.T
	s             *Store
	account, repo string
}

func (f postingsFixture) run(mode, status string) string {
	f.t.Helper()
	var id string
	if err := f.s.owner.QueryRow(f.t.Context(), `INSERT INTO index_runs (account_id, repository_id, commit_sha, embed_model, embed_dims, mode, status)
		VALUES ($1, $2, 'A', 'test-embed', 8, $3, $4) RETURNING id`, f.account, f.repo, mode, status).Scan(&id); err != nil {
		f.t.Fatal(err)
	}
	return id
}

// insert stores a chunk of each path's text under the run.
func (f postingsFixture) insert(run string, chunks map[string]string) {
	f.t.Helper()
	batch := make([]StagedChunk, 0, len(chunks))
	vectors := make([][]float32, 0, len(chunks))
	for path, text := range chunks {
		batch = append(batch, StagedChunk{Path: path, Language: "go", StartLine: 1, EndLine: 1, Text: text})
		vectors = append(vectors, []float32{1, 0, 0, 0, 0, 0, 0, 0})
	}
	f.in(func(tx pgx.Tx) error {
		return InsertIndexChunks(f.t.Context(), tx, f.account, f.repo, run, batch, vectors)
	})
}

func (f postingsFixture) in(fn func(tx pgx.Tx) error) {
	f.t.Helper()
	if err := f.s.WithAccount(f.t.Context(), f.account, fn); err != nil {
		f.t.Fatal(err)
	}
}

// postings counts the run's postings, and checks the account's against its
// chunks: no posting without its chunk, and no lexeme without its posting.
func (f postingsFixture) postings(run string) int {
	f.t.Helper()
	var orphans, missing, n int
	if err := f.s.owner.QueryRow(f.t.Context(), `SELECT
		(SELECT count(*) FROM index_postings p WHERE p.account_id = $1
		 AND NOT EXISTS (SELECT 1 FROM index_chunks c WHERE c.index_run_id = p.index_run_id AND c.id = p.chunk_id)),
		(SELECT count(*) FROM index_chunks c, unnest(tsvector_to_array(c.lexemes)) AS l WHERE c.account_id = $1
		 AND NOT EXISTS (SELECT 1 FROM index_postings p WHERE p.index_run_id = c.index_run_id AND p.lexeme = l AND p.chunk_id = c.id)),
		(SELECT count(*) FROM index_postings WHERE index_run_id = $2)`, f.account, run).Scan(&orphans, &missing, &n); err != nil {
		f.t.Fatal(err)
	}
	if orphans != 0 || missing != 0 {
		f.t.Fatalf("%d postings without their chunk and %d lexemes without their posting", orphans, missing)
	}
	return n
}

// held is the chunks that the run's publication counted, -1 for none.
func (f postingsFixture) held(run string) (n int) {
	f.t.Helper()
	if err := f.s.owner.QueryRow(f.t.Context(), `SELECT coalesce(generation_chunks, -1) FROM index_runs WHERE id = $1`, run).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

// found is the paths of the run's chunks that hold word.
func (f postingsFixture) found(run, word string) []string {
	f.t.Helper()
	var paths []string
	f.in(func(tx pgx.Tx) error {
		hits, err := LexicalChunks(f.t.Context(), tx, run, []LexicalQuery{{Terms: []LexicalTerm{{word, true}}, Vector: []float32{1, 0, 0, 0, 0, 0, 0, 0}}},
			nil, 10, func(int) int { return 100 }, 0)
		for _, h := range hits {
			paths = append(paths, h.Chunk.Path)
		}
		return err
	})
	return paths
}

// TestIndexPostingsFollowChunks: every path that writes, moves or deletes a
// generation's chunks does the same to their postings, so each chunk's
// lexemes have their postings and no posting outlives its chunk. Each
// publication counts the chunks that its generation holds.
func TestIndexPostingsFollowChunks(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, soloAccount("postings"))); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnsureIndexSchema(ctx, "kritika_app", "test-embed", 8, ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = s.owner.Exec(context.Background(), `UPDATE repositories SET active_index_run_id = NULL;
			DROP TABLE IF EXISTS index_chunks; DELETE FROM index_schema; TRUNCATE index_postings`)
	})
	account := accountID(t, s, "postings")
	repo := configfile.RepositoryID(account, "postings/one")
	if _, err := s.owner.Exec(ctx, `INSERT INTO repositories (id, account_id, name) VALUES ($1, $2, 'postings/one')`, repo, account); err != nil {
		t.Fatal(err)
	}
	f := postingsFixture{t: t, s: s, account: account, repo: repo}

	full := f.run(IndexModeFull, "running")
	f.insert(full, map[string]string{"a.go": "func acquireLease() {}", "b.go": "func releaseLease() {}"})
	f.in(func(tx pgx.Tx) error {
		if err := ActivateGeneration(ctx, tx, repo, full); err != nil {
			return err
		}
		return FinishIndexRun(ctx, tx, full, IndexCompleted, 2, "")
	})
	if n := f.postings(full); n == 0 || f.held(full) != 2 {
		t.Fatalf("published full build: %d postings, %d chunks held; want some, and 2", n, f.held(full))
	}

	step := f.run(IndexModeIncremental, "running")
	f.insert(step, map[string]string{"b.go": "func renewLease() {}", "c.go": "func dropLease() {}"})
	f.in(func(tx pgx.Tx) error {
		return AdvanceGeneration(ctx, tx, repo, Generation{ID: full, Commit: "A"}, step, "B", []string{"b.go", "c.go"})
	})
	if n := f.postings(step); n != 0 || f.held(full) != 3 {
		t.Fatalf("advanced: %d postings left under the step, %d chunks held; want none, and 3", n, f.held(full))
	}
	if got := fmt.Sprint(f.found(full, "renewlease"), f.found(full, "releaselease")); got != "[b.go] []" {
		t.Fatalf("after the step, renewLease and releaseLease are found in %s, want [b.go] []", got)
	}

	failed := f.run(IndexModeFull, "failed")
	f.insert(failed, map[string]string{"d.go": "func failedLease() {}"})
	f.in(func(tx pgx.Tx) error {
		return ClearIndexStaging(ctx, tx, failed, "00000000-0000-0000-0000-000000000000")
	})
	if n := f.postings(failed); n != 0 {
		t.Fatalf("cleared run: %d postings, want none", n)
	}

	stray := f.run(IndexModeFull, "failed")
	f.insert(stray, map[string]string{"e.go": "func strayLease() {}"})
	f.in(func(tx pgx.Tx) error {
		_, runner, err := StartIndexRun(ctx, tx, NewIndexRun{AccountID: account, RepositoryID: repo, Commit: "C", Mode: IndexModeFull,
			Embedding: configfile.Embedding{Model: "test-embed", Dims: 8}})
		if err != nil {
			return err
		}
		// Ended, so that no other suite finds it orphaned.
		_, err = tx.Exec(ctx, `UPDATE runner_runs SET finished_at = now() WHERE id = $1`, runner)
		return err
	})
	if n := f.postings(stray); n != 0 {
		t.Fatalf("stray run: %d postings, want none", n)
	}

	rebuild := f.run(IndexModeFull, "running")
	f.insert(rebuild, map[string]string{"f.go": "func rebuiltLease() {}"})
	f.in(func(tx pgx.Tx) error { return ActivateGeneration(ctx, tx, repo, rebuild) })
	if n := f.postings(full); n != 0 || f.postings(rebuild) == 0 || f.held(rebuild) != 1 {
		t.Fatalf("superseded: %d postings left, %d chunks held by the new generation; want none, and 1", n, f.held(rebuild))
	}

	for range 2 {
		if _, err := s.SweepStoppedIndexes(ctx, 0, func(string, configfile.RepoTraits) bool { return false }); err != nil {
			t.Fatal(err)
		}
	}
	if n := f.postings(rebuild); n != 0 {
		t.Fatalf("stopped repository's generation: %d postings, want none", n)
	}

	last := f.run(IndexModeFull, "running")
	f.insert(last, map[string]string{"g.go": "func lastLease() {}"})
	if rebuilt, err := s.EnsureIndexSchema(ctx, "kritika_app", "other-embed", 8, ""); err != nil || !rebuilt {
		t.Fatalf("EnsureIndexSchema for another embedder = %v, %v; want a rebuild", rebuilt, err)
	}
	if n := f.postings(last); n != 0 {
		t.Fatalf("after a rebuild for another embedder: %d postings, want none", n)
	}
}

// TestActiveGenerationForLexical: a generation is searched by name only when
// a build that writes postings built it and no step of another format, such
// as an older build's during a rollout, advanced it.
func TestActiveGenerationForLexical(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, soloAccount("lexical-ready"))); err != nil {
		t.Fatal(err)
	}
	account := accountID(t, s, "lexical-ready")
	for _, tt := range []struct {
		name         string
		format, step int
		want         bool
	}{
		{"built before postings", 1, 0, false},
		{"built with postings", runspec.IndexFormat, 0, true},
		{"stepped by a build before postings", runspec.IndexFormat, 1, false},
		{"stepped by a build with postings", runspec.IndexFormat, runspec.IndexFormat, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repo := configfile.RepositoryID(account, "lexical-ready/"+tt.name)
			var run string
			if _, err := s.owner.Exec(ctx, `INSERT INTO repositories (id, account_id, name) VALUES ($1, $2, $3)`, repo, account,
				"lexical-ready/"+tt.name); err != nil {
				t.Fatal(err)
			}
			if err := s.owner.QueryRow(ctx, `INSERT INTO index_runs (account_id, repository_id, commit_sha, embed_model, embed_dims, mode, status,
				index_format, finished_at) VALUES ($1, $2, 'A', 'test-embed', 8, 'full', 'completed', $3, now() - interval '1 hour') RETURNING id`,
				account, repo, tt.format).Scan(&run); err != nil {
				t.Fatal(err)
			}
			if _, err := s.owner.Exec(ctx, `UPDATE repositories SET active_index_run_id = $2 WHERE id = $1`, repo, run); err != nil {
				t.Fatal(err)
			}
			if tt.step != 0 {
				if _, err := s.owner.Exec(ctx, `INSERT INTO index_runs (account_id, repository_id, commit_sha, embed_model, embed_dims, mode, status,
					index_format) VALUES ($1, $2, 'B', 'test-embed', 8, 'incremental', 'completed', $3)`, account, repo, tt.step); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
				found, lexical, err := ActiveGenerationFor(ctx, tx, repo, "test-embed", 8, "")
				if err != nil || found != run || lexical != tt.want {
					t.Errorf("ActiveGenerationFor = %s, %v, %v; want %s, %v", found, lexical, err, run, tt.want)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
