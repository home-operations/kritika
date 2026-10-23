//go:build integration

package store

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestRunnerCannotSelectAccountPolicy(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, twoAccounts)); err != nil {
		t.Fatal(err)
	}
	listRepos(t, s, "alpha", "alpha/one", "alpha/two")
	alpha, beta := accountID(t, s, "alpha"), accountID(t, s, "beta")
	runs := map[string]string{}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for _, id := range runs {
			if _, err := s.owner.Exec(ctx, `DELETE FROM index_packs WHERE runner_run_id = $1`, id); err != nil {
				t.Error(err)
			}
			if _, err := s.owner.Exec(ctx, `DELETE FROM runner_runs WHERE id = $1`, id); err != nil {
				t.Error(err)
			}
		}
	})
	for _, account := range []string{alpha, beta} {
		var id string
		if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `INSERT INTO runner_runs (account_id, kind) VALUES ($1, 'index') RETURNING id`, account).Scan(&id)
		}); err != nil {
			t.Fatal(err)
		}
		runs[account] = id
	}
	token, err := s.MintRunnerToken(ctx, alpha, runs[alpha])
	if err != nil {
		t.Fatal(err)
	}
	runner, err := Open(ctx, Options{AppURL: testEnv(t, "KRITIKA_TEST_RUNNER_URL"), Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runner.Close)
	t.Run("migration restricts existing public policies", func(t *testing.T) {
		checkPublicPolicyUpgrade(t, s, runner, beta, runs[beta])
	})
	for _, withRun := range []bool{false, true} {
		name := "account setting alone"
		if withRun {
			name = "account setting alongside own run"
		}
		t.Run(name, func(t *testing.T) {
			err := runner.WithAccount(ctx, beta, func(tx pgx.Tx) error {
				if withRun {
					if _, err := tx.Exec(ctx, `SELECT set_config('app.runner_job_id', $1, true), set_config('app.runner_token', $2, true)`, runs[alpha], token); err != nil {
						return err
					}
				}
				var n int
				if err := tx.QueryRow(ctx, `SELECT count(*) FROM runner_runs WHERE account_id = $1`, beta).Scan(&n); err != nil {
					return err
				}
				if n != 0 {
					t.Errorf("runner read %d runs from another account", n)
				}
				if withRun {
					if err := tx.QueryRow(ctx, `SELECT count(*) FROM runner_runs WHERE id = $1`, runs[alpha]).Scan(&n); err != nil {
						return err
					}
					if n != 1 {
						t.Errorf("runner read %d own runs, want 1", n)
					}
				}
				tag, err := tx.Exec(ctx, `UPDATE runner_runs SET phase = 'failed' WHERE account_id = $1`, beta)
				if err == nil && tag.RowsAffected() != 0 {
					t.Errorf("runner changed %d runs from another account", tag.RowsAffected())
				}
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
	t.Run("cannot insert a pack for another run", func(t *testing.T) {
		err := runner.WithAccount(ctx, beta, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `INSERT INTO index_packs (runner_run_id, account_id, mode) VALUES ($1, $2, 'full')`, runs[beta], beta)
			return err
		})
		pgErr, ok := errors.AsType[*pgconn.PgError](err)
		if !ok || pgErr.Code != "42501" {
			t.Fatalf("foreign pack insert = %v, want row security refusal", err)
		}
	})
}

func assertPolicyRoles(t *testing.T, s *Store) {
	t.Helper()
	var wrong int
	if err := s.owner.QueryRow(t.Context(), `SELECT count(*) FROM pg_policies WHERE schemaname = current_schema()
 AND policyname IN ('account_isolation', 'runner_job')
 AND roles::text[] <> ARRAY[CASE policyname WHEN 'account_isolation' THEN 'kritika_app' ELSE 'kritika_runner' END]`).Scan(&wrong); err != nil || wrong != 0 {
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
	if _, err := tx.Exec(ctx, `ALTER POLICY runner_job ON runner_runs TO kritika_runner`); err != nil {
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
	if err := s.grant(limited, "kritika_app", "kritika_runner"); err != nil {
		t.Fatalf("unchanged policy binding blocked behind reader: %v", err)
	}
	assertPolicyRoles(t, s)
}

func TestConfiguredRunnerRole(t *testing.T) {
	openStore(t)
	for _, tt := range []struct {
		name, env string
		wantErr   bool
	}{
		{"runner login", "KRITIKA_TEST_RUNNER_URL", false},
		{"application login", "KRITIKA_TEST_APP_URL", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			st, err := Open(t.Context(), Options{AppURL: testEnv(t, tt.env), ExpectedRole: "kritika_runner", Logger: slog.New(slog.DiscardHandler)})
			if st != nil {
				st.Close()
			}
			if errors.Is(err, ErrDatabaseRole) != tt.wantErr || (!tt.wantErr && err != nil) {
				t.Fatalf("Open = %v", err)
			}
		})
	}
}

func TestPolicyBindingsIncludeNewTables(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if _, err := s.owner.Exec(ctx, `CREATE TABLE policy_binding_probe (id uuid);
		CREATE POLICY account_isolation ON policy_binding_probe USING (true);
		CREATE POLICY runner_job ON policy_binding_probe USING (true)`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := s.owner.Exec(cleanup, `DROP TABLE policy_binding_probe`); err != nil {
			t.Error(err)
		}
	})
	if err := s.grant(ctx, "kritika_app", "kritika_runner"); err != nil {
		t.Fatal(err)
	}
	assertPolicyRoles(t, s)
}

func checkPublicPolicyUpgrade(t *testing.T, s, runner *Store, accountID, runID string) {
	t.Helper()
	ctx := t.Context()
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.grant(cleanup, "kritika_app", "kritika_runner"); err != nil {
			t.Error(err)
		}
	})
	rows, err := s.owner.Query(ctx, `SELECT tablename, policyname FROM pg_policies WHERE schemaname = current_schema()
   AND policyname IN ('account_isolation', 'runner_job')`)
	if err != nil {
		t.Fatal(err)
	}
	type policy struct{ Table, Name string }
	policies, err := pgx.CollectRows(rows, pgx.RowToStructByPos[policy])
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range policies {
		if _, err := s.owner.Exec(ctx, `ALTER POLICY `+pgx.Identifier{p.Name}.Sanitize()+` ON `+pgx.Identifier{p.Table}.Sanitize()+` TO PUBLIC`); err != nil {
			t.Fatal(err)
		}
	}
	var exposed int
	if err := runner.WithAccount(ctx, accountID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM runner_runs WHERE id = $1`, runID).Scan(&exposed)
	}); err != nil || exposed != 1 {
		t.Fatalf("public policy exposes %d foreign runs, %v; want 1", exposed, err)
	}
	if err := s.Migrate(ctx, "kritika_app", "kritika_runner"); err != nil {
		t.Fatal(err)
	}
	assertPolicyRoles(t, s)
}
