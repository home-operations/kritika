//go:build integration

package main

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/home-operations/kritika/internal/config"
	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/configfile/configfiletest"
	"github.com/home-operations/kritika/internal/ingest"
	"github.com/home-operations/kritika/internal/metrics"
	"github.com/home-operations/kritika/internal/server"
	"github.com/home-operations/kritika/internal/store"
	"github.com/home-operations/kritika/internal/store/storetest"
	"github.com/home-operations/kritika/internal/worker"
)

// holdTx begins a transaction on its own owner connection, runs sql in it,
// and returns a release that rolls it back, once, and is also a cleanup.
func holdTx(t *testing.T, sql string) (release func()) {
	t.Helper()
	ctx := t.Context()
	conn, err := pgx.Connect(ctx, storetest.Env(t, "KRITIKA_TEST_OWNER_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, sql); err != nil {
		t.Fatal(err)
	}
	released := false
	release = func() {
		if !released {
			released = true
			if err := tx.Rollback(context.Background()); err != nil {
				t.Error(err)
			}
		}
	}
	t.Cleanup(release)
	return release
}

// expiredLogin plants an expired login that only retention deletes, and
// returns whether it is gone.
func expiredLogin(t *testing.T, st *store.Store, hash string) (swept func() bool) {
	t.Helper()
	ctx := t.Context()
	if _, err := st.App().Exec(ctx, `INSERT INTO login_states (state_hash, provider, nonce, pkce_verifier, expires_at, browser_hash)
		VALUES (decode($1, 'hex'), 'test', 'n', 'v', now() - interval '1 hour', '\x00')`, hash); err != nil {
		t.Fatal(err)
	}
	return func() bool {
		t.Helper()
		var left bool
		if err := st.App().QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM login_states WHERE state_hash = decode($1, 'hex'))`, hash).
			Scan(&left); err != nil {
			t.Fatal(err)
		}
		return !left
	}
}

// indexSchemaWaiters counts the sessions that wait for the index schema's
// lock in mode, a pg_locks mode, or in either mode when mode is "".
// Partitioning the index table, which retention's index work starts with,
// asks for it as ExclusiveLock; the sweep of stopped repositories' indexes
// asks for it as ShareLock.
func indexSchemaWaiters(t *testing.T, watch *pgx.Conn, mode string) int {
	t.Helper()
	var n int
	if err := watch.QueryRow(t.Context(), `SELECT count(DISTINCT a.pid) FROM pg_stat_activity a JOIN pg_locks l ON l.pid = a.pid
		WHERE l.locktype = 'advisory' AND NOT l.granted AND a.query LIKE '%kritika-index-schema%'
			AND ($1 = '' OR l.mode = $1)`, mode).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// startLead runs lead until the test ends and returns a channel closed once
// lead returned, with its error then in *err.
func startLead(
	t *testing.T, st *store.Store, file *configfile.File,
	reach func(context.Context, *configfile.Connection) (map[string]store.Listing, error),
) (exited <-chan struct{}, err *error) {
	t.Helper()
	cfg := &config.Config{DatabaseAppRole: "kritika_app", LeaderRetryInterval: time.Second}
	queue, qerr := river.NewClient(riverpgxv5.New(st.App()), &river.Config{})
	if qerr != nil {
		t.Fatal(qerr)
	}
	apps := &worker.Forges{}
	if reach == nil {
		reach = apps.Reach
	}
	reg := prometheus.NewRegistry()
	leadCtx, stop := context.WithCancel(t.Context())
	done := make(chan struct{})
	var leadErr error
	go func() {
		defer close(done)
		leadErr = lead(leadCtx, st, cfg, configfile.NewCurrent(file), queue, ingest.NewService(st, queue), reach,
			&worker.ForgeCache{Build: apps.Build}, nil, nil, metrics.New(reg), server.NewConfigErrorGauge(reg), slog.New(slog.DiscardHandler))
	}()
	t.Cleanup(func() {
		stop()
		<-done
		// A stop cuts short what the leader was doing, such as ensuring the
		// index schema once a test released its lock.
		if leadErr != nil && !errors.Is(leadErr, context.Canceled) {
			t.Error(leadErr)
		}
	})
	return done, &leadErr
}

// TestLeadPartitionsOnceTheConfigurationIsApplied: a new leader's retention
// holds its index work back until the configuration is applied, so that it
// never converts an index table that applying an embedder replaces. Its
// other sweeps, such as expired logins, do not wait for that.
func TestLeadPartitionsOnceTheConfigurationIsApplied(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	// Another session watches: a transaction sees one snapshot of pg_stat_activity.
	watch, err := pgx.Connect(ctx, storetest.Env(t, "KRITIKA_TEST_OWNER_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = watch.Close(context.Background()) })
	swept := expiredLogin(t, st, "6c656164")
	// Applying a configuration ends by recording its hash on this row.
	releaseConfig := holdTx(t, `INSERT INTO config_state (id, applied_hash) VALUES (1, '')
		ON CONFLICT (id) DO UPDATE SET applied_hash = config_state.applied_hash`)
	// And the index work, once it starts, waits here, where it shows.
	holdTx(t, `SELECT pg_advisory_xact_lock(hashtext('kritika-index-schema'))`)

	startLead(t, st, configfiletest.Load(t, "{}"), nil)
	waitLead(t, "the configuration to wait on the store and retention to sweep the expired login", func() bool {
		var waiting bool
		if err := watch.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity
			WHERE wait_event_type = 'Lock' AND query LIKE '%INSERT INTO config_state%')`).
			Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		return waiting && swept()
	})
	if n := indexSchemaWaiters(t, watch, ""); n != 0 {
		t.Fatalf("%d sessions wait for the index schema's lock before the configuration is applied, want none", n)
	}
	releaseConfig()
	waitLead(t, "retention's index work to start once the configuration is applied", func() bool {
		return indexSchemaWaiters(t, watch, "ExclusiveLock") > 0
	})
}

