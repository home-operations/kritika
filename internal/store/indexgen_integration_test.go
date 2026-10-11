//go:build integration

package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/configfile"
)

func TestGenerationPublication(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, soloAccount("generation"))); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnsureIndexSchema(ctx, "kritika_app", "test-embed", 8); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = s.owner.Exec(context.Background(), `UPDATE repositories SET active_index_run_id = NULL;
   DROP TABLE IF EXISTS index_chunks; DELETE FROM index_schema`)
	})
	account := accountID(t, s, "generation")
	for _, order := range []string{"full first", "incremental first", "no initial generation", "concurrent", "concurrent incremental first"} {
		t.Run(order, func(t *testing.T) {
			repository := configfile.RepositoryID(account, order)
			if _, err := s.owner.Exec(ctx, `INSERT INTO repositories (id, account_id, name, managed_by) VALUES ($1, $2, $3, 'file')`, repository, account, order); err != nil {
				t.Fatal(err)
			}
			makeRun := func(commit, mode, status string) string {
				t.Helper()
				var id string
				if err := s.owner.QueryRow(ctx, `INSERT INTO index_runs (account_id, repository_id, commit_sha, embed_model, embed_dims, mode, status)
     VALUES ($1, $2, $3, 'test-embed', 8, $4, $5) RETURNING id`, account, repository, commit, mode, status).Scan(&id); err != nil {
					t.Fatal(err)
				}
				if _, err := s.owner.Exec(ctx, `INSERT INTO index_chunks (account_id, repository_id, index_run_id, path, start_line, end_line, text, embedding)
     VALUES ($1, $2, $3, 'main.go', 1, 1, $4, '[1,0,0,0,0,0,0,0]')`, account, repository, id, commit); err != nil {
					t.Fatal(err)
				}
				return id
			}
			full := makeRun("A", IndexModeFull, "running")
			var initial *Generation
			if order != "no initial generation" {
				initial = &Generation{ID: makeRun("A", IndexModeFull, "completed"), Commit: "A"}
				if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error { return ActivateGeneration(ctx, tx, repository, initial.ID, nil) }); err != nil {
					t.Fatal(err)
				}
			}
			step := makeRun("B", IndexModeIncremental, "running")
			publishFull := func(tx pgx.Tx) error {
				if err := ActivateGeneration(ctx, tx, repository, full, initial); err != nil {
					return err
				}
				return FinishIndexRun(ctx, tx, full, IndexCompleted, 1, "")
			}
			publishStep := func(tx pgx.Tx) error {
				return AdvanceGeneration(ctx, tx, repository, *initial, step, "B", []string{"main.go"})
			}
			wantID, wantCommit := full, "A"
			var stale error
			switch order {
			case "incremental first":
				if err := s.WithAccount(ctx, account, publishStep); err != nil {
					t.Fatal(err)
				}
				stale = s.WithAccount(ctx, account, publishFull)
				wantID, wantCommit = initial.ID, "B"
			case "concurrent":
				stale = publishConcurrently(t, s, account, publishFull, publishStep)
			case "concurrent incremental first":
				stale = publishConcurrently(t, s, account, publishStep, publishFull)
				wantID, wantCommit = initial.ID, "B"
			default:
				if err := s.WithAccount(ctx, account, publishFull); err != nil {
					t.Fatal(err)
				}
				if initial == nil {
					stale = s.WithAccount(ctx, account, func(tx pgx.Tx) error { return ActivateGeneration(ctx, tx, repository, step, nil) })
				} else {
					stale = s.WithAccount(ctx, account, publishStep)
				}
			}
			if !errors.Is(stale, ErrGenerationChanged) {
				t.Fatalf("stale publication = %v, want ErrGenerationChanged", stale)
			}
			var id, commit, text string
			if err := s.owner.QueryRow(ctx, `SELECT r.active_index_run_id::text, g.commit_sha, c.text FROM repositories r
    JOIN index_runs g ON g.id = r.active_index_run_id JOIN index_chunks c ON c.index_run_id = g.id WHERE r.id = $1`, repository).Scan(&id, &commit, &text); err != nil {
				t.Fatal(err)
			}
			if id != wantID || commit != wantCommit || text != wantCommit {
				t.Fatalf("active = %s/%s/%s, want %s/%s", id, commit, text, wantID, wantCommit)
			}
			if initial != nil && wantID != initial.ID {
				var n int
				if err := s.owner.QueryRow(ctx, `SELECT count(*) FROM index_chunks WHERE index_run_id = $1`, initial.ID).Scan(&n); err != nil {
					t.Fatal(err)
				}
				if n != 0 {
					t.Fatalf("superseded generation retained %d chunks", n)
				}
			}
		})
	}
}

func publishConcurrently(t *testing.T, s *Store, account string, publishFirst, publishSecond func(pgx.Tx) error) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	locked, release := make(chan struct{}), make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	first, second := make(chan error, 1), make(chan error, 1)
	go func() {
		first <- s.WithAccount(ctx, account, func(tx pgx.Tx) error {
			if err := publishFirst(tx); err != nil {
				return err
			}
			close(locked)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	select {
	case <-locked:
	case err := <-first:
		t.Fatalf("first publication: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	pid := make(chan int, 1)
	go func() {
		second <- s.WithAccount(ctx, account, func(tx pgx.Tx) error {
			var backend int
			if err := tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&backend); err != nil {
				return err
			}
			pid <- backend
			return publishSecond(tx)
		})
	}()
	var backend int
	select {
	case backend = <-pid:
	case err := <-second:
		t.Fatalf("second publication: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		if err := s.owner.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_locks WHERE pid = $1 AND NOT granted)`, backend).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal("second publication did not wait for the repository lock")
		}
	}
	close(release)
	released = true
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	return <-second
}
