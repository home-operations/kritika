//go:build integration

package runner

import (
	"log/slog"
	"maps"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/agent"
	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/configfile/configfiletest"
	"github.com/home-operations/kritika/internal/store"
	"github.com/home-operations/kritika/internal/store/storetest"
)

func TestWriteAgentCommandCounts(t *testing.T) {
	ctx := t.Context()
	st := storetest.Open(t)
	t.Setenv("TEST_KEY", "test")
	f := configfiletest.Load(t, `apps:
  command-counts:
    accounts: [command-counts]
    clientId: Iv1.test
    privateKey: { env: TEST_KEY }
    webhookSecret: { env: TEST_KEY }
`)
	if err := st.ApplyConfig(ctx, f); err != nil {
		t.Fatal(err)
	}
	a, _ := f.Account(configfile.ForgeGitHub, "command-counts")
	runner, err := store.Open(ctx, store.Options{
		AppURL: storetest.Env(t, "KRITIKA_TEST_RUNNER_URL"), Logger: slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runner.Close)
	for _, counts := range []map[string]int{{"rg": 3, "jq": 1}, {}} {
		var id string
		if err := st.WithAccount(ctx, a.ID(), func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `INSERT INTO runner_runs (account_id, kind) VALUES ($1, 'review') RETURNING id`, a.ID()).Scan(&id)
		}); err != nil {
			t.Fatal(err)
		}
		token, err := st.MintRunnerToken(ctx, a.ID(), id)
		if err != nil {
			t.Fatal(err)
		}
		rec, err := newAgentRecord(agent.Result{Stop: agent.StopError}, nil, nil, Secrets{})
		if err != nil {
			t.Fatal(err)
		}
		rec.useTools(agentTools{})
		rec.commandCounts = counts
		if err := writeAgentRun(ctx, runner.ForRunner(token), Spec{RunID: id}, rec, "failed"); err != nil {
			t.Fatal(err)
		}
		if err := st.WithAccount(ctx, a.ID(), func(tx pgx.Tx) error {
			var got map[string]int
			var phase string
			if err := tx.QueryRow(ctx, `SELECT a.command_counts, r.phase FROM agent_runs a
				JOIN runner_runs r ON r.id = a.runner_run_id WHERE r.id = $1`, id).Scan(&got, &phase); err != nil {
				return err
			}
			if got == nil || !maps.Equal(got, counts) || phase != "failed" {
				t.Errorf("saved counts=%v phase=%s, want %v failed", got, phase, counts)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.WithAccount(ctx, a.ID(), func(tx pgx.Tx) error {
		data, err := store.ReadCommandUsage(ctx, tx, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
		if err == nil && (!maps.Equal(data.Counts, map[string]int64{"rg": 3, "jq": 1}) || data.RecordedRuns != 2 || data.UncountedRuns != 0) {
			t.Errorf("saved usage = %+v", data)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
