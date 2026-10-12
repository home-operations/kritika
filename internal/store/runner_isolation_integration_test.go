//go:build integration

package store

import (
	"errors"
	"log/slog"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestRunnerCannotSelectAccountPolicy(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, twoAccounts)); err != nil {
		t.Fatal(err)
	}
	alpha, beta := accountID(t, s, "alpha"), accountID(t, s, "beta")
	runs := map[string]string{}
	for _, account := range []string{alpha, beta} {
		var id string
		if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `INSERT INTO runner_runs (account_id, kind) VALUES ($1, 'index') RETURNING id`, account).Scan(&id)
		}); err != nil {
			t.Fatal(err)
		}
		runs[account] = id
	}
	runner, err := Open(ctx, Options{AppURL: testEnv(t, "KRITIKA_TEST_RUNNER_URL"), Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runner.Close)
	t.Run("migration restricts existing public policies", func(t *testing.T) {
		for _, table := range []string{"runner_runs", "context_packs", "index_packs", "index_staging", "agent_runs", "agent_conversations"} {
			for _, policy := range []string{"account_isolation", "runner_job"} {
				if _, err := s.owner.Exec(ctx, `ALTER POLICY `+pgx.Identifier{policy}.Sanitize()+
					` ON `+pgx.Identifier{table}.Sanitize()+` TO PUBLIC`); err != nil {
					t.Fatal(err)
				}
			}
		}
		var exposed int
		if err := runner.WithAccount(ctx, beta, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) FROM runner_runs WHERE id = $1`, runs[beta]).Scan(&exposed)
		}); err != nil || exposed != 1 {
			t.Fatalf("public policy exposes %d foreign runs, %v; want 1", exposed, err)
		}
		if err := s.Migrate(ctx, "kritika_app", "kritika_runner"); err != nil {
			t.Fatal(err)
		}
		var public int
		if err := s.owner.QueryRow(ctx, `SELECT count(*) FROM pg_policies
			WHERE schemaname = 'public' AND tablename IN
			('runner_runs', 'context_packs', 'index_packs', 'index_staging', 'agent_runs', 'agent_conversations')
			AND 'public' = ANY(roles)`).Scan(&public); err != nil || public != 0 {
			t.Fatalf("%d shared policies remain public, %v", public, err)
		}
	})
	for _, withRun := range []bool{false, true} {
		name := "account setting alone"
		if withRun {
			name = "account setting alongside own run"
		}
		t.Run(name, func(t *testing.T) {
			err := runner.WithAccount(ctx, beta, func(tx pgx.Tx) error {
				if withRun {
					if _, err := tx.Exec(ctx, `SELECT set_config('app.runner_job_id', $1, true)`, runs[alpha]); err != nil {
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
