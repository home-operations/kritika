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
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/runspec"
)

// createUnpartitionedIndexChunks creates index_chunks as a build before
// partitioning did, a plain table, with the lexemes that migration 0064
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
		if err := tx.QueryRow(ctx, `INSERT INTO index_runs (account_id, repository_id, commit_sha, embed_model, embed_dims, mode, status,
			index_format) VALUES ($1, $2, 'abc', 'test-embed', 8, 'full', 'completed', $3) RETURNING id`, account, repo, runspec.IndexFormat).
			Scan(&run); err != nil {
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
		hits, err := SimilarChunks(ctx, tx, run, [][]float32{{1, 0, 0, 0, 0, 0, 0, 0}}, Exclusions{}, 10)
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
		hits, err := LexicalChunks(ctx, tx, run, []LexicalQuery{q}, Exclusions{}, 10, LexicalBar{Least: 100}, 100)
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

// onlyPartition is the partition that holds every chunk of the generation.
func onlyPartition(t *testing.T, s *Store, run string) string {
	t.Helper()
	parts := chunkPartitions(t, s, run)
	if len(parts) != 1 {
		t.Fatalf("the generation is in %v, want one partition", parts)
	}
	for name := range parts {
		return name
	}
	return ""
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
	// An incremental step's chunks move to the generation's partition from
	// another one. A step in the generation's own would be updated in place,
	// so one is taken whose run is in another.
	leaf := onlyPartition(t, s, run)
	var step string
	for try := 0; step == ""; try++ {
		if try == 100 {
			t.Fatalf("100 steps' runs were all in the generation's partition, %s", leaf)
		}
		candidate := partitionRun(t, s, account, repo, false, "c.go")
		if onlyPartition(t, s, candidate) != leaf {
			step = candidate
		} else if _, err := s.owner.Exec(ctx, `DELETE FROM index_runs WHERE id = $1`, candidate); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE index_chunks SET index_run_id = $2 WHERE index_run_id = $1`, step, run)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if got := similarPaths(t, s, account, run); len(got) != 3 {
		t.Fatalf("search after the step found %v, want three chunks", got)
	}
	if parts := chunkPartitions(t, s, run); len(parts) != 1 || parts[leaf] != 3 {
		t.Fatalf("the generation is in %v, want its three chunks in %s alone", parts, leaf)
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
	defer func() { _ = writer.Rollback(context.Background()) }()
	if _, err := writer.Exec(ctx, `LOCK TABLE index_chunks IN ROW EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	if got, err := s.PartitionIndexChunks(ctx, "kritika_app"); !lockNotAvailable(err) || got {
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
// hash partitions hold every chunk that is not legacy, and they keep the
// partitioned table's indexes and foreign keys, not copies of them.
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
	if bound != "FOR VALUES IN (false)" || partitions != 1 || indexes != 4 || keys != 3 || inherited != 3 {
		t.Fatalf("index_chunks_current %s, one of %d partitions, with %d of the table's indexes and %d foreign keys, %d inherited;"+
			" want FOR VALUES IN (false), one of 1, 4 and 3, 3 inherited", bound, partitions, indexes, keys, inherited)
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
		rows, err := tx.Query(ctx, "EXPLAIN "+similarChunksSQL, "[1,0,0,0,0,0,0,0]", run, []string{}, 4, nil)
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
		rows, err := tx.Query(ctx, "EXPLAIN "+lexicalChunksSQL, run, []int32{0}, []string{"main"}, []bool{false}, []string{"main"}, 0, 100,
			[]string{}, []string{"[1,0,0,0,0,0,0,0]"}, 4, 4+LexicalCommonMax, 100, LexicalCommonMax, 0, postingsIndexFormat, nil)
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

// relationLock is a lock of a mode on a relation.
type relationLock struct {
	relation uint32
	mode     string
}

// waitBehind waits until a transaction waits for next while another still
// waits for first. It fails if first's wait ends before, or if done is
// closed.
func waitBehind(t *testing.T, s *Store, first, next relationLock, done <-chan struct{}) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(2 * time.Millisecond) {
		select {
		case <-done:
			t.Fatalf("the transaction to wait for %s on relation %d finished first", next.mode, next.relation)
		default:
		}
		var held, queued bool
		if err := s.owner.QueryRow(t.Context(), `SELECT
			EXISTS (SELECT 1 FROM pg_locks WHERE locktype = 'relation' AND relation = $1 AND mode = $2 AND NOT granted),
			EXISTS (SELECT 1 FROM pg_locks WHERE locktype = 'relation' AND relation = $3 AND mode = $4 AND NOT granted)`,
			first.relation, first.mode, next.relation, next.mode).Scan(&held, &queued); err != nil {
			t.Fatal(err)
		}
		if !held {
			t.Fatalf("the wait for %s on relation %d ended before a transaction waited for %s on relation %d",
				first.mode, first.relation, next.mode, next.relation)
		}
		if queued {
			return
		}
	}
	t.Fatalf("no transaction waited for %s on relation %d", next.mode, next.relation)
}

// slowLockWaits sets the database's deadlock_timeout to 80s, for every
// connection opened after it until the test ends. A transaction that gives
// way after an eighth of it (yieldLocks) then waits 10s, as long as a lock
// wait's hang guard here, rather than 125ms, too short to be seen waiting
// in. Every transaction keeps the same deadlock_timeout, so the one that
// gives way still does so before another's deadlock check.
func slowLockWaits(t *testing.T) {
	t.Helper()
	ctx := t.Context()
	super, err := pgxpool.New(ctx, testEnv(t, "KRITIKA_TEST_SUPER_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(super.Close)
	var name string
	if err := super.QueryRow(ctx, `SELECT current_database()`).Scan(&name); err != nil {
		t.Fatal(err)
	}
	database := pgx.Identifier{name}.Sanitize()
	if _, err := super.Exec(ctx, `ALTER DATABASE `+database+` SET deadlock_timeout = '80s'`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := super.Exec(context.Background(), `ALTER DATABASE `+database+` RESET deadlock_timeout`); err != nil {
			t.Error(err)
		}
	})
}

// TestPartitionIndexChunksYieldsToPublication: a publication that holds its
// repository and then waits for index_chunks behind the partitioner, which
// then waits for repositories, publishes. The partitioner gives way, and
// partitions the table at its next try, once the publication commits.
func TestPartitionIndexChunksYieldsToPublication(t *testing.T) {
	slowLockWaits(t)
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
	var table, repositories uint32
	if err := s.owner.QueryRow(ctx, `SELECT 'index_chunks'::regclass::oid, 'repositories'::regclass::oid`).Scan(&table, &repositories); err != nil {
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
	type result struct {
		partitioned bool
		err         error
	}
	gaveUp := 0
	gaveWay = func() { gaveUp++ }
	partitioned := make(chan result, 1)
	partitionerDone := make(chan struct{})
	go func() {
		defer close(partitionerDone)
		got, err := s.PartitionIndexChunks(ctx, "kritika_app")
		partitioned <- result{got, err}
	}()
	// A failure lets the writer go, and the partitioner then ends before
	// gaveWay is unset.
	t.Cleanup(func() {
		<-partitionerDone
		gaveWay = nil
	})
	partitioner := relationLock{table, "AccessExclusiveLock"}
	waitForLockWait(t, s, partitioner.relation, partitioner.mode, nil)
	// The publication locks the repository and then queues behind the
	// partitioner to read index_chunks for the generation's postings.
	published := make(chan error, 1)
	publishedDone := make(chan struct{})
	go func() {
		defer close(publishedDone)
		published <- s.WithAccount(ctx, account, func(tx pgx.Tx) error {
			return ActivateGeneration(ctx, tx, repo, next)
		})
	}()
	publication := relationLock{table, "AccessShareLock"}
	waitBehind(t, s, partitioner, publication, publishedDone)
	// Without the writer, the partitioner, first in the queue, takes
	// index_chunks and waits for the repository that the publication holds.
	if err := writer.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	waitBehind(t, s, publication, relationLock{repositories, "ShareRowExclusiveLock"}, publishedDone)
	if err := <-published; err != nil {
		t.Fatalf("the publication failed beside the partitioner: %v", err)
	}
	if got := <-partitioned; got.err != nil || !got.partitioned || gaveUp != 1 {
		t.Fatalf("PartitionIndexChunks beside the publication = %v, %v after giving way %d times; "+
			"want it partitioned at the next try after giving way once", got.partitioned, got.err, gaveUp)
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

// TestDropLegacyIndexChunksYieldsToSearch: the drop waits for no lock of
// index_chunks. A transaction that holds it, as a chunk writer does, makes
// the drop give way at once, rather than hold every search up behind its
// wait, and nothing of the drop's is left for the writer to wait for. Nor
// does it take the tables that its foreign keys reference, so a search that
// holds them leaves it to drop the legacy partition.
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
	hold := func(t *testing.T, stmt string) pgx.Tx {
		t.Helper()
		holder, err := s.owner.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = holder.Rollback(context.Background()) })
		if _, err := holder.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
		return holder
	}

	t.Run("a chunk writer", func(t *testing.T) {
		holder := hold(t, `LOCK TABLE ONLY index_chunks IN ROW EXCLUSIVE MODE`)
		// A wait that lock_timeout ends is "canceling statement due to lock
		// timeout"; this is the lock not taken at all.
		dropped, err := s.DropLegacyIndexChunks(ctx)
		pgErr, ok := errors.AsType[*pgconn.PgError](err)
		if want := `could not obtain lock on relation "index_chunks"`; dropped || !ok || pgErr.Code != "55P03" || pgErr.Message != want {
			t.Fatalf("DropLegacyIndexChunks beside a chunk writer = %v, %v; want it to give way at once: %s", dropped, err, want)
		}
		if !legacyExists(t, s) {
			t.Fatal("the legacy partition was dropped beside a chunk writer")
		}
		if _, err := holder.Exec(ctx, `LOCK TABLE index_chunks_legacy IN ROW EXCLUSIVE MODE NOWAIT`); err != nil {
			t.Fatalf("the drop that gave way still holds the legacy partition: %v", err)
		}
	})
	t.Run("a search", func(t *testing.T) {
		hold(t, `SELECT (SELECT count(*) FROM accounts), (SELECT count(*) FROM repositories), (SELECT count(*) FROM index_runs)`)
		if dropped, err := s.DropLegacyIndexChunks(ctx); err != nil || !dropped {
			t.Fatalf("DropLegacyIndexChunks beside a search of the referenced tables = %v, %v; want the partition dropped", dropped, err)
		}
	})
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
// layout of a fresh one, and a chunk dated before the conversion goes to
// its generation's hash partition.
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
	// A chunk dated before the conversion, as by a database clock that is
	// behind, goes to the hash partitions as any other.
	if _, err := s.owner.Exec(t.Context(), `INSERT INTO index_chunks
			(account_id, repository_id, index_run_id, path, start_line, end_line, text, embedding, created_at)
		VALUES ($1, $2, $3, 'early.go', 1, 2, 'package main', '[1,2,0,0,0,0,0,0]', now() - interval '1 day')`,
		account, repository, runOne); err != nil {
		t.Fatalf("insert of a chunk dated before the conversion: %v", err)
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

// TestDrainLegacyIndexChunksBesideADiscard: a build that lost its generation
// to another discards the generation's chunks holding its job's lock, not
// the repository's. The drain leaves a generation whose job another holds,
// so the discard never deletes a chunk that the drain is moving, which fails
// once the move commits. Once the job is free, the drain moves what is left.
func TestDrainLegacyIndexChunksBesideADiscard(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, soloAccount("index-discard"))); err != nil {
		t.Fatal(err)
	}
	listRepos(t, s, "index-discard", "index-discard/one")
	dropIndexChunks(t, s)
	createUnpartitionedIndexChunks(t, s, "test-embed", 8, true, true)
	account := accountID(t, s, "index-discard")
	repo := configfile.RepositoryID(account, "index-discard/one")
	lost := partitionRun(t, s, account, repo, false, "a.go")
	kept := partitionRun(t, s, account, repo, false, "b.go")
	runners := legacyBuilds(t, s, account, lost, kept)
	partitionLegacy(t, s)

	// The losing build's job holds its lock, as its discard does.
	discard := holdLegacy(t, s, `SELECT 1 FROM river_job WHERE id = (SELECT river_job_id FROM runner_runs WHERE id = $1) FOR UPDATE`,
		runners[lost])
	for range 2 {
		if _, _, err := s.DrainLegacyIndexChunks(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if parts := chunkPartitions(t, s, lost); len(parts) != 1 || parts["index_chunks_legacy"] != 1 {
		t.Fatalf("the drain moved a generation whose job a discard holds: it is in %v", parts)
	}
	if err := DiscardIndexProgress(ctx, discard, lost, runners[lost]); err != nil {
		t.Fatalf("the discard beside the drain: %v", err)
	}
	if err := discard.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if parts := chunkPartitions(t, s, lost); len(parts) != 0 {
		t.Fatalf("the discarded generation is left in %v", parts)
	}
	drainLegacy(t, s)
	if parts := chunkPartitions(t, s, kept); len(parts) != 1 || parts["index_chunks_legacy"] != 0 {
		t.Fatalf("the generation whose job is free is in %v after the drain, want a hash partition", parts)
	}
}

// legacyBuilds makes each of runs a build that a job still works and can
// resume, and returns each run's runner run.
func legacyBuilds(t *testing.T, s *Store, account string, runs ...string) map[string]string {
	t.Helper()
	ctx := t.Context()
	runners := make(map[string]string, len(runs))
	jobIDs := make([]int64, 0, len(runs))
	t.Cleanup(func() {
		ctx := context.Background()
		if _, err := s.owner.Exec(ctx, `DELETE FROM runner_runs WHERE account_id = $1`, account); err != nil {
			t.Error(err)
		}
		if _, err := s.owner.Exec(ctx, `DELETE FROM river_job WHERE id = ANY($1)`, jobIDs); err != nil {
			t.Error(err)
		}
	})
	for _, run := range runs {
		var job int64
		if err := s.owner.QueryRow(ctx, `INSERT INTO river_job (kind, queue, args, max_attempts, state, attempt)
			VALUES ('index', 'index', '{}', 3, 'running', 1) RETURNING id`).Scan(&job); err != nil {
			t.Fatal(err)
		}
		jobIDs = append(jobIDs, job)
		if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `UPDATE index_runs SET status = 'running', resume_key = 'resume' WHERE id = $1`, run); err != nil {
				return err
			}
			runner, err := InsertRunnerRun(ctx, tx, account, RunnerKindIndex, run, job)
			runners[run] = runner
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	return runners
}

// holdLegacy begins a transaction of the owner that takes the row locks of
// query, run with arg, and holds them until the test rolls it back or ends.
func holdLegacy(t *testing.T, s *Store, query, arg string) pgx.Tx {
	t.Helper()
	tx, err := s.owner.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	if _, err := tx.Exec(t.Context(), query, arg); err != nil {
		t.Fatal(err)
	}
	return tx
}

// TestDrainLegacyIndexChunksPassesOverHeldGenerations: a generation whose
// build job, or every chunk left in the legacy partition, another holds is
// passed over for the next, in the same call. So one held generation does
// not end the sweep's drain while others can move. Each generation is held
// in turn, so whichever the drain reads first, it is held once.
func TestDrainLegacyIndexChunksPassesOverHeldGenerations(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, soloAccount("index-held"))); err != nil {
		t.Fatal(err)
	}
	listRepos(t, s, "index-held", "index-held/one")
	dropIndexChunks(t, s)
	createUnpartitionedIndexChunks(t, s, "test-embed", 8, true, true)
	account := accountID(t, s, "index-held")
	repo := configfile.RepositoryID(account, "index-held/one")
	first := partitionRun(t, s, account, repo, false, "a.go", "b.go", "c.go")
	second := partitionRun(t, s, account, repo, false, "d.go", "e.go", "f.go")
	runners := legacyBuilds(t, s, account, first, second)
	partitionLegacy(t, s)
	slice := legacyDrainSlice
	legacyDrainSlice = 1
	t.Cleanup(func() { legacyDrainSlice = slice })

	const job = `SELECT 1 FROM river_job WHERE id = (SELECT river_job_id FROM runner_runs WHERE id = $1) FOR UPDATE`
	const chunks = `SELECT 1 FROM index_chunks_legacy WHERE index_run_id = $1 FOR UPDATE`
	left := map[string]int{first: 3, second: 3}
	for _, tt := range []struct {
		what, query, arg, free string
	}{
		{"the first generation's job", job, runners[first], second},
		{"the second generation's job", job, runners[second], first},
		{"the first generation's chunks", chunks, first, second},
		{"the second generation's chunks", chunks, second, first},
	} {
		hold := holdLegacy(t, s, tt.query, tt.arg)
		moved, removed, err := s.DrainLegacyIndexChunks(ctx)
		if err != nil || moved != 1 || removed != 0 {
			t.Fatalf("drain while another holds %s moved %d and deleted %d, %v; want the other's chunk moved", tt.what, moved, removed, err)
		}
		left[tt.free]--
		for _, run := range []string{first, second} {
			if got := chunkPartitions(t, s, run)["index_chunks_legacy"]; got != left[run] {
				t.Fatalf("after the drain beside %s, %d of generation %s's chunks are in the legacy partition, want %d",
					tt.what, got, run, left[run])
			}
		}
		if err := hold.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if moved, removed := drainLegacy(t, s); moved != 2 || removed != 0 {
		t.Fatalf("the rest of the drain moved %d and deleted %d, want 2 moved", moved, removed)
	}
}

// TestDrainLegacyIndexChunksBesidePublications: incremental steps and a full
// build publish while the drain moves the generation a chunk at a time. No
// chunk is lost or doubled. The first step and the full build each publish
// while a slice of the drain holds the repository (besideDrainSlice).
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
	drainCtx, stopDrain := context.WithCancel(ctx)
	drained := make(chan error, 1)
	draining := false
	t.Cleanup(func() {
		stopDrain()
		if draining {
			if err := <-drained; err != nil && !errors.Is(err, context.Canceled) {
				t.Error(err)
			}
		}
		legacyDrainSlice = slice
	})

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
		advance := func(tx pgx.Tx) error { return AdvanceGeneration(ctx, tx, repo, active, step, commit, []string{path}) }
		if i == 0 {
			besideDrainSlice(t, s, account, advance)
			// The rest of the steps publish beside a drain of its own pace,
			// which leaves chunks for the full build to contend for: of the
			// files-1 left, the steps replace at most files/2 and the drain
			// moves drainBudget.
			const drainBudget = 15
			draining = true
			go func() {
				drained <- func() error {
					for done := 0; done < drainBudget && drainCtx.Err() == nil; {
						var left int
						if err := s.owner.QueryRow(drainCtx, `SELECT count(*) FROM index_chunks_legacy`).Scan(&left); err != nil || left == 0 {
							return err
						}
						moved, removed, err := s.DrainLegacyIndexChunks(drainCtx)
						if err != nil {
							return err
						}
						done += int(moved + removed)
					}
					return nil
				}()
			}()
		} else if err := s.WithAccount(ctx, account, advance); err != nil {
			t.Fatal(err)
		}
		active.Commit = commit
		want[path] = text
	}
	err := <-drained
	draining = false
	if err != nil {
		t.Fatal(err)
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
	if legacy := chunkPartitions(t, s, run)["index_chunks_legacy"]; legacy == 0 {
		t.Fatal("the drain left the full build no chunk of the generation to contend for")
	}

	// A full build supersedes the generation, wherever its chunks are.
	full := partitionRun(t, s, account, repo, false, "new.go")
	besideDrainSlice(t, s, account, func(tx pgx.Tx) error { return ActivateGeneration(ctx, tx, repo, full) })
	drainLegacy(t, s)
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
			EXECUTE partition_search('[1,0,0,0,0,0,0,0]', '%s', '{}', 4, NULL)`, run))
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
// writer lets go, which it does when the partitioner has given way.
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
	// A failure lets the writer go too, so the cleanup's DROP TABLE does not
	// queue behind it.
	defer func() { _ = writer.Rollback(context.Background()) }()
	if _, err := writer.Exec(ctx, `LOCK TABLE index_chunks IN ROW EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	gaveUp := 0
	gaveWay = func() {
		if gaveUp++; gaveUp == 1 {
			if err := writer.Rollback(context.Background()); err != nil {
				t.Error(err)
			}
		}
	}
	t.Cleanup(func() { gaveWay = nil })
	got, err := s.PartitionIndexChunks(ctx, "kritika_app")
	if err != nil || !got || gaveUp != 1 {
		t.Fatalf("PartitionIndexChunks behind a writer that lets go once it gives way = %v, %v after giving way %d times; "+
			"want it partitioned at the next try", got, err, gaveUp)
	}
}

// besideDrainSlice runs publish, in the account, while a slice of the legacy
// drain holds the repository: a transaction that holds the hash partitions
// keeps the slice from moving its chunk until the publication waits for the
// slice too.
func besideDrainSlice(t *testing.T, s *Store, account string, publish func(tx pgx.Tx) error) {
	t.Helper()
	ctx := t.Context()
	hold, err := s.owner.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = hold.Rollback(context.Background()) }()
	if _, err := hold.Exec(ctx, `LOCK TABLE index_chunks_current IN SHARE MODE`); err != nil {
		t.Fatal(err)
	}
	sliced, published := make(chan error, 1), make(chan error, 1)
	go func() {
		moved, removed, err := s.DrainLegacyIndexChunks(ctx)
		if err == nil && moved+removed != 1 {
			err = fmt.Errorf("the slice moved %d and deleted %d chunks, want one", moved, removed)
		}
		sliced <- err
	}()
	lockWaiters(t, s, 1)
	go func() { published <- s.WithAccount(ctx, account, publish) }()
	lockWaiters(t, s, 2)
	if err := hold.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	for _, done := range []chan error{sliced, published} {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

// lockWaiters waits until n transactions wait for a lock. pg_locks shows
// another role's wait, which pg_stat_activity hides.
func lockWaiters(t *testing.T, s *Store, n int) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		var waits int
		if err := s.owner.QueryRow(t.Context(), `SELECT count(DISTINCT pid) FROM pg_locks WHERE NOT granted`).Scan(&waits); err != nil {
			t.Fatal(err)
		}
		if waits >= n {
			return
		}
	}
	t.Fatalf("fewer than %d transactions wait for a lock", n)
}

// TestContentHashIndexBuiltApart: a populated plain index_chunks that a
// leader of a build before content hashes recreated gains the column in a
// transaction of its own, whose exclusive lock is gone by the time the
// content hash's index is built. So searches read the table while the index
// reads every chunk. That holds whether the leader brings the table up to
// its schema or the sweep partitions it, which then attaches the index
// built rather than build one under its own exclusive lock.
func TestContentHashIndexBuiltApart(t *testing.T) {
	for _, tt := range []struct {
		name, index string
		run         func(ctx context.Context, s *Store) error
	}{
		{"by the leader", "index_chunks_content_hash_idx", func(ctx context.Context, s *Store) error {
			_, err := s.EnsureIndexSchema(ctx, "kritika_app", "test-embed", 8, "")
			return err
		}},
		{"by the partitioning sweep", "index_chunks_legacy_content_hash_idx", func(ctx context.Context, s *Store) error {
			_, err := s.PartitionIndexChunks(ctx, "kritika_app")
			return err
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := openStore(t)
			ctx := t.Context()
			if err := s.ApplyConfig(ctx, parse(t, soloAccount("index-apart"))); err != nil {
				t.Fatal(err)
			}
			listRepos(t, s, "index-apart", "index-apart/one")
			dropIndexChunks(t, s)
			createUnpartitionedIndexChunks(t, s, "test-embed", 8, true, false)
			account := accountID(t, s, "index-apart")
			repo := configfile.RepositoryID(account, "index-apart/one")
			if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
				var run string
				if err := tx.QueryRow(ctx, `INSERT INTO index_runs (account_id, repository_id, commit_sha, embed_model, embed_dims, mode, status)
					VALUES ($1, $2, 'abc', 'test-embed', 8, 'full', 'completed') RETURNING id`, account, repo).Scan(&run); err != nil {
					return err
				}
				_, err := tx.Exec(ctx, `INSERT INTO index_chunks (account_id, repository_id, index_run_id, path, start_line, end_line, text, embedding)
					VALUES ($1, $2, $3, 'a.go', 1, 2, 'package main', '[1,0,0,0,0,0,0,0]')`, account, repo, run)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			var table uint32
			if err := s.owner.QueryRow(ctx, `SELECT 'index_chunks'::regclass::oid`).Scan(&table); err != nil {
				t.Fatal(err)
			}
			lock := func() (pgx.Tx, error) {
				tx, err := s.owner.Begin(ctx)
				if err != nil {
					return nil, err
				}
				t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
				_, err = tx.Exec(ctx, `LOCK TABLE index_chunks IN ROW EXCLUSIVE MODE`)
				return tx, err
			}

			// A writer holds the table, so the column waits for it, and a
			// second writer queues behind the column.
			first, err := lock()
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{})
			var ran error
			go func() {
				defer close(done)
				ran = tt.run(ctx, s)
			}()
			waitForLockWait(t, s, table, "AccessExclusiveLock", done)
			type locked struct {
				tx  pgx.Tx
				err error
			}
			second := make(chan locked, 1)
			go func() {
				tx, err := lock()
				second <- locked{tx, err}
			}()
			waitForLockWait(t, s, table, "RowExclusiveLock", done)
			if err := first.Rollback(ctx); err != nil {
				t.Fatal(err)
			}
			// The second writer holds the table once the column commits,
			// and the index build then waits for it.
			held := <-second
			if held.err != nil {
				t.Fatal(held.err)
			}
			waitForLockWait(t, s, table, "ShareLock", done)
			select {
			case <-done:
				t.Fatalf("the index was built in the column's transaction, under its exclusive lock (err %v)", ran)
			default:
			}
			var exclusive bool
			if err := s.owner.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_locks WHERE locktype = 'relation' AND relation = $1
				AND mode = 'AccessExclusiveLock' AND granted)`, table).Scan(&exclusive); err != nil {
				t.Fatal(err)
			}
			if exclusive {
				t.Fatal("the index waits to be built while the table's exclusive lock is held")
			}
			if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
				if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout = '1s'`); err != nil {
					return err
				}
				var n int
				return tx.QueryRow(ctx, `SELECT count(*) FROM index_chunks`).Scan(&n)
			}); err != nil {
				t.Fatalf("a search while the index waits to be built: %v", err)
			}
			if err := held.tx.Rollback(ctx); err != nil {
				t.Fatal(err)
			}
			<-done
			if ran != nil {
				t.Fatal(ran)
			}
			var built bool
			if err := s.owner.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, tt.index).Scan(&built); err != nil || !built {
				t.Fatalf("%s built = %v, %v; want it built", tt.index, built, err)
			}
		})
	}
}

