//go:build integration

package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/model"
)

func TestGenerationPublication(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, soloAccount("generation"))); err != nil {
		t.Fatal(err)
	}
	listRepos(t, s, "generation", "generation/one")
	if _, err := s.EnsureIndexSchema(ctx, "kritika_app", "test-embed", 8, ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = s.owner.Exec(context.Background(), `UPDATE repositories SET active_index_run_id = NULL;
   DROP TABLE IF EXISTS index_chunks; DELETE FROM index_schema`)
	})
	account := accountID(t, s, "generation")
	tests := []struct {
		name string
		// first publishes before second. concurrent holds first's
		// transaction open until second waits on the repository lock.
		first, second string
		concurrent    bool
		// stale is whether second is refused. active is the run left
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
			if _, err := s.owner.Exec(ctx, `INSERT INTO repositories (id, account_id, name) VALUES ($1, $2, $3)`, repository, account, tt.name); err != nil {
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

func TestFinishIndexRunPreservesTerminalStatus(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, soloAccount("finish-index"))); err != nil {
		t.Fatal(err)
	}
	listRepos(t, s, "finish-index", "finish-index/one")
	account := accountID(t, s, "finish-index")
	for _, tt := range []struct {
		status     IndexRunStatus
		wantStatus IndexRunStatus
		wantChunks int
		wantError  string
	}{
		{IndexRunning, IndexFailed, 7, "commit acknowledgement lost"},
		{IndexCompleted, IndexCompleted, 7, ""},
		{IndexSuperseded, IndexSuperseded, 7, ""},
		{IndexFailed, IndexFailed, 7, ""},
	} {
		t.Run(string(tt.status), func(t *testing.T) {
			repo := configfile.RepositoryID(account, t.Name())
			if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
				if _, err := tx.Exec(ctx, `INSERT INTO repositories(id, account_id, name) VALUES($1, $2, $3)`, repo, account, t.Name()); err != nil {
					return err
				}
				var run string
				if err := tx.QueryRow(ctx, `INSERT INTO index_runs(account_id, repository_id, commit_sha, embed_model, embed_dims, mode, status, chunk_count, finished_at)
					VALUES($1, $2, 'abc', 'test-embed', 8, 'full', $3, 7, CASE WHEN $3 = 'running' THEN NULL ELSE '2020-01-01'::timestamptz END) RETURNING id`,
					account, repo, tt.status).Scan(&run); err != nil {
					return err
				}
				// A terminal record can reflect a successful COMMIT whose
				// acknowledgement never reached the worker doing failure cleanup.
				if err := FinishIndexRun(ctx, tx, run, IndexFailed, 0, "commit acknowledgement lost"); err != nil {
					return err
				}
				var status IndexRunStatus
				var chunks int
				var reason string
				var finished time.Time
				if err := tx.QueryRow(ctx, `SELECT status, chunk_count, error, finished_at FROM index_runs WHERE id = $1`, run).
					Scan(&status, &chunks, &reason, &finished); err != nil {
					return err
				}
				if status != tt.wantStatus || chunks != tt.wantChunks || reason != tt.wantError {
					t.Errorf("finished run = %s/%d/%q, want %s/%d/%q", status, chunks, reason, tt.wantStatus, tt.wantChunks, tt.wantError)
				}
				if tt.status != IndexRunning && !finished.Equal(time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)) {
					t.Errorf("terminal finished_at changed: %v", finished)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
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

// TestIndexVectors: a chunk's vector is found by its content hash within the
// run being built and the repository's active generation, and copied as
// stored. A chunk of the same hash in another run of the repository is not
// found, nor one in another repository, nor one in another account, which
// row-level security hides even when the query names its repository and
// run, nor a chunk without a hash. The lookup reads the two runs' partitions
// alone.
func TestIndexVectors(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	// Accounts of its own: the suites share one database, and others count
	// alpha's and beta's repositories.
	if err := s.ApplyConfig(ctx, parse(t, `
apps:
  vectors-bot:
    accounts: [vectors]
    clientId: Iv1.vectors
    privateKey: { env: KRITIKA_TEST_TOKEN }
    webhookSecret: { env: KRITIKA_TEST_TOKEN }
  strangers-bot:
    accounts: [strangers]
    clientId: Iv1.strangers
    privateKey: { env: KRITIKA_TEST_TOKEN }
    webhookSecret: { env: KRITIKA_TEST_TOKEN }
`)); err != nil {
		t.Fatal(err)
	}
	listRepos(t, s, "vectors", "vectors/one", "vectors/two")
	listRepos(t, s, "strangers", "strangers/one")
	if _, err := s.EnsureIndexSchema(ctx, "kritika_app", "test-embed", 8, ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = s.owner.Exec(context.Background(), `UPDATE repositories SET active_index_run_id = NULL;
   DROP TABLE IF EXISTS index_chunks; DELETE FROM index_schema`)
	})
	ours, theirs := accountID(t, s, "vectors"), accountID(t, s, "strangers")
	one, two, theirsOne := configfile.RepositoryID(ours, "vectors/one"), configfile.RepositoryID(ours, "vectors/two"),
		configfile.RepositoryID(theirs, "strangers/one")
	// A vector that halfvec stores exactly, which a round trip through text
	// or float32 must keep bit for bit.
	stored := []float32{0.1, -0.333, 1e-3, 65504, 0, 0.5, -2, 3.14159}
	newRun := func(account, repository string) string {
		t.Helper()
		var run string
		if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `INSERT INTO index_runs (account_id, repository_id, commit_sha, embed_model, embed_dims, mode, status)
				VALUES ($1, $2, 'abc', 'test-embed', 8, 'full', 'running') RETURNING id`, account, repository).Scan(&run)
		}); err != nil {
			t.Fatal(err)
		}
		return run
	}
	insert := func(account, repository, run string, hash []byte, vector []float32) {
		t.Helper()
		if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
			return InsertIndexChunks(ctx, tx, account, repository, run,
				[]StagedChunk{{Path: "main.go", Text: "text", ContentHash: hash}}, [][]float32{vector})
		}); err != nil {
			t.Fatal(err)
		}
	}
	shared, other, copied := []byte("shared-hash-0123456789abcdef0123"), []byte("other-hash-0123456789abcdef01234"), []byte("copied-hash-0123456789abcdef0123")
	built, active, abandoned := newRun(ours, one), newRun(ours, one), newRun(ours, one)
	unhashed := []byte("abandoned-hash-0123456789abcdef0")
	insert(ours, one, active, shared, stored)
	insert(ours, one, built, nil, []float32{1, 0, 0, 0, 0, 0, 0, 0})
	insert(ours, one, abandoned, unhashed, []float32{0, 0, 0, 1, 0, 0, 0, 0})
	twoRun := newRun(ours, two)
	insert(ours, two, twoRun, other, []float32{0, 1, 0, 0, 0, 0, 0, 0})
	theirsRun := newRun(theirs, theirsOne)
	insert(theirs, theirsOne, theirsRun, shared, []float32{0, 0, 1, 0, 0, 0, 0, 0})
	lookup := func(account, repository, run, activeRun string, hashes ...[]byte) map[string][]float32 {
		t.Helper()
		var found map[string][]float32
		if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
			var err error
			found, err = IndexVectors(ctx, tx, repository, run, activeRun, hashes)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return found
	}
	want := halfvecRound(t, s, stored)
	found := lookup(ours, one, built, active, shared, other, unhashed, []byte("missing-hash-0123456789abcdef012"))
	if len(found) != 1 || !slices.Equal(found[string(shared)], want) {
		t.Fatalf("vectors/one found %v, want only the shared hash's stored vector %v", found, want)
	}
	if found := lookup(ours, one, built, "", shared); len(found) != 0 {
		t.Fatalf("vectors/one found %v without the active generation, want none", found)
	}
	// A run of another repository of the account, named as the active
	// generation, which row-level security does not hide.
	if found := lookup(ours, one, built, twoRun, other); len(found) != 0 {
		t.Fatalf("vectors/one found %v from vectors/two", found)
	}
	if found := lookup(ours, theirsOne, theirsRun, "", shared); len(found) != 0 {
		t.Fatalf("vectors found %v of strangers'", found)
	}
	if err := s.WithAccount(ctx, ours, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, "EXPLAIN "+indexVectorsSQL, one, built, active, [][]byte{shared})
		if err != nil {
			return err
		}
		plan, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		if text := strings.Join(plan, "\n"); strings.Count(text, " on index_chunks_") > 2 {
			return fmt.Errorf("the lookup read more than the two runs' partitions:\n%s", text)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// The copy is the stored vector exactly: inserted again from what was
	// read, it equals the source.
	insert(ours, one, built, copied, found[string(shared)])
	var equal bool
	if err := s.owner.QueryRow(ctx, `SELECT (SELECT embedding FROM index_chunks WHERE content_hash = $1 AND account_id = $3) =
		(SELECT embedding FROM index_chunks WHERE content_hash = $2)`, shared, copied, ours).Scan(&equal); err != nil {
		t.Fatal(err)
	}
	if !equal {
		t.Fatal("the copied vector differs from the one it was read from")
	}
}

// halfvecRound is v as halfvec stores it.
func halfvecRound(t *testing.T, s *Store, v []float32) []float32 {
	t.Helper()
	var out []float32
	if err := s.owner.QueryRow(t.Context(), `SELECT $1::halfvec::real[]`, model.VectorLiteral(v)).Scan(&out); err != nil {
		t.Fatal(err)
	}
	return out
}
