package store

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Migrate applies River's schema and then kritika's own migrations, in order,
// each in its own transaction. It records each in schema_migrations. It runs
// as the owner and is idempotent. Grants to the application role are
// re-applied after every run because a new table needs them and GRANT is
// idempotent.
func (s *Store) Migrate(ctx context.Context, appRole string) error {
	if s.owner == nil {
		return errors.New("store: migrate needs the owner DSN")
	}
	migrator, err := rivermigrate.New(riverpgxv5.New(s.owner), &rivermigrate.Config{Logger: s.logger})
	if err != nil {
		return fmt.Errorf("store: river migrator: %w", err)
	}
	if _, err := migrator.Migrate(ctx, rivermigrate.DirectionUp, nil); err != nil {
		return fmt.Errorf("store: river migrations: %w", err)
	}

	if _, err := s.owner.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return fmt.Errorf("store: schema_migrations: %w", err)
	}
	names, err := fs.Glob(migrationFS, "migrations/*.sql")
	if err != nil {
		return fmt.Errorf("store: list migrations: %w", err)
	}
	slices.Sort(names)
	for _, name := range names {
		version := strings.TrimSuffix(strings.TrimPrefix(name, "migrations/"), ".sql")
		if err := s.applyMigration(ctx, name, version); err != nil {
			return err
		}
	}
	return s.grant(ctx, appRole)
}

