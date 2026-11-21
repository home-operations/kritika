//go:build integration

package store

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/home-operations/kritika/internal/configfile"
)

// attachImplied is the message that Postgres logs at DEBUG1 when a validated
// constraint proves a partition's bound, so that attaching it reads no row.
const attachImplied = `partition constraint for table "index_chunks_legacy" is implied by existing constraints`

// noticedStore is s with an owner pool of its own, whose sessions report
// the server's messages down to DEBUG1, which notices returns.
func noticedStore(t *testing.T, s *Store) (noticed *Store, notices func() []string) {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(testEnv(t, "KRITIKA_TEST_OWNER_URL"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["client_min_messages"] = "debug1"
	var mu sync.Mutex
	var got []string
	cfg.ConnConfig.OnNotice = func(_ *pgconn.PgConn, n *pgconn.Notice) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, n.Message)
	}
	owner, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(owner.Close)
	return &Store{app: s.app, owner: owner, logger: s.logger}, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(got)
	}
}

// legacyMarkValid reports whether table holds legacyMark, and whether it is
// validated.
func legacyMarkValid(t *testing.T, s *Store, table string) (marked, valid bool) {
	t.Helper()
	err := s.owner.QueryRow(t.Context(), `SELECT convalidated FROM pg_constraint
		WHERE conrelid = to_regclass($1) AND conname = $2`, table, legacyMark).Scan(&valid)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return true, valid
}

// TestPartitionIndexChunksValidatesLegacyApart: the conversion reads the
// plain table's chunks to validate legacyMark while a build writes the
// table, rather than under the exclusive lock that it attaches the table
// in, which every search and build waits for. Attaching the table then
// reads none of them: the validated constraint proves the legacy
// partition's bound. A chunk that the build writes meanwhile is legacy.
func TestPartitionIndexChunksValidatesLegacyApart(t *testing.T) {
	// Before the store, so that its connections give way after 10s rather
	// than before the test sees them wait.
	slowLockWaits(t)
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, soloAccount("index-mark"))); err != nil {
		t.Fatal(err)
	}
	listRepos(t, s, "index-mark", "index-mark/one")
	dropIndexChunks(t, s)
	createUnpartitionedIndexChunks(t, s, "test-embed", 8, true, true)
	account := accountID(t, s, "index-mark")
	repo := configfile.RepositoryID(account, "index-mark/one")
	run := partitionRun(t, s, account, repo, true, "a.go")
	noticed, notices := noticedStore(t, s)
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

	// A writer holds the table, so the conversion's first exclusive lock
	// waits for it, and a second writer queues behind that wait.
	first, err := lock()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var ran error
	go func() {
		defer close(done)
		_, ran = noticed.PartitionIndexChunks(ctx, "kritika_app")
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
	// The second writer holds the table once that lock lets go. The
	// conversion validates legacyMark beside it, and then waits for it to
	// attach the table.
	held := <-second
	if held.err != nil {
		t.Fatal(held.err)
	}
	if _, err := held.tx.Exec(ctx, `INSERT INTO index_chunks (account_id, repository_id, index_run_id, path, start_line, end_line, text, embedding)
		VALUES ($1, $2, $3, 'b.go', 1, 2, 'package main', '[0,1,0,0,0,0,0,0]')`, account, repo, run); err != nil {
		t.Fatal(err)
	}
	waitForLockWait(t, s, table, "AccessExclusiveLock", done)
	select {
	case <-done:
		t.Fatalf("the table was attached in the transaction of the exclusive lock that the first writer held up, "+
			"reading its chunks under that lock, rather than after validating %s beside the second writer (err %v)", legacyMark, ran)
	default:
	}
	if marked, valid := legacyMarkValid(t, s, "index_chunks"); !marked || !valid {
		t.Fatalf("while the conversion waits for the writer to attach the table, %s is there %v and valid %v; want both",
			legacyMark, marked, valid)
	}
	if err := held.tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	<-done
	if ran != nil {
		t.Fatal(ran)
	}
	if got := notices(); !slices.Contains(got, attachImplied) {
		t.Fatalf("attaching the legacy partition read its chunks: no %q among the conversion's %d messages", attachImplied, len(got))
	}
	if marked, valid := legacyMarkValid(t, s, "index_chunks_legacy"); !marked || !valid {
		t.Fatalf("the legacy partition holds %s %v, valid %v; want it kept, valid, until the partition is dropped",
			legacyMark, marked, valid)
	}
	if parts := chunkPartitions(t, s, run); len(parts) != 1 || parts["index_chunks_legacy"] != 2 {
		t.Fatalf("the generation's chunks are in %v, want both in the legacy partition, the one written beside the validation too", parts)
	}
}

// TestPartitionIndexChunksAfterAnUnvalidatedMark: a sweep that stopped once
// it added legacyMark, before it validated it, leaves the table for the next
// one, which validates the constraint rather than add it or the column
// again. The conversion's own transaction attaches no table whose
// legacyMark is not validated, which it would read every chunk of under its
// exclusive lock.
func TestPartitionIndexChunksAfterAnUnvalidatedMark(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, soloAccount("index-unmarked"))); err != nil {
		t.Fatal(err)
	}
	listRepos(t, s, "index-unmarked", "index-unmarked/one")
	dropIndexChunks(t, s)
	createUnpartitionedIndexChunks(t, s, "test-embed", 8, true, true)
	account := accountID(t, s, "index-unmarked")
	run := partitionRun(t, s, account, configfile.RepositoryID(account, "index-unmarked/one"), true, "a.go")
	if _, err := s.owner.Exec(ctx, `ALTER TABLE index_chunks
		ADD COLUMN legacy boolean CONSTRAINT index_chunks_legacy_legacy_not_null NOT NULL DEFAULT true,
		ADD CONSTRAINT `+legacyMark+` CHECK (legacy) NOT VALID`); err != nil {
		t.Fatal(err)
	}
	if got, err := s.partitionIndexChunks(ctx, "kritika_app"); !errors.Is(err, errLegacyUnmarked) || got {
		t.Fatalf("the conversion of a table whose %s is not valid = %v, %v; want %v", legacyMark, got, err, errLegacyUnmarked)
	}
	var kind string
	if err := s.owner.QueryRow(ctx, `SELECT relkind::text FROM pg_class WHERE oid = 'index_chunks'::regclass`).Scan(&kind); err != nil {
		t.Fatal(err)
	}
	if kind != "r" {
		t.Fatalf("index_chunks is of kind %q after a conversion that gave up, want the plain table", kind)
	}
	partitionLegacy(t, s)
	var marks int
	if err := s.owner.QueryRow(ctx, `SELECT count(*) FROM pg_constraint
		WHERE conrelid = 'index_chunks_legacy'::regclass AND contype = 'c'`).Scan(&marks); err != nil {
		t.Fatal(err)
	}
	if marked, valid := legacyMarkValid(t, s, "index_chunks_legacy"); !marked || !valid || marks != 1 {
		t.Fatalf("the legacy partition holds %d CHECK constraints, %s %v, valid %v; want it alone, valid", marks, legacyMark, marked, valid)
	}
	if parts := chunkPartitions(t, s, run); len(parts) != 1 || parts["index_chunks_legacy"] != 1 {
		t.Fatalf("the generation's chunk is in %v, want the legacy partition", parts)
	}
}