// TestPartitionIndexChunksWhateverTheClock: the partitions route a chunk by
// whether it was written before the conversion, not by when, so a database
// clock that is behind, as after a failover, routes none wrong. A chunk of
// the plain table dated after the conversion, as by a server whose clock was
// ahead, becomes the legacy partition's and is moved out of it unchanged,
// and a chunk written after the conversion but dated before it goes to the
// hash partitions.
func TestPartitionIndexChunksWhateverTheClock(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, soloAccount("index-clock"))); err != nil {
		t.Fatal(err)
	}
	listRepos(t, s, "index-clock", "index-clock/one")
	dropIndexChunks(t, s)
	createUnpartitionedIndexChunks(t, s, "test-embed", 8, true, true)
	account := accountID(t, s, "index-clock")
	repo := configfile.RepositoryID(account, "index-clock/one")
	run := partitionRun(t, s, account, repo, true, "a.go")
	var dated time.Time
	if err := s.owner.QueryRow(ctx, `UPDATE index_chunks SET created_at = now() + interval '1 day' WHERE index_run_id = $1
		RETURNING created_at`, run).Scan(&dated); err != nil {
		t.Fatal(err)
	}
	partitionLegacy(t, s)
	if parts := chunkPartitions(t, s, run); parts["index_chunks_legacy"] != 1 {
		t.Fatalf("the plain table's chunk is in %v, want the legacy partition", parts)
	}
	early := partitionRun(t, s, account, repo, false, "early.go")
	if _, err := s.owner.Exec(ctx, `UPDATE index_chunks SET created_at = now() - interval '1 day' WHERE index_run_id = $1`, early); err != nil {
		t.Fatal(err)
	}
	if parts := chunkPartitions(t, s, early); len(parts) != 1 || parts["index_chunks_legacy"] != 0 {
		t.Fatalf("a chunk written after the conversion, dated before it, is in %v, want a hash partition", parts)
	}
	if moved, removed := drainLegacy(t, s); moved != 1 || removed != 0 {
		t.Fatalf("drain moved %d and deleted %d chunks, want the active generation's 1 moved", moved, removed)
	}
	var kept time.Time
	if err := s.owner.QueryRow(ctx, `SELECT created_at FROM index_chunks WHERE index_run_id = $1`, run).Scan(&kept); err != nil {
		t.Fatal(err)
	}
	if !kept.Equal(dated) {
		t.Fatalf("the moved chunk is dated %s, want its own %s", kept, dated)
	}
	if dropped, err := s.DropLegacyIndexChunks(ctx); err != nil || !dropped {
		t.Fatalf("DropLegacyIndexChunks of the empty legacy partition = %v, %v", dropped, err)
	}
	checkFreshLayout(t, s)
}
