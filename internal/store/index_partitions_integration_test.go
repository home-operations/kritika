//go:build integration

package store

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/runspec"
)

// createUnpartitionedIndexChunks creates index_chunks as a build before
// partitioning did, a plain table, with the lexemes that migration 0065
// adds, and records its index schema. With index false it leaves out the
// vector index, to be built once rows are loaded.
// With hash false it leaves out the content hash, as a leader of a build
// before content hashes recreated the table for a new embedder.
func createUnpartitionedIndexChunks(t testing.TB, s *Store, model string, dims int, index, hash bool) {
	t.Helper()
	ctx := t.Context()
	hashColumn := ""
	if hash {
		hashColumn = "content_hash  bytea,"
	}
	stmts := []string{
		fmt.Sprintf(`CREATE TABLE index_chunks (
			id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
			account_id     uuid        NOT NULL REFERENCES accounts (id),
			repository_id uuid        NOT NULL REFERENCES repositories (id),
			index_run_id  uuid        NOT NULL REFERENCES index_runs (id) ON DELETE CASCADE,
			path          text        NOT NULL,
			start_line    int         NOT NULL,
			end_line      int         NOT NULL,
			language      text        NOT NULL DEFAULT '',
			symbol        text        NOT NULL DEFAULT '',
			kind          text        NOT NULL DEFAULT '',
			scope         text        NOT NULL DEFAULT '',
			text          text        NOT NULL,
			embedding     halfvec(%d) NOT NULL,
			%s
			lexemes       tsvector,
			created_at    timestamptz NOT NULL DEFAULT now()
		)`, dims, hashColumn),
		`CREATE INDEX index_chunks_run_path_idx ON index_chunks (index_run_id, path)`,
		`CREATE INDEX index_chunks_account_id_idx ON index_chunks (account_id)`,
		`ALTER TABLE index_chunks ENABLE ROW LEVEL SECURITY`,
		`CREATE POLICY account_isolation ON index_chunks TO kritika_app
			USING      (account_id = NULLIF(current_setting('app.account_id', true), '')::uuid)
			WITH CHECK (account_id = NULLIF(current_setting('app.account_id', true), '')::uuid)`,
		`GRANT SELECT, INSERT, UPDATE, DELETE ON index_chunks TO kritika_app`,
	}
	if hash {
		stmts = append(stmts, contentHashIndex)
	}
	if index {
		stmts = append(stmts, `CREATE INDEX index_chunks_embedding_idx ON index_chunks USING vchordrq (embedding halfvec_cosine_ops)`)
	}
	for _, stmt := range stmts {
		if _, err := s.owner.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if _, err := s.owner.Exec(ctx, `INSERT INTO index_schema (id, embed_model, embed_dims, document_prompt, index_format)
		VALUES (1, $1, $2, '', $3)`, model, dims, runspec.IndexFormat); err != nil {
		t.Fatal(err)
	}
}

// dropIndexChunks leaves no index schema behind for the suites that share
// the database.
func dropIndexChunks(t *testing.T, s *Store) {
	t.Cleanup(func() {
		_, _ = s.owner.Exec(context.Background(), `UPDATE repositories SET active_index_run_id = NULL;
			DROP TABLE IF EXISTS index_chunks; DROP TABLE IF EXISTS index_chunks_legacy; DELETE FROM index_schema`)
	})
}

// partitionRun records a completed generation of the repository, published
// when active is set, with a chunk at each path, its vector the path's index
// along the first axis.
func partitionRun(t *testing.T, s *Store, account, repo string, active bool, paths ...string) string {
	t.Helper()
	ctx := t.Context()
	var run string
	if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO index_runs (account_id, repository_id, commit_sha, embed_model, embed_dims, mode, status)
			VALUES ($1, $2, 'abc', 'test-embed', 8, 'full', 'completed') RETURNING id`, account, repo).Scan(&run); err != nil {
			return err
		}
		chunks := make([]StagedChunk, len(paths))
		vectors := make([][]float32, len(paths))
		for i, p := range paths {
			chunks[i] = StagedChunk{Path: p, StartLine: 1, EndLine: 2, Text: "package main"}
			vectors[i] = []float32{1, float32(i), 0, 0, 0, 0, 0, 0}
		}
		if err := InsertIndexChunks(ctx, tx, account, repo, run, chunks, vectors); err != nil {
			return err
		}
		if active {
			return ActivateGeneration(ctx, tx, repo, run)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return run
}

// similarPaths is what the application's search of the generation finds.
func similarPaths(t *testing.T, s *Store, account, run string) []string {
	t.Helper()
	ctx := t.Context()
	var paths []string
	if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		hits, err := SimilarChunks(ctx, tx, run, [][]float32{{1, 0, 0, 0, 0, 0, 0, 0}}, nil, 10)
		for _, h := range hits {
			paths = append(paths, h.Chunk.Path)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return paths
}

// lexicalPaths is what the application's search by name of the generation
// finds for main, which every chunk of partitionRun holds.
func lexicalPaths(t *testing.T, s *Store, account, run string) []string {
	t.Helper()
	ctx := t.Context()
	var paths []string
	if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		q := LexicalQuery{Terms: []LexicalTerm{{Lexeme: "main"}}, Vector: []float32{1, 0, 0, 0, 0, 0, 0, 0}}
		hits, err := LexicalChunks(ctx, tx, run, []LexicalQuery{q}, nil, 10, func(int) int { return 100 }, 100)
		for _, h := range hits {
			paths = append(paths, h.Chunk.Path)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return paths
}

// partitionsRead are the partitions of index_chunks that a plan reads.
func partitionsRead(plan []string) []string {
	var out []string
	for _, m := range partitionRe.FindAllStringSubmatch(strings.Join(plan, "\n"), -1) {
		if !slices.Contains(out, m[1]) {
			out = append(out, m[1])
		}
	}
	return out
}

var partitionRe = regexp.MustCompile(` on (index_chunks_\w+)`)

// chunkPartitions counts the generation's chunks in each partition that
// holds some.
func chunkPartitions(t *testing.T, s *Store, run string) map[string]int {
	t.Helper()
	rows, err := s.owner.Query(t.Context(), `SELECT tableoid::regclass::text, count(*)::int FROM index_chunks
		WHERE index_run_id = $1 GROUP BY 1`, run)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]int{}
	var name string
	var n int
	if _, err := pgx.ForEachRow(rows, []any{&name, &n}, func() error {
		out[name] = n
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

// appDenied makes sure that the application role cannot read the table
// directly, only through index_chunks.
func appDenied(t *testing.T, s *Store, account, table string) {
	t.Helper()
	ctx := t.Context()
	err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `SELECT 1 FROM `+table+` LIMIT 1`)
		return err
	})
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
		t.Fatalf("the application role read %s directly: %v", table, err)
	}
}

// TestIndexChunksPartitioned: a new index_chunks is hash partitioned by
// generation, with the vector index on every partition. The application role
// writes, searches and moves chunks through it. A search reads the
// generation's partition alone, and the role cannot read a partition
// directly.
func TestIndexChunksPartitioned(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, soloAccount("index-parts"))); err != nil {
		t.Fatal(err)
	}
	listRepos(t, s, "index-parts", "index-parts/one")
	dropIndexChunks(t, s)
	if _, err := s.EnsureIndexSchema(ctx, "kritika_app", "test-embed", 8, ""); err != nil {
		t.Fatal(err)
	}
	var kind string
	var leaves, vectorIndexes int
	if err := s.owner.QueryRow(ctx, `SELECT
			(SELECT relkind::text FROM pg_class WHERE oid = 'index_chunks'::regclass),
			(SELECT count(*) FROM pg_inherits WHERE inhparent = 'index_chunks_current'::regclass),
			(SELECT count(*) FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid JOIN pg_am am ON am.oid = c.relam
			 JOIN pg_inherits h ON h.inhrelid = i.indrelid AND h.inhparent = 'index_chunks_current'::regclass
			 WHERE am.amname = 'vchordrq')`).Scan(&kind, &leaves, &vectorIndexes); err != nil {
		t.Fatal(err)
	}
	if kind != "p" || leaves != indexChunkPartitions || vectorIndexes != indexChunkPartitions {
		t.Fatalf("index_chunks kind %q with %d hash partitions and %d vector indexes; want partitioned, %d of each",
			kind, leaves, vectorIndexes, indexChunkPartitions)
	}
	checkFreshLayout(t, s)
	account := accountID(t, s, "index-parts")
	repo := configfile.RepositoryID(account, "index-parts/one")
	run := partitionRun(t, s, account, repo, true, "a.go", "b.go")
	if got := similarPaths(t, s, account, run); strings.Join(got, ",") != "a.go,b.go" {
		t.Fatalf("search found %v, want a.go,b.go", got)
	}
	if plan := searchPlan(t, s, account, run); strings.Count(plan, " on index_chunks_") != 1 {
		t.Fatalf("the search did not read one partition alone:\n%s", plan)
	}
	if got := lexicalPaths(t, s, account, run); strings.Join(got, ",") != "a.go,b.go" {
		t.Fatalf("search by name found %v, want a.go,b.go", got)
	}
	if plan := lexicalPlan(t, s, account, run); len(partitionsRead(plan)) != 1 {
		t.Fatalf("the search by name read %v, not one partition alone:\n%s", partitionsRead(plan), strings.Join(plan, "\n"))
	}
	appDenied(t, s, account, "index_chunks_00")
	// An incremental step's chunks move to the generation's partition.
	step := partitionRun(t, s, account, repo, false, "c.go")
	if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE index_chunks SET index_run_id = $2 WHERE index_run_id = $1`, step, run)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if got := similarPaths(t, s, account, run); len(got) != 3 {
		t.Fatalf("search after the step found %v, want three chunks", got)
	}
	if parts := chunkPartitions(t, s, run); len(parts) != 1 {
		t.Fatalf("the generation is in %v, want one partition", parts)
	}
	// Deleting the generation deletes its chunks.
	if _, err := s.owner.Exec(ctx, `UPDATE repositories SET active_index_run_id = NULL WHERE id = $1`, repo); err != nil {
		t.Fatal(err)
	}
	if _, err := s.owner.Exec(ctx, `DELETE FROM index_runs WHERE id = $1`, run); err != nil {
		t.Fatal(err)
	}
	if parts := chunkPartitions(t, s, run); len(parts) != 0 {
		t.Fatalf("chunks of a deleted generation are left in %v", parts)
	}
}

