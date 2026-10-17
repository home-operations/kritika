//go:build integration

package store

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/configfile"
)

func TestSimilarWithoutExclusions(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, soloAccount("review-similar"))); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnsureIndexSchema(ctx, "kritika_app", "test-embed", 8, ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = s.owner.Exec(context.Background(), `UPDATE repositories SET active_index_run_id=NULL; DROP TABLE IF EXISTS index_chunks; DELETE FROM index_schema`)
	})
	account := accountID(t, s, "review-similar")
	repo := configfile.RepositoryID(account, "review/similar")
	if _, err := s.owner.Exec(ctx, `INSERT INTO repositories(id,account_id,name,managed_by) VALUES($1,$2,'review/similar','file')`, repo, account); err != nil {
		t.Fatal(err)
	}
	var run string
	if err := s.owner.QueryRow(ctx, `INSERT INTO index_runs(account_id,repository_id,commit_sha,embed_model,embed_dims,mode,status) VALUES($1,$2,'abc','test-embed',8,'full','completed') RETURNING id`, account, repo).Scan(&run); err != nil {
		t.Fatal(err)
	}
	if _, err := s.owner.Exec(ctx, `INSERT INTO index_chunks(account_id,repository_id,index_run_id,path,start_line,end_line,text,embedding) VALUES($1,$2,$3,'main.go',1,1,'package main','[1,0,0,0,0,0,0,0]')`, account, repo, run); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name    string
		exclude []string
		want    int
	}{{"empty", []string{}, 1}, {"omitted", nil, 1}, {"excluded", []string{"main.go"}, 0}} {
		t.Run(tt.name, func(t *testing.T) {
			err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
				hits, err := SimilarChunks(ctx, tx, run, [][]float32{{1, 0, 0, 0, 0, 0, 0, 0}}, tt.exclude, 4)
				if err == nil && len(hits) != tt.want {
					t.Errorf("got %d hits, want %d", len(hits), tt.want)
				}
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
	// Each hit says which query it is near.
	if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		v := []float32{1, 0, 0, 0, 0, 0, 0, 0}
		hits, err := SimilarChunks(ctx, tx, run, [][]float32{v, v}, nil, 4)
		if err == nil && (len(hits) != 2 || hits[0].Query != 0 || hits[1].Query != 1) {
			t.Errorf("hits = %+v; want one near each query, in their order", hits)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
