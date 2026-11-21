//go:build integration

package main

import (
	"context"
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
	"github.com/home-operations/kritika/internal/store/storetest"
	"github.com/home-operations/kritika/internal/worker"
)

// TestLeadSweepsOnceTheConfigurationIsApplied: a new leader's retention sweep
// waits for the configuration to be applied, so that it never converts an
// index table that applying an embedder replaces. A configuration held up in
// the store holds the sweep back, and the sweep runs once it is applied.
func TestLeadSweepsOnceTheConfigurationIsApplied(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	owner, err := pgx.Connect(ctx, storetest.Env(t, "KRITIKA_TEST_OWNER_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Close(context.Background()) })
	// Another session watches: a transaction sees one snapshot of pg_stat_activity.
	watch, err := pgx.Connect(ctx, storetest.Env(t, "KRITIKA_TEST_OWNER_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = watch.Close(context.Background()) })
	// An expired login only the sweep deletes.
	if _, err := st.App().Exec(ctx, `INSERT INTO login_states (state_hash, provider, nonce, pkce_verifier, expires_at, browser_hash)
		VALUES ('\x6c656164', 'test', 'n', 'v', now() - interval '1 hour', '\x00')`); err != nil {
		t.Fatal(err)
	}
	swept := func() bool {
		t.Helper()
		var left bool
		if err := st.App().QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM login_states WHERE state_hash = '\x6c656164')`).Scan(&left); err != nil {
			t.Fatal(err)
		}
		return !left
	}
	hold, err := owner.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Applying a configuration ends by recording its hash on this row.
	if _, err := hold.Exec(ctx, `INSERT INTO config_state (id, applied_hash) VALUES (1, '')
		ON CONFLICT (id) DO UPDATE SET applied_hash = config_state.applied_hash`); err != nil {
		t.Fatal(err)
	}
	released := false
	release := func() {
		if !released {
			released = true
			if err := hold.Rollback(context.Background()); err != nil {
				t.Error(err)
			}
		}
	}
	t.Cleanup(release)

	cfg := &config.Config{DatabaseAppRole: "kritika_app", LeaderRetryInterval: time.Second}
	current := configfile.NewCurrent(configfiletest.Load(t, "{}"))
	queue, err := river.NewClient(riverpgxv5.New(st.App()), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	apps := &worker.Forges{}
	reg := prometheus.NewRegistry()
	logger := slog.New(slog.DiscardHandler)
	leadCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		done <- lead(leadCtx, st, cfg, current, queue, ingest.NewService(st, queue), apps, &worker.ForgeCache{Build: apps.Build}, nil, nil,
			metrics.New(reg), server.NewConfigErrorGauge(reg), logger)
	}()
	t.Cleanup(func() {
		stop()
		release()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})

	waitLead(t, "the configuration to wait on the store", func() bool {
		var waiting bool
		if err := watch.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity
			WHERE wait_event_type = 'Lock' AND query LIKE '%INSERT INTO config_state%')`).
			Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		return waiting
	})
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if swept() {
			t.Fatal("retention swept before the configuration was applied")
		}
	}
	release()
	waitLead(t, "retention to sweep once the configuration is applied", swept)
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

// TestLeadSweepsOnceTheIndexSchemaIsEnsured: with an embedder configured, a
// new leader's retention sweep also waits for the index schema to be
// ensured, which can replace the index table that the sweep would convert:
// the configuration's commit alone does not start it.
func TestLeadSweepsOnceTheIndexSchemaIsEnsured(t *testing.T) {
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
	owner, err := pgx.Connect(ctx, storetest.Env(t, "KRITIKA_TEST_OWNER_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Close(context.Background()) })
	watch, err := pgx.Connect(ctx, storetest.Env(t, "KRITIKA_TEST_OWNER_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = watch.Close(context.Background()) })
	if _, err := st.App().Exec(ctx, `INSERT INTO login_states (state_hash, provider, nonce, pkce_verifier, expires_at, browser_hash)
		VALUES ('\x736368656d61', 'test', 'n', 'v', now() - interval '1 hour', '\x00')`); err != nil {
		t.Fatal(err)
	}
	swept := func() bool {
		t.Helper()
		var left bool
		if err := st.App().QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM login_states WHERE state_hash = '\x736368656d61')`).Scan(&left); err != nil {
			t.Fatal(err)
		}
		return !left
	}
	// Ensuring the index schema starts by taking this lock.
	hold, err := owner.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hold.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('kritika-index-schema'))`); err != nil {
		t.Fatal(err)
	}
	released := false
	release := func() {
		if !released {
			released = true
			if err := hold.Rollback(context.Background()); err != nil {
				t.Error(err)
			}
		}
	}
	t.Cleanup(release)

	cfg := &config.Config{DatabaseAppRole: "kritika_app", LeaderRetryInterval: time.Second}
	queue, err := river.NewClient(riverpgxv5.New(st.App()), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	apps := &worker.Forges{}
	reg := prometheus.NewRegistry()
	leadCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		done <- lead(leadCtx, st, cfg, configfile.NewCurrent(file), queue, ingest.NewService(st, queue), apps, &worker.ForgeCache{Build: apps.Build},
			nil, nil, metrics.New(reg), server.NewConfigErrorGauge(reg), slog.New(slog.DiscardHandler))
	}()
	t.Cleanup(func() {
		stop()
		release()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})

	waitLead(t, "the configuration to be applied and the index schema to wait on its lock", func() bool {
		var applied, waiting bool
		if err := watch.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM config_state WHERE id = 1 AND applied_hash = $1),
			EXISTS (SELECT 1 FROM pg_stat_activity WHERE wait_event_type = 'Lock' AND wait_event = 'advisory'
				AND query LIKE '%kritika-index-schema%')`, file.Hash()).Scan(&applied, &waiting); err != nil {
			t.Fatal(err)
		}
		return applied && waiting
	})
	if swept() {
		t.Fatal("retention swept once the configuration was applied, before the index schema was ensured")
	}
	release()
	waitLead(t, "retention to sweep once the index schema is ensured", swept)
}