// partitionBehindWriter checks that, behind a writer that holds the plain
// index_chunks past the lock timeout, the sweep gives up and leaves the table
// as it was.
func partitionBehindWriter(t *testing.T, s *Store) {
	t.Helper()
	ctx := t.Context()
	writer, err := s.owner.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Exec(ctx, `LOCK TABLE index_chunks IN ROW EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	if got, err := s.PartitionIndexChunks(ctx, "kritika_app"); err == nil || got {
		t.Fatalf("PartitionIndexChunks behind a writer = %v, %v; want a lock timeout", got, err)
	}
	if err := writer.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
}

// partitionLegacy partitions the plain index_chunks as the retention sweep
// does and checks that its chunks, indexes and foreign keys became the
// legacy partition's.
func partitionLegacy(t *testing.T, s *Store) {
	t.Helper()
	ctx := t.Context()
	for i, want := range []bool{true, false} {
		if got, err := s.PartitionIndexChunks(ctx, "kritika_app"); err != nil || got != want {
			t.Fatalf("PartitionIndexChunks call %d = %v, %v; want %v", i+1, got, err, want)
		}
	}
	var kind string
	var attached bool
	var indexes, keys int
	if err := s.owner.QueryRow(ctx, `SELECT
			(SELECT relkind::text FROM pg_class WHERE oid = 'index_chunks'::regclass),
			EXISTS (SELECT 1 FROM pg_inherits WHERE inhrelid = 'index_chunks_legacy'::regclass AND inhparent = 'index_chunks'::regclass),
			(SELECT count(*) FROM pg_inherits h JOIN pg_index i ON i.indexrelid = h.inhrelid
			 WHERE i.indrelid = 'index_chunks_legacy'::regclass),
			(SELECT count(*) FROM pg_constraint WHERE conrelid = 'index_chunks_legacy'::regclass AND contype = 'f')`).
		Scan(&kind, &attached, &indexes, &keys); err != nil {
		t.Fatal(err)
	}
	if kind != "p" || !attached || indexes != 4 || keys != 3 {
		t.Fatalf("index_chunks kind %q, legacy attached %v with %d of its indexes and %d foreign keys; want partitioned, attached, 4 and 3",
			kind, attached, indexes, keys)
	}
}

// drainLegacy drains the legacy partition as the retention sweep does and
// returns how many chunks it moved and how many it deleted.
func drainLegacy(t *testing.T, s *Store) (moved, removed int64) {
	t.Helper()
	for {
		m, r, err := s.DrainLegacyIndexChunks(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if m+r == 0 {
			return moved, removed
		}
		moved, removed = moved+m, removed+r
	}
}

// legacyExists reports whether index_chunks_legacy exists.
func legacyExists(t *testing.T, s *Store) bool {
	t.Helper()
	var legacy bool
	if err := s.owner.QueryRow(t.Context(), `SELECT to_regclass('index_chunks_legacy') IS NOT NULL`).Scan(&legacy); err != nil {
		t.Fatal(err)
	}
	return legacy
}

// checkFreshLayout checks that index_chunks is laid out as a new one is: the
// hash partitions cover every created_at, and they keep the partitioned
// table's indexes and foreign keys, not copies of them.
func checkFreshLayout(t *testing.T, s *Store) {
	t.Helper()
	var bound string
	var partitions, indexes, keys, inherited int
	if err := s.owner.QueryRow(t.Context(), `SELECT
			(SELECT pg_get_expr(relpartbound, oid) FROM pg_class WHERE oid = 'index_chunks_current'::regclass),
			(SELECT count(*) FROM pg_inherits WHERE inhparent = 'index_chunks'::regclass),
			(SELECT count(*) FROM pg_inherits h JOIN pg_index i ON i.indexrelid = h.inhrelid
			 WHERE i.indrelid = 'index_chunks_current'::regclass),
			(SELECT count(*) FROM pg_constraint WHERE conrelid = 'index_chunks_current'::regclass AND contype = 'f'),
			(SELECT count(*) FROM pg_constraint WHERE conrelid = 'index_chunks_current'::regclass AND contype = 'f' AND conparentid <> 0)`).
		Scan(&bound, &partitions, &indexes, &keys, &inherited); err != nil {
		t.Fatal(err)
	}
	if bound != "FOR VALUES FROM (MINVALUE) TO (MAXVALUE)" || partitions != 1 || indexes != 4 || keys != 3 || inherited != 3 {
		t.Fatalf("index_chunks_current %s, one of %d partitions, with %d of the table's indexes and %d foreign keys, %d inherited;"+
			" want FROM (MINVALUE) TO (MAXVALUE), one of 1, 4 and 3, 3 inherited", bound, partitions, indexes, keys, inherited)
	}
}

// searchPlan is the plan of the application's search of the generation.
func searchPlan(t *testing.T, s *Store, account, run string) string {
	t.Helper()
	ctx := t.Context()
	var text string
	if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SET LOCAL vchordrq.prefilter = on`); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, "EXPLAIN "+similarChunksSQL, "[1,0,0,0,0,0,0,0]", run, []string{}, 4)
		if err != nil {
			return err
		}
		plan, err := pgx.CollectRows(rows, pgx.RowTo[string])
		text = strings.Join(plan, "\n")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return text
}

