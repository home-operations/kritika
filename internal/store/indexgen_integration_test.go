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
	tests := []struct {
		name string
		// first publishes before second; concurrent holds first's
		// transaction open until second waits on the repository lock.
		first, second string
		concurrent    bool
		// stale is whether second is refused; active is the run left
		// active at commit, and superseded the generation it replaced.
		stale                      bool
		active, commit, superseded string
	}{
		{name: "update after a full build", first: "full", second: "update", stale: true, active: "full", commit: "A", superseded: "initial"},
		{name: "full build after an update", first: "update", second: "full", active: "full", commit: "A", superseded: "initial"},
		{name: "two full builds", first: "full", second: "rebuild", active: "rebuild", commit: "B", superseded: "full"},
		{name: "update waits for a full build", first: "full", second: "update", concurrent: true, stale: true, active: "full", commit: "A", superseded: "initial"},
		{name: "full build waits for an update", first: "update", second: "full", concurrent: true, active: "full", commit: "A", superseded: "initial"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repository := configfile.RepositoryID(account, tt.name)
			if _, err := s.owner.Exec(ctx, `INSERT INTO repositories (id, account_id, name, managed_by) VALUES ($1, $2, $3, 'file')`, repository, account, tt.name); err != nil {
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
			runs := map[string]string{
				"initial": makeRun("A", IndexModeFull, "completed"),
				"full":    makeRun("A", IndexModeFull, "running"),
				"rebuild": makeRun("B", IndexModeFull, "running"),
				"update":  makeRun("B", IndexModeIncremental, "running"),
			}
			if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error { return ActivateGeneration(ctx, tx, repository, runs["initial"]) }); err != nil {
				t.Fatal(err)
			}
			publish := func(name string) func(pgx.Tx) error {
				return func(tx pgx.Tx) error {
					if name == "update" {
						return AdvanceGeneration(ctx, tx, repository, Generation{ID: runs["initial"], Commit: "A"}, runs[name], "B", []string{"main.go"})
					}
					if err := ActivateGeneration(ctx, tx, repository, runs[name]); err != nil {
						return err
					}
					return FinishIndexRun(ctx, tx, runs[name], IndexCompleted, 1, "")
				}
			}
			var err error
			if tt.concurrent {
				err = publishConcurrently(t, s, account, publish(tt.first), publish(tt.second))
			} else {
				if err := s.WithAccount(ctx, account, publish(tt.first)); err != nil {
					t.Fatal(err)
				}
				err = s.WithAccount(ctx, account, publish(tt.second))
			}
			if errors.Is(err, ErrGenerationChanged) != tt.stale || (!tt.stale && err != nil) {
				t.Fatalf("second publication = %v, want stale %v", err, tt.stale)
			}
			var id, commit, text string
			if err := s.owner.QueryRow(ctx, `SELECT r.active_index_run_id::text, g.commit_sha, c.text FROM repositories r
    JOIN index_runs g ON g.id = r.active_index_run_id JOIN index_chunks c ON c.index_run_id = g.id WHERE r.id = $1`, repository).Scan(&id, &commit, &text); err != nil {
				t.Fatal(err)
			}
			if id != runs[tt.active] || commit != tt.commit || text != tt.commit {
				t.Fatalf("active = %s/%s/%s, want %s/%s", id, commit, text, runs[tt.active], tt.commit)
			}
			var status string
			var n int
			if err := s.owner.QueryRow(ctx, `SELECT status, (SELECT count(*) FROM index_chunks WHERE index_run_id = $1) FROM index_runs WHERE id = $1`,
				runs[tt.superseded]).Scan(&status, &n); err != nil {
				t.Fatal(err)
			}
			if status != string(IndexSuperseded) || n != 0 {
				t.Fatalf("replaced generation = %s with %d chunks, want superseded with none", status, n)
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
