//go:build integration

package runner

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/agent"
	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/configfile/configfiletest"
	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/runspec"
	"github.com/home-operations/kritika/internal/store/storetest"
)

func TestPartCheckpointPersistence(t *testing.T) {
	ctx := t.Context()
	st := storetest.Open(t)
	t.Setenv("TEST_KEY", "test")
	f := configfiletest.Load(t, `apps:
  part-checkpoints:
    accounts: [part-checkpoints]
    clientId: Iv1.test
    privateKey: { env: TEST_KEY }
    webhookSecret: { env: TEST_KEY }
`)
	if err := st.ApplyConfig(ctx, f); err != nil {
		t.Fatal(err)
	}
	a, _ := f.Account(configfile.ForgeGitHub, "part-checkpoints")
	s := reviewSpec()
	if err := st.WithAccount(ctx, a.ID(), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO runner_runs (account_id, kind) VALUES ($1, 'review') RETURNING id`, a.ID()).Scan(&s.RunID)
	}); err != nil {
		t.Fatal(err)
	}
	token, err := st.MintRunnerToken(ctx, a.ID(), s.RunID)
	if err != nil {
		t.Fatal(err)
	}
	report := gatewayReporter(t, st, token)
	parts := []reviewPart{{paths: []string{"a.go"}, prompt: loopPrompt(s, false)}}
	hash, err := partInputHash(s, parts, agentTools{})
	if err != nil {
		t.Fatal(err)
	}
	result := agent.Result{Stop: agent.StopSubmitted, Submitted: submission("a.go", "repository-secret"), Model: "model",
		Steps: 4, Usage: model.Usage{Input: 100, Output: 20}, CostUSD: 0.5, ForcedBy: agent.ForcedByTime}
	if err := savePart(ctx, report, runspec.Secrets{GitToken: "repository-secret"}, hash, 1, result, agentTools{}); err != nil {
		t.Fatal(err)
	}
	saved, err := loadParts(ctx, report, hash, parts, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	got := saved[0]
	if got.SourceRun != s.RunID || got.Result.Stop != agent.StopSubmitted || strings.Contains(string(got.Result.Submitted), "repository-secret") ||
		got.Result.ForcedBy != agent.ForcedByTime {
		t.Fatalf("restored result = %+v", got)
	}
	if got.Result.CostUSD != 0 || got.Result.Steps != 0 || got.Result.Usage.Prompt() != 0 {
		t.Fatalf("restored result would count work twice: %+v", got.Result)
	}
}
