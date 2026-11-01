//go:build integration

package store

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func assertPolicyRoles(t *testing.T, s *Store) {
	t.Helper()
	var wrong int
	if err := s.owner.QueryRow(t.Context(), `SELECT count(*) FROM pg_policies WHERE schemaname = current_schema()
 AND policyname = 'account_isolation' AND roles::text[] <> ARRAY['kritika_app']`).Scan(&wrong); err != nil || wrong != 0 {
		t.Fatalf("%d incorrectly bound policies: %v", wrong, err)
	}
}

func TestPolicyBindingsAvoidUnchangedLocks(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	tx, err := s.owner.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `ALTER POLICY account_isolation ON runner_runs TO kritika_app`); err != nil {
		t.Fatal(err)
	}
	var exclusive bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE pid = pg_backend_pid()
 AND relation = 'runner_runs'::regclass AND mode = 'AccessExclusiveLock')`).Scan(&exclusive); err != nil || !exclusive {
		t.Fatalf("unchanged ALTER POLICY exclusive lock = %v, %v", exclusive, err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	reader, err := s.app.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Rollback(ctx) }()
	if _, err := reader.Exec(ctx, `SELECT 1 FROM runner_runs LIMIT 1`); err != nil {
		t.Fatal(err)
	}
	limited, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := s.grant(limited, "kritika_app"); err != nil {
		t.Fatalf("unchanged policy binding blocked behind reader: %v", err)
	}
	assertPolicyRoles(t, s)
}

func TestPolicyBindingsIncludeNewTables(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if _, err := s.owner.Exec(ctx, `CREATE TABLE policy_binding_probe (id uuid);
		CREATE POLICY account_isolation ON policy_binding_probe USING (true)`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := s.owner.Exec(cleanup, `DROP TABLE policy_binding_probe`); err != nil {
			t.Error(err)
		}
	})
	if err := s.grant(ctx, "kritika_app"); err != nil {
		t.Fatal(err)
	}
	assertPolicyRoles(t, s)
}

// TestPublicPoliciesAreBound brings back a binding to PUBLIC, which a policy
// has until the first grant, and makes sure that Migrate restricts it.
func TestPublicPoliciesAreBound(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	rows, err := s.owner.Query(ctx, `SELECT tablename FROM pg_policies WHERE schemaname = current_schema() AND policyname = 'account_isolation'`)
	if err != nil {
		t.Fatal(err)
	}
	tables, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range tables {
		if _, err := s.owner.Exec(ctx, `ALTER POLICY account_isolation ON `+pgx.Identifier{table}.Sanitize()+` TO PUBLIC`); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Migrate(ctx, "kritika_app"); err != nil {
		t.Fatal(err)
	}
	assertPolicyRoles(t, s)
}

// TestRunnerRoleLosesItsAccess applies the migration that took the database
// away from runners to what an install had before it: a role bound to the
// runner_job policies and granted what they gated.
func TestRunnerRoleLosesItsAccess(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	super, err := pgxpool.New(ctx, testEnv(t, "KRITIKA_TEST_SUPER_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(super.Close)
	const role = "custom_runner_role"
	if _, err := super.Exec(ctx, `CREATE ROLE `+role+` LOGIN`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := super.Exec(cleanup, `DROP ROLE `+role); err != nil {
			t.Error(err)
		}
	})
	for _, stmt := range []string{
		`GRANT USAGE ON SCHEMA public TO ` + role,
		`GRANT USAGE ON ALL SEQUENCES IN SCHEMA public TO ` + role,
		`GRANT SELECT, UPDATE (phase, error, heartbeat_at, parts_remaining) ON runner_runs TO ` + role,
		`GRANT SELECT, INSERT ON context_packs, index_packs, index_staging, agent_runs, agent_conversations, review_part_checkpoints TO ` + role,
		`CREATE POLICY runner_job ON agent_runs TO ` + role + ` USING (true)`,
		`CREATE POLICY runner_job ON runner_runs TO ` + role + ` USING (true)`,
	} {
		if _, err := s.owner.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	migration, err := migrationFS.ReadFile("migrations/0054_runner_without_database.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.owner.Exec(ctx, string(migration)); err != nil {
		t.Fatal(err)
	}
	var policies int
	if err := s.owner.QueryRow(ctx, `SELECT count(*) FROM pg_policies WHERE schemaname = current_schema() AND policyname = 'runner_job'`).
		Scan(&policies); err != nil || policies != 0 {
		t.Fatalf("%d runner_job policies left: %v", policies, err)
	}
	var table, column bool
	if err := s.owner.QueryRow(ctx, `SELECT has_table_privilege($1, 'agent_runs', 'INSERT'),
		has_column_privilege($1, 'runner_runs', 'phase', 'UPDATE')`, role).Scan(&table, &column); err != nil {
		t.Fatal(err)
	}
	if table || column {
		t.Fatalf("the runner role kept table = %v, column = %v", table, column)
	}
	// A role with nothing left can be dropped by whoever manages it, as the
	// cleanup above does.
	for _, name := range []string{"runner_runs", "agent_runs"} {
		var granted int
		if err := s.owner.QueryRow(ctx, `SELECT count(*) FROM information_schema.role_table_grants WHERE grantee = $1 AND table_name = $2`, role, name).
			Scan(&granted); err != nil || granted != 0 {
			t.Fatalf("%d grants left on %s: %v", granted, name, err)
		}
	}
}