// lexicalPlan is the plan of the application's search by name of the
// generation.
func lexicalPlan(t *testing.T, s *Store, account, run string) []string {
	t.Helper()
	ctx := t.Context()
	var plan []string
	if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, "EXPLAIN "+lexicalChunksSQL, run, []int32{0}, []string{"main"}, []bool{false}, []string{"main"}, 2, 100,
			[]string{}, []string{"[1,0,0,0,0,0,0,0]"}, 4, 16, 100)
		if err != nil {
			return err
		}
		plan, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return plan
}

// waitForLockWait waits until a transaction waits for a lock of mode on the
// relation, or until done is closed.
func waitForLockWait(t *testing.T, s *Store, relation uint32, mode string, done <-chan struct{}) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-done:
			return
		default:
		}
		var waiting bool
		if err := s.owner.QueryRow(t.Context(), `SELECT EXISTS (SELECT 1 FROM pg_locks
			WHERE locktype = 'relation' AND relation = $1 AND mode = $2 AND NOT granted)`, relation, mode).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("no transaction waited for %s on relation %d", mode, relation)
}

// TestPartitionIndexChunksYieldsToPublication: a publication that holds its
// repository and then waits for index_chunks behind the partitioner, which
// then waits for repositories, publishes. The partitioner gives way, and
// partitions the table once the publication commits.
func TestPartitionIndexChunksYieldsToPublication(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, soloAccount("index-yield"))); err != nil {
		t.Fatal(err)
	}
	listRepos(t, s, "index-yield", "index-yield/one")
	dropIndexChunks(t, s)
	createUnpartitionedIndexChunks(t, s, "test-embed", 8, true, true)
	account := accountID(t, s, "index-yield")
	repo := configfile.RepositoryID(account, "index-yield/one")
	partitionRun(t, s, account, repo, true, "a.go")
	next := partitionRun(t, s, account, repo, false, "b.go")
	var table uint32
	if err := s.owner.QueryRow(ctx, `SELECT 'index_chunks'::regclass::oid`).Scan(&table); err != nil {
		t.Fatal(err)
	}

	// A writer holds index_chunks, so the partitioner queues for it.
	writer, err := s.owner.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.Rollback(context.Background()) }()
	if _, err := writer.Exec(ctx, `LOCK TABLE index_chunks IN ROW EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	partitioned := make(chan error, 1)
	go func() {
		_, err := s.PartitionIndexChunks(ctx, "kritika_app")
		partitioned <- err
	}()
	waitForLockWait(t, s, table, "AccessExclusiveLock", nil)
	// The publication updates the repository and then queues behind the
	// partitioner for index_chunks.
	published := make(chan error, 1)
	publishedDone := make(chan struct{})
	go func() {
		defer close(publishedDone)
		published <- s.WithAccount(ctx, account, func(tx pgx.Tx) error {
			return ActivateGeneration(ctx, tx, repo, next)
		})
	}()
	waitForLockWait(t, s, table, "RowExclusiveLock", publishedDone)
	if err := writer.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-published; err != nil {
		t.Fatalf("the publication failed beside the partitioner: %v", err)
	}
	if err := <-partitioned; err != nil {
		t.Logf("the partitioner gave way: %v", err)
	}
	if _, err := s.PartitionIndexChunks(ctx, "kritika_app"); err != nil {
		t.Fatal(err)
	}
	var kind string
	if err := s.owner.QueryRow(ctx, `SELECT relkind::text FROM pg_class WHERE oid = 'index_chunks'::regclass`).Scan(&kind); err != nil {
		t.Fatal(err)
	}
	if kind != "p" {
		t.Fatalf("index_chunks kind %q after the publication, want partitioned", kind)
	}
	if got := similarPaths(t, s, account, next); strings.Join(got, ",") != "b.go" {
		t.Fatalf("search of the published generation found %v, want b.go", got)
	}
}

// TestDropLegacyIndexChunksYieldsToSearch: a search that read repositories
// and then waits for index_chunks behind the drop, which then waits for
// repositories, finishes. The drop gives way, and drops the legacy partition
// once the search ends.
func TestDropLegacyIndexChunksYieldsToSearch(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, soloAccount("index-drop-yield"))); err != nil {
		t.Fatal(err)
	}
	listRepos(t, s, "index-drop-yield", "index-drop-yield/one")
	dropIndexChunks(t, s)
	createUnpartitionedIndexChunks(t, s, "test-embed", 8, true, true)
	account := accountID(t, s, "index-drop-yield")
	repo := configfile.RepositoryID(account, "index-drop-yield/one")
	run := partitionRun(t, s, account, repo, true, "a.go")
	partitionLegacy(t, s)
	drainLegacy(t, s)
	var table uint32
	if err := s.owner.QueryRow(ctx, `SELECT 'index_chunks'::regclass::oid`).Scan(&table); err != nil {
		t.Fatal(err)
	}

	// A writer holds index_chunks, so the drop queues for it.
	writer, err := s.owner.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.Rollback(context.Background()) }()
	if _, err := writer.Exec(ctx, `LOCK TABLE index_chunks IN ROW EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	dropped := make(chan error, 1)
	go func() {
		_, err := s.DropLegacyIndexChunks(ctx)
		dropped <- err
	}()
	waitForLockWait(t, s, table, "AccessExclusiveLock", nil)
	searched := make(chan error, 1)
	searchedDone := make(chan struct{})
	go func() {
		defer close(searchedDone)
		searched <- s.WithAccount(ctx, account, func(tx pgx.Tx) error {
			var n int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM repositories`).Scan(&n); err != nil {
				return err
			}
			return tx.QueryRow(ctx, `SELECT count(*) FROM index_chunks WHERE index_run_id = $1`, run).Scan(&n)
		})
	}()
	waitForLockWait(t, s, table, "AccessShareLock", searchedDone)
	if err := writer.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-searched; err != nil {
		t.Fatalf("the search failed beside the drop: %v", err)
	}
	if err := <-dropped; err != nil {
		t.Logf("the drop gave way: %v", err)
	}
	if _, err := s.DropLegacyIndexChunks(ctx); err != nil {
		t.Fatal(err)
	}
	if legacyExists(t, s) {
		t.Fatal("the empty legacy partition was kept")
	}
	checkFreshLayout(t, s)
	if got := similarPaths(t, s, account, run); strings.Join(got, ",") != "a.go" {
		t.Fatalf("search after the drop found %v, want a.go", got)
	}
}

// TestPartitionIndexChunksFromLegacy: the retention sweep makes an
// index_chunks of a build before partitioning the legacy partition of a
// partitioned one, keeping its chunks, indexes and foreign keys. New chunks
// go to the hash partitions. The drain moves the chunks that a build needs
// there, the active generations' first, and deletes the others. The empty
// legacy partition is then dropped, and a search reads its generation's
// partition alone.
func TestPartitionIndexChunksFromLegacy(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, soloAccount("index-legacy"))); err != nil {
		t.Fatal(err)
	}
	listRepos(t, s, "index-legacy", "index-legacy/one", "index-legacy/two")
	dropIndexChunks(t, s)
	createUnpartitionedIndexChunks(t, s, "test-embed", 8, true, true)
	account := accountID(t, s, "index-legacy")
	one := configfile.RepositoryID(account, "index-legacy/one")
	two := configfile.RepositoryID(account, "index-legacy/two")
	runOne := partitionRun(t, s, account, one, true, "a.go", "b.go")
	stray := partitionRun(t, s, account, one, false, "old.go")
	deleted := partitionRun(t, s, account, one, false, "gone.go")
	resumable := partitionRun(t, s, account, two, false, "next.go")
	if _, err := s.owner.Exec(ctx, `UPDATE index_runs SET status = 'failed', resume_key = 'resume' WHERE id = $1`, resumable); err != nil {
		t.Fatal(err)
	}
	runTwo := partitionRun(t, s, account, two, true, "c.go")

	// The leader keeps the table for the same embedder.
	if _, err := s.EnsureIndexSchema(ctx, "kritika_app", "test-embed", 8, ""); err != nil {
		t.Fatalf("EnsureIndexSchema: %v", err)
	}
	partitionBehindWriter(t, s)
	partitionLegacy(t, s)
	if got := similarPaths(t, s, account, runOne); strings.Join(got, ",") != "a.go,b.go" {
		t.Fatalf("search of the legacy generation found %v, want a.go,b.go", got)
	}
	if got := lexicalPaths(t, s, account, runOne); strings.Join(got, ",") != "a.go,b.go" {
		t.Fatalf("search by name of the legacy generation found %v, want a.go,b.go", got)
	}
	for _, run := range []string{runOne, runTwo} {
		checkPostings(t, s, run)
		searchesMatch(t, s, account, run)
	}
	appDenied(t, s, account, "index_chunks_legacy")
	added := partitionRun(t, s, account, two, false, "d.go")
	if parts := chunkPartitions(t, s, added); len(parts) != 1 || parts["index_chunks_legacy"] != 0 {
		t.Fatalf("a new chunk is in %v, want a hash partition", parts)
	}
	// The legacy partition's foreign keys still cascade.
	if _, err := s.owner.Exec(ctx, `DELETE FROM index_runs WHERE id = $1`, deleted); err != nil {
		t.Fatal(err)
	}
	if parts := chunkPartitions(t, s, deleted); len(parts) != 0 {
		t.Fatalf("chunks of a deleted generation are left in %v", parts)
	}
	// Nothing is dropped while the legacy partition holds a chunk.
	if dropped, err := s.DropLegacyIndexChunks(ctx); err != nil || dropped {
		t.Fatalf("DropLegacyIndexChunks of a legacy partition with chunks = %v, %v", dropped, err)
	}

	if moved, removed := drainLegacy(t, s); moved != 4 || removed != 1 {
		t.Fatalf("drain moved %d and deleted %d chunks, want the active and resumable generations' 4 moved and the stray's 1 deleted",
			moved, removed)
	}
	for _, run := range []string{runOne, runTwo, resumable} {
		if parts := chunkPartitions(t, s, run); len(parts) != 1 || parts["index_chunks_legacy"] != 0 {
			t.Fatalf("generation %s is in %v after the drain, want a hash partition", run, parts)
		}
	}
	// The moved chunks keep their ids, and so their postings.
	for _, run := range []string{runOne, runTwo} {
		checkPostings(t, s, run)
		searchesMatch(t, s, account, run)
	}
	if parts := chunkPartitions(t, s, stray); len(parts) != 0 {
		t.Fatalf("an inactive generation is in %v after the drain, want it deleted", parts)
	}
	var postings int
	if err := s.owner.QueryRow(ctx, `SELECT count(*) FROM index_postings WHERE index_run_id = $1`, stray).Scan(&postings); err != nil || postings != 0 {
		t.Fatalf("the deleted generation keeps %d postings, %v; want none", postings, err)
	}
	if dropped, err := s.DropLegacyIndexChunks(ctx); err != nil || !dropped {
		t.Fatalf("DropLegacyIndexChunks of the empty legacy partition = %v, %v", dropped, err)
	}
	if legacyExists(t, s) {
		t.Fatal("the empty legacy partition was kept")
	}
	checkSearchAfterLegacyDrop(t, s, account, one, runOne)
	if moved, removed, err := s.DrainLegacyIndexChunks(ctx); err != nil || moved+removed != 0 {
		t.Fatalf("drain without a legacy partition = %d, %d, %v", moved, removed, err)
	}
	if dropped, err := s.DropLegacyIndexChunks(ctx); err != nil || dropped {
		t.Fatalf("DropLegacyIndexChunks without a legacy partition = %v, %v", dropped, err)
	}
}

// checkSearchAfterLegacyDrop checks that once the legacy partition is
// dropped, both searches read runOne's partition alone, the table has the
// layout of a fresh one, and a chunk dated before the dropped partition's
// bound still has a partition.
func checkSearchAfterLegacyDrop(t *testing.T, s *Store, account, repository, runOne string) {
	t.Helper()
	if got := similarPaths(t, s, account, runOne); strings.Join(got, ",") != "a.go,b.go" {
		t.Fatalf("search after the drain found %v, want a.go,b.go", got)
	}
	if plan := searchPlan(t, s, account, runOne); strings.Count(plan, " on index_chunks_") != 1 {
		t.Fatalf("the search after the drain did not read one partition alone:\n%s", plan)
	}
	if got := lexicalPaths(t, s, account, runOne); strings.Join(got, ",") != "a.go,b.go" {
		t.Fatalf("search by name after the drain found %v, want a.go,b.go", got)
	}
	if plan := lexicalPlan(t, s, account, runOne); len(partitionsRead(plan)) != 1 {
		t.Fatalf("the search by name after the drain read %v, not one partition alone:\n%s", partitionsRead(plan), strings.Join(plan, "\n"))
	}
	checkPostings(t, s, runOne)
	searchesMatch(t, s, account, runOne)
	checkFreshLayout(t, s)
	// A chunk created before the dropped partition's bound, as by a database
	// clock that stepped back, still has a partition.
	if _, err := s.owner.Exec(t.Context(), `INSERT INTO index_chunks
			(account_id, repository_id, index_run_id, path, start_line, end_line, text, embedding, created_at)
		VALUES ($1, $2, $3, 'early.go', 1, 2, 'package main', '[1,2,0,0,0,0,0,0]', now() - interval '1 day')`,
		account, repository, runOne); err != nil {
		t.Fatalf("insert of a chunk created before the legacy bound: %v", err)
	}
	if got := similarPaths(t, s, account, runOne); strings.Join(got, ",") != "a.go,b.go,early.go" {
		t.Fatalf("search after an early chunk found %v, want a.go,b.go,early.go", got)
	}
	if plan := searchPlan(t, s, account, runOne); strings.Count(plan, " on index_chunks_") != 1 {
		t.Fatalf("the search after an early chunk did not read one partition alone:\n%s", plan)
	}
}

// TestDrainLegacyIndexChunksInSlices: the drain moves a slice of a
// generation at a time, and a search reads the generation split between the
// legacy and hash partitions as one.
func TestDrainLegacyIndexChunksInSlices(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, soloAccount("index-slices"))); err != nil {
		t.Fatal(err)
	}
	listRepos(t, s, "index-slices", "index-slices/one")
	dropIndexChunks(t, s)
	createUnpartitionedIndexChunks(t, s, "test-embed", 8, true, true)
	account := accountID(t, s, "index-slices")
	repo := configfile.RepositoryID(account, "index-slices/one")
	run := partitionRun(t, s, account, repo, true, "a.go", "b.go", "c.go")
	partitionLegacy(t, s)
	slice := legacyDrainSlice
	legacyDrainSlice = 2
	t.Cleanup(func() { legacyDrainSlice = slice })

	if moved, removed, err := s.DrainLegacyIndexChunks(ctx); err != nil || moved != 2 || removed != 0 {
		t.Fatalf("first slice moved %d and deleted %d, %v; want 2 moved", moved, removed, err)
	}
	if parts := chunkPartitions(t, s, run); len(parts) != 2 || parts["index_chunks_legacy"] != 1 {
		t.Fatalf("the generation is in %v after a slice, want 1 chunk left in the legacy partition", parts)
	}
	if got := similarPaths(t, s, account, run); strings.Join(got, ",") != "a.go,b.go,c.go" {
		t.Fatalf("search of the split generation found %v, want a.go,b.go,c.go", got)
	}
	if moved, removed := drainLegacy(t, s); moved != 1 || removed != 0 {
		t.Fatalf("the rest of the drain moved %d and deleted %d, want 1 moved", moved, removed)
	}
}

// TestDrainLegacyIndexChunksBesidePublications: incremental steps and a full
// build publish while the drain moves the generation a chunk at a time. No
// chunk is lost or doubled.
func TestDrainLegacyIndexChunksBesidePublications(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, soloAccount("index-drain-race"))); err != nil {
		t.Fatal(err)
	}
	listRepos(t, s, "index-drain-race", "index-drain-race/one")
	dropIndexChunks(t, s)
	createUnpartitionedIndexChunks(t, s, "test-embed", 8, true, true)
	account := accountID(t, s, "index-drain-race")
	repo := configfile.RepositoryID(account, "index-drain-race/one")
	const files = 40
	paths := make([]string, files)
	for i := range paths {
		paths[i] = fmt.Sprintf("f%02d.go", i)
	}
	run := partitionRun(t, s, account, repo, true, paths...)
	partitionLegacy(t, s)
	slice := legacyDrainSlice
	legacyDrainSlice = 1
	t.Cleanup(func() { legacyDrainSlice = slice })

	drained := make(chan error, 1)
	go func() {
		drained <- func() error {
			for {
				moved, removed, err := s.DrainLegacyIndexChunks(ctx)
				if err != nil {
					return err
				}
				if moved+removed > 0 {
					continue
				}
				var left bool
				if err := s.owner.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM index_chunks_legacy)`).Scan(&left); err != nil || !left {
					return err
				}
				time.Sleep(time.Millisecond)
			}
		}()
	}()

	// Each step replaces one file's chunk with its own.
	active := Generation{ID: run, Commit: "abc"}
	want := map[string]string{}
	for i := range files / 2 {
		path, text, commit := paths[2*i], fmt.Sprintf("step %d", i), fmt.Sprintf("c%02d", i)
		var step string
		if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
			if err := tx.QueryRow(ctx, `INSERT INTO index_runs (account_id, repository_id, commit_sha, embed_model, embed_dims, mode, status)
				VALUES ($1, $2, $3, 'test-embed', 8, 'incremental', 'running') RETURNING id`, account, repo, commit).Scan(&step); err != nil {
				return err
			}
			return InsertIndexChunks(ctx, tx, account, repo, step,
				[]StagedChunk{{Path: path, StartLine: 1, EndLine: 2, Text: text}}, [][]float32{{1, 0, 0, 0, 0, 0, 0, 0}})
		}); err != nil {
			t.Fatal(err)
		}
		if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
			return AdvanceGeneration(ctx, tx, repo, active, step, commit, []string{path})
		}); err != nil {
			t.Fatal(err)
		}
		active.Commit = commit
		want[path] = text
	}
	rows, err := s.owner.Query(ctx, `SELECT path, text FROM index_chunks WHERE index_run_id = $1`, run)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	var path, text string
	n := 0
	if _, err := pgx.ForEachRow(rows, []any{&path, &text}, func() error {
		n++
		got[path] = text
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if n != files || len(got) != files {
		t.Fatalf("the generation holds %d chunks of %d paths after the steps, want one of each of %d", n, len(got), files)
	}
	for p, w := range want {
		if got[p] != w {
			t.Fatalf("%s holds %q after the steps, want %q", p, got[p], w)
		}
	}

	// A full build supersedes the generation, wherever its chunks are.
	full := partitionRun(t, s, account, repo, false, "new.go")
	if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error { return ActivateGeneration(ctx, tx, repo, full) }); err != nil {
		t.Fatal(err)
	}
	if err := <-drained; err != nil {
		t.Fatal(err)
	}
	if parts := chunkPartitions(t, s, run); len(parts) != 0 {
		t.Fatalf("the superseded generation is left in %v", parts)
	}
	if got := similarPaths(t, s, account, full); strings.Join(got, ",") != "new.go" {
		t.Fatalf("search of the full build found %v, want new.go", got)
	}
	if dropped, err := s.DropLegacyIndexChunks(ctx); err != nil || !dropped {
		t.Fatalf("DropLegacyIndexChunks after the drain = %v, %v", dropped, err)
	}
}

