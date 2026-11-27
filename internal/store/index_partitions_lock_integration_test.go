//go:build integration

package store

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/tuning"
)

// requestLog records the SQL of every request that a pool's connections
// send.
type requestLog struct {
	mu   sync.Mutex
	sent []string
}

func (l *requestLog) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sent = append(l.sent, data.SQL)
	return ctx
}

func (l *requestLog) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// since returns the requests sent from the last that contains marker on.
func (l *requestLog) since(marker string) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i, sql := range slices.Backward(l.sent) {
		if strings.Contains(sql, marker) {
			return append([]string(nil), l.sent[i:]...)
		}
	}
	return nil
}

// tracedOwner is s with an owner pool of its own whose requests log records.
func tracedOwner(t *testing.T, s *Store, log *requestLog) *Store {
	t.Helper()
	config, err := pgxpool.ParseConfig(testEnv(t, "KRITIKA_TEST_OWNER_URL"))
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.Tracer = log
	owner, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(owner.Close)
	return &Store{app: s.app, owner: owner, logger: s.logger}
}

// TestPartitionIndexChunksLockedRoundTrips: once the conversion holds the
// plain index_chunks' exclusive lock, which every search and build waits
// for, it sends a few requests, not one for each statement of the
// partitioned tree, so a connection's latency is paid a few times under the
// lock rather than about eighty.
func TestPartitionIndexChunksLockedRoundTrips(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, soloAccount("index-roundtrips"))); err != nil {
		t.Fatal(err)
	}
	listRepos(t, s, "index-roundtrips", "index-roundtrips/one")
	dropIndexChunks(t, s)
	createUnpartitionedIndexChunks(t, s, "test-embed", 8, true, true)
	account := accountID(t, s, "index-roundtrips")
	run := partitionRun(t, s, account, configfile.RepositoryID(account, "index-roundtrips/one"), true, "a.go")

	var log requestLog
	traced := tracedOwner(t, s, &log)
	if got, err := traced.PartitionIndexChunks(ctx, "kritika_app"); err != nil || !got {
		t.Fatalf("PartitionIndexChunks = %v, %v; want the table partitioned", got, err)
	}
	// The lock request, the constraints' names, the conversion and the
	// commit.
	locked := log.since("LOCK TABLE index_chunks IN ACCESS EXCLUSIVE MODE")
	if len(locked) == 0 || len(locked) > 4 {
		t.Fatalf("the conversion sent %d requests from its exclusive lock on, want 4 at most:\n%s",
			len(locked), strings.Join(locked, "\n---\n"))
	}
	if got := similarPaths(t, s, account, run); strings.Join(got, ",") != "a.go" {
		t.Fatalf("search after the conversion found %v, want a.go", got)
	}
	drainLegacy(t, s)
	if dropped, err := s.DropLegacyIndexChunks(ctx); err != nil || !dropped {
		t.Fatalf("DropLegacyIndexChunks after the drain = %v, %v", dropped, err)
	}
	checkFreshLayout(t, s)
}

// TestDropLegacyIndexChunksBesideSustainedSearches: searches that overlap
// without a pause, one always holding index_chunks, keep the drop from
// taking its lock at once at every try. Its last try queues for the lock,
// and so drops the empty legacy partition once the searches that hold it
// end.
func TestDropLegacyIndexChunksBesideSustainedSearches(t *testing.T) {
	slowLockWaits(t)
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, soloAccount("index-drop-sustained"))); err != nil {
		t.Fatal(err)
	}
	listRepos(t, s, "index-drop-sustained", "index-drop-sustained/one")
	dropIndexChunks(t, s)
	createUnpartitionedIndexChunks(t, s, "test-embed", 8, true, true)
	account := accountID(t, s, "index-drop-sustained")
	run := partitionRun(t, s, account, configfile.RepositoryID(account, "index-drop-sustained/one"), true, "a.go")
	partitionLegacy(t, s)
	drainLegacy(t, s)
	var table uint32
	if err := s.owner.QueryRow(ctx, `SELECT 'index_chunks'::regclass::oid`).Scan(&table); err != nil {
		t.Fatal(err)
	}

	// search runs on the drop's goroutine too, so it reports an error rather
	// than end the test.
	search := func() (pgx.Tx, error) {
		tx, err := s.owner.Begin(ctx)
		if err != nil {
			return nil, err
		}
		t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
		_, err = tx.Exec(ctx, `SELECT count(*) FROM index_chunks`)
		return tx, err
	}
	// Each time the drop gives way, the search that held index_chunks ends
	// and another, which began before it ended, holds it in its place.
	holding, err := search()
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	gaveUp := 0
	gaveWay = func(error) {
		mu.Lock()
		defer mu.Unlock()
		gaveUp++
		next, err := search()
		if err != nil {
			t.Error(err)
			return
		}
		if err := holding.Commit(ctx); err != nil {
			t.Error(err)
		}
		holding = next
	}
	type result struct {
		dropped bool
		err     error
	}
	dropped := make(chan result, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		got, err := s.DropLegacyIndexChunks(ctx)
		dropped <- result{got, err}
	}()
	t.Cleanup(func() {
		<-done
		gaveWay = nil
	})
	waitForLockWait(t, s, table, "AccessExclusiveLock", done)
	mu.Lock()
	defer mu.Unlock()
	select {
	case got := <-dropped:
		t.Fatalf("DropLegacyIndexChunks beside sustained searches = %v, %v after giving way %d times; want its last try to wait for the lock",
			got.dropped, got.err, gaveUp)
	default:
	}
	if gaveUp != tuning.Get().Store.PartitionTries-1 {
		t.Fatalf("the drop waited for the lock after giving way %d times, want %d", gaveUp, tuning.Get().Store.PartitionTries-1)
	}
	// The search that holds index_chunks now ends, and the drop takes it.
	if err := holding.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if got := <-dropped; got.err != nil || !got.dropped {
		t.Fatalf("DropLegacyIndexChunks once the search that held index_chunks ended = %v, %v; want the partition dropped", got.dropped, got.err)
	}
	if legacyExists(t, s) {
		t.Fatal("the empty legacy partition was kept")
	}
	checkFreshLayout(t, s)
	if got := similarPaths(t, s, account, run); strings.Join(got, ",") != "a.go" {
		t.Fatalf("search after the drop found %v, want a.go", got)
	}
}