func (s *Store) applyMigration(ctx context.Context, name, version string) error {
	tx, err := s.owner.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: begin migration %s: %w", version, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Serialize concurrent migrators. The leader lock already does, but a
	// manual run from a shell must not race it.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('kritika-migrate'))`); err != nil {
		return fmt.Errorf("store: migration lock: %w", err)
	}
	var applied bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`, version).Scan(&applied); err != nil {
		return fmt.Errorf("store: check migration %s: %w", version, err)
	}
	if applied {
		return nil
	}
	sql, err := migrationFS.ReadFile(name)
	if err != nil {
		return fmt.Errorf("store: read migration %s: %w", version, err)
	}
	if _, err := tx.Exec(ctx, string(sql)); err != nil {
		return fmt.Errorf("store: apply migration %s: %w", version, err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, version); err != nil {
		return fmt.Errorf("store: record migration %s: %w", version, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: commit migration %s: %w", version, err)
	}
	s.logger.Info("migration applied", "version", version)
	return nil
}

// SchemaReady reports whether every embedded migration, River's and
// kritika's, is applied and the application role holds what grant gives it
// on River's tables and kritika's. So a replica that is not the leader knows
// when it can start working jobs. A release can bring a River migration
// without one of kritika's. A fresh database has no schema_migrations table
// at all, which is "not yet", not an error.
func (s *Store) SchemaReady(ctx context.Context) (bool, error) {
	names, err := fs.Glob(migrationFS, "migrations/*.sql")
	if err != nil {
		return false, fmt.Errorf("store: list migrations: %w", err)
	}
	var exists bool
	if err := s.app.QueryRow(ctx, `SELECT to_regclass('schema_migrations') IS NOT NULL`).Scan(&exists); err != nil {
		return false, fmt.Errorf("store: check schema: %w", err)
	}
	if !exists {
		return false, nil
	}
	var applied int
	if err := s.app.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&applied); err != nil {
		return false, fmt.Errorf("store: count migrations: %w", err)
	}
	if applied < len(names) {
		return false, nil
	}
	// The result says what River's migrator will otherwise log, at every poll.
	migrator, err := rivermigrate.New(riverpgxv5.New(s.app), &rivermigrate.Config{Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		return false, fmt.Errorf("store: river migrator: %w", err)
	}
	river, err := migrator.Validate(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("store: check river migrations: %w", err)
	}
	if !river.OK {
		return false, nil
	}
	// River and kritika each commit a migration before grant gives the
	// application role the tables it made.
	var tables, privileges []string
	for _, g := range appTableGrants {
		for table := range strings.SplitSeq(g.tables, ", ") {
			for p := range strings.SplitSeq(g.privileges, ", ") {
				tables, privileges = append(tables, table), append(privileges, p)
			}
		}
	}
	var granted bool
	if err := s.app.QueryRow(ctx, `SELECT NOT EXISTS (SELECT 1 FROM pg_tables
		WHERE schemaname = current_schema() AND tablename LIKE 'river\_%'
		AND NOT has_table_privilege(format('%I.%I', schemaname, tablename), 'SELECT'))
		AND NOT EXISTS (SELECT 1 FROM unnest($1::text[], $2::text[]) AS g (t, p)
			WHERE to_regclass(g.t) IS NULL OR NOT has_table_privilege(g.t, g.p))`, tables, privileges).Scan(&granted); err != nil {
		return false, fmt.Errorf("store: check grants: %w", err)
	}
	return granted, nil
}

// WaitForSchema polls SchemaReady until it is true or ctx ends.
func (s *Store) WaitForSchema(ctx context.Context, every time.Duration) error {
	for {
		ready, err := s.SchemaReady(ctx)
		if err != nil {
			s.logger.Warn("schema check failed, retrying", "error", err)
		} else if ready {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(every):
		}
	}
}

// readWrite is every privilege of a table that the application role writes
// as it reads.
const readWrite = "SELECT, INSERT, UPDATE, DELETE"

// appTableGrants are the application role's privileges on kritika's tables,
// which grant gives and SchemaReady checks.
var appTableGrants = []struct{ privileges, tables string }{
	{"SELECT", "accounts, config_state, schema_migrations"},
	{readWrite, "repositories, model_leases, pull_requests"},
	{readWrite, "reviews, runner_runs, context_packs, findings, sticky_comments, usage"},
	{readWrite, "index_runs, index_packs, index_staging, index_postings, followups, dismissals, poll_state"},
	{"SELECT", "index_schema"},
	{"SELECT, INSERT", "agent_runs, agent_conversations, similar_exclusions"},
	{"SELECT, INSERT, DELETE", "review_part_checkpoints"},
	{readWrite, "gateway_tokens, job_heartbeats"},
	// The web dashboard's own tables: all instance-level (no RLS, access
	// control lives in web code) except model_calls, which is account
	// content gated by its own account_isolation policy.
	{readWrite, "users, identities, sessions, login_states, api_tokens"},
	{"SELECT, INSERT, UPDATE", "chatgpt_sessions"},
	{"SELECT, INSERT", "audit_events"},
	{readWrite, "model_calls, egress_refusals, model_invocations"},
}

// grant gives the application role what requests and jobs need. Its name
// comes from configuration, so it is quoted as an identifier rather than
// interpolated raw.
func (s *Store) grant(ctx context.Context, appRole string) error {
	app := pgx.Identifier{appRole}.Sanitize()
	stmts := []string{
		`GRANT USAGE ON SCHEMA public TO ` + app,
		// Only the leader writes connections. A request stamps a delivery.
		`GRANT SELECT, UPDATE (last_webhook_at, last_unsigned_webhook_at) ON connections TO ` + app,
		`GRANT USAGE ON ALL SEQUENCES IN SCHEMA public TO ` + app,
	}
	for _, g := range appTableGrants {
		stmts = append(stmts, `GRANT `+g.privileges+` ON `+g.tables+` TO `+app)
	}
	// River's tables are outside row-level security. The application role
	// inserts and works jobs. Their set changes across River versions, so
	// grant on whatever River's migrations created rather than a fixed list.
	rows, err := s.owner.Query(ctx, `SELECT tablename FROM pg_tables WHERE schemaname = current_schema() AND tablename LIKE 'river\_%'`)
	if err != nil {
		return fmt.Errorf("store: list river tables: %w", err)
	}
	river, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return fmt.Errorf("store: list river tables: %w", err)
	}
	for _, table := range river {
		stmts = append(stmts, `GRANT SELECT, INSERT, UPDATE, DELETE ON `+pgx.Identifier{table}.Sanitize()+` TO `+app)
	}
	tx, err := s.owner.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: begin grants: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := bindPolicies(ctx, tx, appRole); err != nil {
		return err
	}
	for _, stmt := range stmts {
		if _, err := tx.Exec(ctx, stmt); err != nil {
			grant, _, _ := strings.Cut(stmt, " TO ")
			return fmt.Errorf("store: %s: %w", grant, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: commit grants: %w", err)
	}
	return nil
}

// bindPolicies scopes policies to roles only when their bindings differ.
// ALTER POLICY takes an exclusive table lock even for an unchanged binding.
func bindPolicies(ctx context.Context, tx pgx.Tx, appRole string) error {
	rows, err := tx.Query(ctx, `SELECT tablename, policyname, $1::text AS role
		FROM pg_policies WHERE schemaname = current_schema()
		AND policyname = 'account_isolation'
		AND roles::text[] <> ARRAY[$1::text]
		ORDER BY tablename, policyname`, appRole)
	if err != nil {
		return fmt.Errorf("store: inspect policy roles: %w", err)
	}
	type binding struct{ Table, Policy, Role string }
	bindings, err := pgx.CollectRows(rows, pgx.RowToStructByPos[binding])
	if err != nil {
		return fmt.Errorf("store: read policy roles: %w", err)
	}
	for _, b := range bindings {
		stmt := `ALTER POLICY ` + pgx.Identifier{b.Policy}.Sanitize() + ` ON ` + pgx.Identifier{b.Table}.Sanitize() +
			` TO ` + pgx.Identifier{b.Role}.Sanitize()
		if _, err := tx.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("store: bind policy: %w", err)
		}
	}
	return nil
}