// TestPartitionIndexChunksAddsContentHash: a plain index_chunks that a
// leader of a build before content hashes recreated, without the column,
// gains it as it becomes the legacy partition. Its chunks keep no hash, and
// new chunks record theirs.
func TestPartitionIndexChunksAddsContentHash(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, soloAccount("index-unhashed"))); err != nil {
		t.Fatal(err)
	}
	listRepos(t, s, "index-unhashed", "index-unhashed/one")
	dropIndexChunks(t, s)
	createUnpartitionedIndexChunks(t, s, "test-embed", 8, true, false)
	account := accountID(t, s, "index-unhashed")
	repo := configfile.RepositoryID(account, "index-unhashed/one")
	var run string
	if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO index_runs (account_id, repository_id, commit_sha, embed_model, embed_dims, mode, status)
			VALUES ($1, $2, 'abc', 'test-embed', 8, 'full', 'completed') RETURNING id`, account, repo).Scan(&run); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO index_chunks (account_id, repository_id, index_run_id, path, start_line, end_line, text, embedding)
			VALUES ($1, $2, $3, 'a.go', 1, 2, 'package main', '[1,0,0,0,0,0,0,0]')`, account, repo, run); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE repositories SET active_index_run_id = $2 WHERE id = $1`, repo, run)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	partitionLegacy(t, s)
	var columns int
	if err := s.owner.QueryRow(ctx, `SELECT count(*) FROM pg_attribute WHERE attname = 'content_hash' AND NOT attisdropped
		AND attrelid IN ('index_chunks'::regclass, 'index_chunks_legacy'::regclass)`).Scan(&columns); err != nil {
		t.Fatal(err)
	}
	if columns != 2 {
		t.Fatalf("content_hash is on %d of index_chunks and its legacy partition, want both", columns)
	}
	if got := similarPaths(t, s, account, run); strings.Join(got, ",") != "a.go" {
		t.Fatalf("search of the legacy generation found %v, want a.go", got)
	}
	hash := []byte("unhashed-hash-0123456789abcdef01")
	step := partitionRun(t, s, account, repo, false)
	if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		if err := InsertIndexChunks(ctx, tx, account, repo, step,
			[]StagedChunk{{Path: "b.go", Text: "package main", ContentHash: hash}}, [][]float32{{0, 1, 0, 0, 0, 0, 0, 0}}); err != nil {
			return err
		}
		found, err := IndexVectors(ctx, tx, repo, step, run, [][]byte{hash})
		if err != nil {
			return err
		}
		if len(found) != 1 {
			return fmt.Errorf("IndexVectors found %v, want the new chunk's vector", found)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if moved, removed := drainLegacy(t, s); moved != 1 || removed != 0 {
		t.Fatalf("drain moved %d and deleted %d, want the generation's 1 moved", moved, removed)
	}
}

// TestRebuildDropsLegacyIndexChunks: a rebuild for a new embedder drops the
// legacy partition with the table.
func TestRebuildDropsLegacyIndexChunks(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, soloAccount("index-legacy-rebuild"))); err != nil {
		t.Fatal(err)
	}
	listRepos(t, s, "index-legacy-rebuild", "index-legacy-rebuild/one")
	dropIndexChunks(t, s)
	createUnpartitionedIndexChunks(t, s, "test-embed", 8, true, true)
	account := accountID(t, s, "index-legacy-rebuild")
	partitionRun(t, s, account, configfile.RepositoryID(account, "index-legacy-rebuild/one"), true, "a.go")
	partitionLegacy(t, s)
	if _, err := s.EnsureIndexSchema(ctx, "kritika_app", "test-embed", 16, ""); err != nil {
		t.Fatal(err)
	}
	if legacyExists(t, s) {
		t.Fatal("a rebuild for a new embedder kept the legacy partition")
	}
}

// executedRe matches a plan node that scans a partition of index_chunks, and
// whether it never ran.
var executedRe = regexp.MustCompile(` on (index_chunks_\w+).*?( \(never executed\))?$`)

// executedPartitions are the partitions of index_chunks that the
// application's search of the generation reads when it runs, under the
// plan_cache_mode mode. A prepared statement's generic plan prunes the other
// partitions as it starts, which only EXPLAIN ANALYZE shows.
func executedPartitions(t *testing.T, s *Store, account, run, mode string) ([]string, string) {
	t.Helper()
	ctx := t.Context()
	var plan []string
	if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		for _, stmt := range []string{
			`SET LOCAL vchordrq.prefilter = on`,
			`SET LOCAL plan_cache_mode = ` + mode,
			`PREPARE partition_search AS ` + similarChunksSQL,
		} {
			if _, err := tx.Exec(ctx, stmt); err != nil {
				return err
			}
		}
		rows, err := tx.Query(ctx, fmt.Sprintf(`EXPLAIN (ANALYZE, COSTS OFF, TIMING OFF, SUMMARY OFF)
			EXECUTE partition_search('[1,0,0,0,0,0,0,0]', '%s', '{}', 4)`, run))
		if err != nil {
			return err
		}
		if plan, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `DEALLOCATE partition_search`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var read []string
	for _, line := range plan {
		m := executedRe.FindStringSubmatch(line)
		if m != nil && m[2] == "" && !slices.Contains(read, m[1]) {
			read = append(read, m[1])
		}
	}
	return read, strings.Join(plan, "\n")
}

// TestSimilarSearchReadsOnePartition: a search reads its generation's hash
// partition alone, while other generations fill other partitions, both in a
// table created partitioned and in one converted from a plain table once its
// legacy partition is dropped. That holds for a custom plan, which prunes as
// it is planned, and for the generic plan that a prepared statement settles
// on, which prunes as it starts.
func TestSimilarSearchReadsOnePartition(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, soloAccount("index-prune"))); err != nil {
		t.Fatal(err)
	}
	listRepos(t, s, "index-prune", "index-prune/one", "index-prune/two")
	account := accountID(t, s, "index-prune")
	one := configfile.RepositoryID(account, "index-prune/one")
	two := configfile.RepositoryID(account, "index-prune/two")
	dropIndexChunks(t, s)
	for _, layout := range []string{"fresh", "converted"} {
		t.Run(layout, func(t *testing.T) {
			if _, err := s.owner.Exec(ctx, `UPDATE repositories SET active_index_run_id = NULL;
				DROP TABLE IF EXISTS index_chunks; DROP TABLE IF EXISTS index_chunks_legacy; DELETE FROM index_schema`); err != nil {
				t.Fatal(err)
			}
			var run string
			if layout == "fresh" {
				if _, err := s.EnsureIndexSchema(ctx, "kritika_app", "test-embed", 8, ""); err != nil {
					t.Fatal(err)
				}
				run = partitionRun(t, s, account, one, true, "a.go", "b.go")
			} else {
				createUnpartitionedIndexChunks(t, s, "test-embed", 8, true, true)
				run = partitionRun(t, s, account, one, true, "a.go", "b.go")
				partitionLegacy(t, s)
				drainLegacy(t, s)
				if dropped, err := s.DropLegacyIndexChunks(ctx); err != nil || !dropped {
					t.Fatalf("DropLegacyIndexChunks after the drain = %v, %v", dropped, err)
				}
			}
			// Other generations, in other partitions.
			occupied := map[string]bool{}
			for range 12 {
				for p := range chunkPartitions(t, s, partitionRun(t, s, account, two, false, "x.go")) {
					occupied[p] = true
				}
			}
			own := chunkPartitions(t, s, run)
			if len(own) != 1 {
				t.Fatalf("the generation is in %v, want one partition", own)
			}
			var partition string
			for p := range own {
				partition = p
			}
			delete(occupied, partition)
			if len(occupied) == 0 {
				t.Fatal("no other partition holds chunks, so the search could not read one")
			}
			if got := similarPaths(t, s, account, run); strings.Join(got, ",") != "a.go,b.go" {
				t.Fatalf("search found %v, want a.go,b.go", got)
			}
			for _, mode := range []string{"force_custom_plan", "force_generic_plan"} {
				read, plan := executedPartitions(t, s, account, run, mode)
				if !slices.Equal(read, []string{partition}) {
					t.Errorf("%s: the search read %v, want %s alone:\n%s", mode, read, partition, plan)
				}
			}
		})
	}
}

// TestPartitionIndexChunksRetriesLockWaits: a lock that a writer holds for
// longer than one short wait does not cost the sweep its turn. The
// partitioner gives way, tries again, and partitions the table once the
// writer lets go.
func TestPartitionIndexChunksRetriesLockWaits(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, soloAccount("index-retry"))); err != nil {
		t.Fatal(err)
	}
	listRepos(t, s, "index-retry", "index-retry/one")
	dropIndexChunks(t, s)
	createUnpartitionedIndexChunks(t, s, "test-embed", 8, true, true)
	account := accountID(t, s, "index-retry")
	partitionRun(t, s, account, configfile.RepositoryID(account, "index-retry/one"), true, "a.go")

	writer, err := s.owner.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Exec(ctx, `LOCK TABLE index_chunks IN ROW EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	// Past one wait and one pause, within the tries.
	hold := partitionRetryPause + partitionRetryPause/2
	released := make(chan struct{})
	time.AfterFunc(hold, func() {
		_ = writer.Rollback(context.Background())
		close(released)
	})
	// A failure waits for the writer to let go, so the cleanup's DROP TABLE
	// does not queue behind it.
	defer func() { <-released }()
	start := time.Now()
	got, err := s.PartitionIndexChunks(ctx, "kritika_app")
	if err != nil || !got {
		t.Fatalf("PartitionIndexChunks behind a writer that lets go after %s = %v, %v; want it partitioned", hold, got, err)
	}
	if took := time.Since(start); took < hold {
		t.Fatalf("PartitionIndexChunks returned after %s, before the writer let go at %s", took, hold)
	}
}
