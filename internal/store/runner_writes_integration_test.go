//go:build integration

package store

import (
	"context"
	"fmt"
	"log/slog"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestRunnerWriteTransactions(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, twoAccounts)); err != nil {
		t.Fatal(err)
	}
	account := accountID(t, s, "alpha")
	runner, err := Open(ctx, Options{AppURL: testEnv(t, "KRITIKA_TEST_RUNNER_URL"), Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runner.Close)
	for _, tt := range []struct {
		name  string
		kind  RunnerKind
		rows  map[string]int
		write func(*Store, string, bool) error
	}{
		{name: "context and phase", kind: RunnerKindReview, rows: map[string]int{"context_packs": 1},
			write: func(st *Store, id string, fail bool) error {
				phase := "done"
				if fail {
					phase = "invalid"
				}
				return st.WriteRunnerContext(ctx, id, RunnerContextPack{
					Head: "head", Base: "base", PatchID: "patch", ChangedPaths: []string{}, Stages: []byte(`[]`), Files: []byte(`{}`),
					Notes: []string{}, DeltaPaths: []string{}, Scope: "full", RuleIDs: []string{},
				}, phase)
			}},
		{name: "agent conversation and phase", kind: RunnerKindReview, rows: map[string]int{"agent_runs": 1, "agent_conversations": 1},
			write: func(st *Store, id string, fail bool) error {
				phase := "done"
				if fail {
					phase = "invalid"
				}
				return st.WriteRunnerAgent(ctx, id, RunnerAgentRecord{
					Stop: "error", ToolCalls: []byte(`{}`), Timeline: []byte(`[]`), Sources: []byte(`[]`),
					SkillsOffered: []string{}, SkillsOpened: []string{}, CommandsOffered: []string{}, CommandsRun: []string{}, CommandErrors: []string{},
					Conversation: []byte(`{"messages":[]}`), Session: id, GrantedModel: "test/model",
				}, phase)
			}},
		{name: "all index batches and phase", kind: RunnerKindIndex, rows: map[string]int{"index_packs": 1, "index_staging": 501},
			write: func(st *Store, id string, fail bool) error {
				chunks := make([]StagedChunk, 501)
				for n := range chunks {
					chunks[n] = StagedChunk{Path: fmt.Sprintf("%d.go", n), StartLine: 1, EndLine: 1, Text: "content"}
				}
				if fail {
					chunks[500].Text = "invalid\x00text"
				}
				return st.WriteRunnerIndex(ctx, id, IndexPack{Mode: IndexModeFull, ChangedPaths: []string{}}, chunks)
			}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			id, token := runnerWriteFixture(t, s, account, tt.kind)
			other, _ := runnerWriteFixture(t, s, account, tt.kind)
			st := runner.ForRunner(token)
			if err := st.AuthorizeRunner(ctx, id); err != nil {
				t.Fatal(err)
			}
			if err := st.AuthorizeRunner(ctx, other); err == nil {
				t.Fatal("credential authorized another run")
			}
			if err := tt.write(st, other, false); err == nil {
				t.Fatal("credential wrote another run")
			}
			assertRunnerWriteState(t, s, other, "created", tt.rows, false)
			if err := tt.write(st, id, true); err == nil {
				t.Fatal("injected failure did not fail")
			}
			assertRunnerWriteState(t, s, id, "created", tt.rows, false)
			if err := tt.write(st, id, false); err != nil {
				t.Fatal(err)
			}
			assertRunnerWriteState(t, s, id, "done", tt.rows, true)
		})
	}
}

func runnerWriteFixture(t *testing.T, s *Store, account string, kind RunnerKind) (string, string) {
	t.Helper()
	ctx := t.Context()
	var id string
	if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		var err error
		id, err = InsertRunnerRun(ctx, tx, account, kind, "", 0)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error { return FailRunnerRun(ctx, tx, id, "test finished") }); err != nil {
			t.Error(err)
		}
	})
	token, err := s.MintRunnerToken(ctx, account, id)
	if err != nil {
		t.Fatal(err)
	}
	return id, token
}

func assertRunnerWriteState(t *testing.T, s *Store, id, wantPhase string, tables map[string]int, written bool) {
	t.Helper()
	ctx := t.Context()
	var phase string
	if err := s.owner.QueryRow(ctx, `SELECT phase FROM runner_runs WHERE id = $1`, id).Scan(&phase); err != nil {
		t.Fatal(err)
	}
	if phase != wantPhase {
		t.Fatalf("phase=%q, want %q", phase, wantPhase)
	}
	for table, want := range tables {
		if !written {
			want = 0
		}
		var rows, foreign int
		if err := s.owner.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE w.account_id <> r.account_id)
   FROM `+pgx.Identifier{table}.Sanitize()+` w JOIN runner_runs r ON r.id = w.runner_run_id WHERE r.id = $1`, id).Scan(&rows, &foreign); err != nil {
			t.Fatal(err)
		}
		if rows != want || foreign != 0 {
			t.Fatalf("%s rows=%d foreign=%d, want %d/0", table, rows, foreign, want)
		}
	}
}
