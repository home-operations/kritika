//go:build integration

package store

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/configfile"
)

// TestANNPlan: the recall benchmark's check of its approximate search
// passes a plan that reads the VectorChord index, and refuses one that reads
// none, as an exact scan of a generation does, even beside the exclusions'
// B-tree, which an Index Scan of its own reads.
func TestANNPlan(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, soloAccount("ann-plan"))); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnsureIndexSchema(ctx, "kritika_app", "test-embed", 8, ""); err != nil {
		t.Fatal(err)
	}
	account := accountID(t, s, "ann-plan")
	repo := configfile.RepositoryID(account, "ann/plan")
	var run, runner string
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = s.owner.Exec(ctx, `DELETE FROM runner_runs WHERE account_id = $1`, account)
		_, _ = s.owner.Exec(ctx, `UPDATE repositories SET active_index_run_id = NULL; DROP TABLE IF EXISTS index_chunks; DELETE FROM index_schema`)
		_, _ = s.owner.Exec(ctx, `DELETE FROM index_runs WHERE repository_id = $1`, repo)
		_, _ = s.owner.Exec(ctx, `DELETE FROM repositories WHERE id = $1`, repo)
	})
	if _, err := s.owner.Exec(ctx, `INSERT INTO repositories (id, account_id, name) VALUES ($1, $2, 'ann/plan')`, repo, account); err != nil {
		t.Fatal(err)
	}
	if err := s.owner.QueryRow(ctx, `INSERT INTO index_runs (account_id, repository_id, commit_sha, embed_model, embed_dims, mode, status)
		VALUES ($1, $2, 'abc', 'test-embed', 8, 'full', 'completed') RETURNING id`, account, repo).Scan(&run); err != nil {
		t.Fatal(err)
	}
	if _, err := s.owner.Exec(ctx, `INSERT INTO index_chunks (account_id, repository_id, index_run_id, path, start_line, end_line, text, embedding)
		SELECT $1, $2, $3, 'f/' || i || '.go', 1, 1, 'package main', '[1,0,0,0,0,0,0,0]' FROM generate_series(1, 100) AS i`,
		account, repo, run); err != nil {
		t.Fatal(err)
	}
	if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		var err error
		if runner, err = InsertRunnerRun(ctx, tx, account, RunnerKindReview, "", 0); err != nil {
			return err
		}
		return SetSimilarExclude(ctx, tx, runner, []string{"f/1.go"})
	}); err != nil {
		t.Fatal(err)
	}
	q := []float32{1, 0, 0, 0, 0, 0, 0, 0}
	for _, tt := range []struct {
		name    string
		session string
		ann     bool
	}{
		{"vector index", `SET LOCAL enable_seqscan = off; SET LOCAL enable_sort = off`, true},
		{"other indexes", `SET LOCAL enable_seqscan = off; SET LOCAL vchordrq.enable_scan = off`, false},
		{"exact scan", `SET LOCAL enable_indexscan = off; SET LOCAL enable_bitmapscan = off`, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
				if _, err := tx.Exec(ctx, tt.session); err != nil {
					return err
				}
				err := annPlan(ctx, tx, run, q, Exclusions{Run: runner})
				if tt.ann && err != nil {
					t.Errorf("annPlan = %v, want nil", err)
				}
				if !tt.ann && !errors.Is(err, errNoVectorIndex) {
					t.Errorf("annPlan = %v, want %v", err, errNoVectorIndex)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