func waitLead(t *testing.T, what string, done func() bool) {
	t.Helper()
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if done() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestLeadSweepsWhileTheIndexSchemaWaits: with an embedder configured, a
// new leader's retention also holds its index work back until the index
// schema is ensured, which can replace the index table that the work would
// convert: the configuration's commit alone does not start it. Its other
// sweeps run while ensuring the schema waits for a lock.
func TestLeadSweepsWhileTheIndexSchemaWaits(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	t.Setenv("TEST_SECRET", "test-provider-key")
	file := configfiletest.Load(t, `
providers:
  test:
    type: openai
    baseUrl: http://unused.invalid/v1
    apiKey: { env: TEST_SECRET }
embedding: { model: test/fake-embed, dims: 8 }
`)
	watch, err := pgx.Connect(ctx, storetest.Env(t, "KRITIKA_TEST_OWNER_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = watch.Close(context.Background()) })
	swept := expiredLogin(t, st, "736368656d61")
	// Ensuring the index schema starts by taking this lock.
	release := holdTx(t, `SELECT pg_advisory_xact_lock(hashtext('kritika-index-schema'))`)

	startLead(t, st, file, nil)
	waitLead(t, "the configuration to be applied, the index schema to wait on its lock, and retention to sweep the expired login", func() bool {
		var applied bool
		if err := watch.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM config_state WHERE id = 1 AND applied_hash = $1)`, file.Hash()).
			Scan(&applied); err != nil {
			t.Fatal(err)
		}
		return applied && indexSchemaWaiters(t, watch, "") > 0 && swept()
	})
	// Ensuring the schema is the one waiting: retention's index work is not.
	if n := indexSchemaWaiters(t, watch, ""); n != 1 {
		t.Fatalf("%d sessions wait for the index schema's lock, want only the one ensuring it", n)
	}
	release()
}

// TestLeadSweepsWhileRepositoriesAreListed: a new leader's retention does
// not wait for the repositories of its connections to be listed, which a
// forge that does not answer holds up for a minute per connection: neither
// its sweeps nor its index work, which starts by partitioning the index
// table. The listing here waits for the sweep of an expired login and for
// the partitioning to wait on the index schema's lock, held here, so it ends
// only once its own timeout does when either waits for it.
func TestLeadSweepsWhileRepositoriesAreListed(t *testing.T) {
	st := storetest.Open(t)
	t.Setenv("TEST_GT_TOKEN", "gitea-api")
	t.Setenv("TEST_GT_HOOK", "gitea-hook")
	file := configfiletest.Load(t, `
gitea:
  acme-tea:
    host: gitea.example.com
    accounts: [acme]
    token: { env: TEST_GT_TOKEN }
    webhookSecret: { env: TEST_GT_HOOK }
`)
	if _, err := st.App().Exec(t.Context(), `INSERT INTO login_states (state_hash, provider, nonce, pkce_verifier, expires_at, browser_hash)
		VALUES ('\x6c69737473', 'test', 'n', 'v', now() - interval '1 hour', '\x00')`); err != nil {
		t.Fatal(err)
	}
	// Another session watches, from the listing's goroutine, so its errors
	// are reported with t.Error rather than indexSchemaWaiters' t.Fatal.
	watch, err := pgx.Connect(t.Context(), storetest.Env(t, "KRITIKA_TEST_OWNER_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = watch.Close(context.Background()) })
	release := holdTx(t, `SELECT pg_advisory_xact_lock(hashtext('kritika-index-schema'))`)
	swept := func(ctx context.Context) bool {
		var left, partitioning bool
		if err := watch.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM login_states WHERE state_hash = '\x6c69737473'),
			EXISTS (SELECT 1 FROM pg_stat_activity a JOIN pg_locks l ON l.pid = a.pid
				WHERE l.locktype = 'advisory' AND NOT l.granted AND l.mode = 'ExclusiveLock' AND a.query LIKE '%kritika-index-schema%')`).
			Scan(&left, &partitioning); err != nil {
			if ctx.Err() == nil {
				t.Error(err)
			}
			return false
		}
		return !left && partitioning
	}
	// The first listing's outcome: the backstop poll lists again later.
	listed := make(chan bool, 1)
	report := func(ok bool) {
		select {
		case listed <- ok:
		default:
		}
	}
	reach := func(ctx context.Context, _ *configfile.Connection) (map[string]store.Listing, error) {
		tick := time.NewTicker(50 * time.Millisecond)
		defer tick.Stop()
		for !swept(ctx) {
			select {
			case <-ctx.Done():
				report(false)
				return nil, ctx.Err()
			case <-tick.C:
			}
		}
		report(true)
		return nil, errors.New("not listed")
	}

	exited, leadErr := startLead(t, st, file, reach)
	// A hang guard past the listing's own minute, whose end is the failure
	// this test looks for.
	select {
	case ok := <-listed:
		if !ok {
			t.Fatal("retention's sweep or index work waited for the repositories to be listed")
		}
		release()
	case <-exited:
		t.Fatalf("lead returned before it listed the repositories: %v", *leadErr)
	case <-time.After(3 * time.Minute):
		t.Fatal("timed out waiting for the first listing")
	}
}
