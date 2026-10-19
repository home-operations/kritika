//go:build integration

package store

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestRunnerNotificationsConveyNoAuthority(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, twoAccounts)); err != nil {
		t.Fatal(err)
	}
	runner, err := Open(ctx, Options{AppURL: testEnv(t, "KRITIKA_TEST_RUNNER_URL"), Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runner.Close)
	listener, err := runner.app.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Release()
	if _, err := listener.Exec(ctx, `LISTEN kritika_events`); err != nil {
		t.Fatal(err)
	}
	inserts := map[string]string{
		"review_part_checkpoints": `INSERT INTO review_part_checkpoints
			(runner_run_id, account_id, input_hash, part, source_run_id, payload) VALUES ($1, $2, 'inputs', 1, $1, '{}')`,
		"context_packs": `INSERT INTO context_packs (runner_run_id, account_id, head_sha, base_sha, patch_id, diff)
			VALUES ($1, $2, 'head', 'base', 'patch', 'private diff')`,
		"index_packs": `INSERT INTO index_packs (runner_run_id, account_id, mode) VALUES ($1, $2, 'full')`,
		"index_staging": `INSERT INTO index_staging (runner_run_id, account_id, path, start_line, end_line, text)
			VALUES ($1, $2, 'secret.go', 1, 1, 'private source')`,
		"agent_runs": `INSERT INTO agent_runs (runner_run_id, account_id, stop_reason, model) VALUES ($1, $2, 'error', 'test')`,
		"agent_conversations": `INSERT INTO agent_conversations (runner_run_id, account_id, session, conversation, tokens)
			VALUES ($1, $2, 'session', 'private conversation', 1)`,
	}
	type run struct{ account, id, token string }
	runs := make([]run, 0, 2)
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for _, r := range runs {
			for table := range inserts {
				if _, err := s.owner.Exec(cleanup, `DELETE FROM `+pgx.Identifier{table}.Sanitize()+` WHERE runner_run_id = $1`, r.id); err != nil {
					t.Error(err)
				}
			}
			if _, err := s.owner.Exec(cleanup, `DELETE FROM runner_runs WHERE id = $1`, r.id); err != nil {
				t.Error(err)
			}
		}
	})
	for _, name := range []string{"alpha", "beta"} {
		r := run{account: accountID(t, s, name)}
		if err := s.WithAccount(ctx, r.account, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `INSERT INTO runner_runs (account_id, kind) VALUES ($1, 'index') RETURNING id`, r.account).Scan(&r.id)
		}); err != nil {
			t.Fatal(err)
		}
		runs = append(runs, r)
		r.token, err = s.MintRunnerToken(ctx, r.account, r.id)
		if err != nil {
			t.Fatal(err)
		}
		runs[len(runs)-1] = r
		for _, stmt := range inserts {
			if _, err := s.owner.Exec(ctx, stmt, r.id, r.account); err != nil {
				t.Fatal(err)
			}
		}
	}
	awaitRunnerNotification(t, listener.Conn(), runs[1].id, runs[1].account, runs[1].token)
	for _, tt := range []struct {
		name, id, token string
		want            int
	}{
		{"own credential", runs[0].id, runs[0].token, 1},
		{"public ID without credential", runs[1].id, "", 0},
		{"public ID with another run credential", runs[1].id, runs[0].token, 0},
		{"public ID with random credential", runs[1].id, RandomToken(), 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			bound := runner.ForRunner(tt.token)
			if err := bound.WithRunnerJob(ctx, tt.id, func(tx pgx.Tx) error {
				// A compromised runner can set either custom variable itself.
				if _, err := tx.Exec(ctx, `SELECT set_config('app.account_id', $1, true)`, runs[1].account); err != nil {
					return err
				}
				for _, table := range []string{"runner_runs", "context_packs", "index_packs", "index_staging", "agent_runs", "agent_conversations"} {
					column := "runner_run_id"
					if table == "runner_runs" {
						column = "id"
					}
					var n int
					if err := tx.QueryRow(ctx, `SELECT count(*) FROM `+pgx.Identifier{table}.Sanitize()+` WHERE `+column+` = $1`, tt.id).Scan(&n); err != nil {
						return err
					}
					if n != tt.want {
						t.Errorf("read %d rows from %s, want %d", n, table, tt.want)
					}
				}
				tag, err := tx.Exec(ctx, `UPDATE runner_runs SET phase = 'fetching' WHERE id = $1`, tt.id)
				if err == nil && tag.RowsAffected() != int64(tt.want) {
					t.Errorf("updated %d runs, want %d", tag.RowsAffected(), tt.want)
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if tt.want == 0 {
				for table, stmt := range inserts {
					t.Run("insert "+table, func(t *testing.T) {
						err := bound.WithRunnerJob(ctx, tt.id, func(tx pgx.Tx) error {
							_, err := tx.Exec(ctx, stmt, tt.id, runs[1].account)
							return err
						})
						pgErr, ok := errors.AsType[*pgconn.PgError](err)
						if !ok || pgErr.Code != "42501" {
							t.Fatalf("insert = %v, want row security refusal", err)
						}
					})
				}
			}
		})
	}
}

func awaitRunnerNotification(t *testing.T, listener *pgx.Conn, runID, accountID, token string) {
	t.Helper()
	ctx := t.Context()
	gotNotification := false
	wait, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for !gotNotification {
		n, err := listener.WaitForNotification(wait)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(n.Payload, token) {
			t.Fatal("notification leaked a database credential")
		}
		event, err := parseEvent(n.Payload)
		if err != nil {
			t.Fatal(err)
		}
		gotNotification = event.Kind == EventRunnerRun && event.ID == runID && event.AccountID == accountID
	}
}
