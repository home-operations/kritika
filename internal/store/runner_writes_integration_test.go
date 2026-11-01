//go:build integration

package store

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/runspec"
)

func TestRunnerWriteTransactions(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, twoAccounts)); err != nil {
		t.Fatal(err)
	}
	account := accountID(t, s, "alpha")
	for _, tt := range []struct {
		name  string
		kind  RunnerKind
		rows  map[string]int
		write func(pgx.Tx, string, bool) error
	}{
		{name: "context and phase", kind: RunnerKindReview, rows: map[string]int{"context_packs": 1},
			write: func(tx pgx.Tx, id string, fail bool) error {
				phase := "done"
				if fail {
					phase = "invalid"
				}
				return WriteRunnerContext(ctx, tx, id, runspec.ContextPack{
					Head: "head", Base: "base", PatchID: "patch", ChangedPaths: []string{}, Stages: []byte(`[]`), Files: []byte(`{}`),
					Notes: []string{}, DeltaPaths: []string{}, Scope: "full", RuleIDs: []string{},
				}, phase)
			}},
		{name: "agent conversation and phase", kind: RunnerKindReview, rows: map[string]int{"agent_runs": 1, "agent_conversations": 1},
			write: func(tx pgx.Tx, id string, fail bool) error {
				phase := "done"
				if fail {
					phase = "invalid"
				}
				return WriteRunnerAgent(ctx, tx, id, runspec.AgentRecord{
					Stop: "error", ToolCalls: []byte(`{}`), Timeline: []byte(`[]`), Sources: []byte(`[]`),
					SkillsOffered: []string{}, SkillsOpened: []string{}, CommandsOffered: []string{}, CommandsRun: []string{}, CommandErrors: []string{},
					Conversation: []byte(`{"messages":[]}`), Session: id, GrantedModel: "test/model",
				}, phase)
			}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			id, token := runnerWriteFixture(t, s, account, tt.kind)
			other, _ := runnerWriteFixture(t, s, account, tt.kind)
			write := func(fail bool) error {
				return s.WithRunner(ctx, token, func(tx pgx.Tx, g RunnerGrant) error { return tt.write(tx, g.RunID, fail) })
			}
			if err := write(true); err == nil {
				t.Fatal("injected failure did not fail")
			}
			assertRunnerWriteState(t, s, id, "created", tt.rows, false)
			if err := write(false); err != nil {
				t.Fatal(err)
			}
			assertRunnerWriteState(t, s, id, "done", tt.rows, true)
			// An answer lost on the way back has the runner send it again.
			if err := write(false); err != nil {
				t.Fatalf("second write: %v", err)
			}
			assertRunnerWriteState(t, s, id, "done", tt.rows, true)
			assertRunnerWriteState(t, s, other, "created", tt.rows, false)
		})
	}
}

// TestRunnerIndexUpload stages an upload over several transactions, as the
// gateway does, and seals it with its pack.
func TestRunnerIndexUpload(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, twoAccounts)); err != nil {
		t.Fatal(err)
	}
	account := accountID(t, s, "alpha")
	tables := map[string]int{"index_packs": 1, "index_staging": 3}
	chunks := func(n int) []runspec.IndexChunk {
		out := make([]runspec.IndexChunk, n)
		for i := range out {
			out[i] = runspec.IndexChunk{Path: fmt.Sprintf("%d.go", i), StartLine: 1, EndLine: 1, Text: "content"}
		}
		return out
	}
	pack := runspec.IndexPack{Mode: IndexModeFull, ChangedPaths: []string{}}
	id, token := runnerWriteFixture(t, s, account, RunnerKindIndex)
	begin := func() (string, bool) {
		t.Helper()
		var upload string
		var written bool
		if err := s.WithRunner(ctx, token, func(tx pgx.Tx, g RunnerGrant) error {
			var err error
			upload, written, err = BeginRunnerIndex(ctx, tx, g.RunID)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return upload, written
	}
	stage := func(upload string, n int) error {
		return s.WithRunner(ctx, token, func(tx pgx.Tx, g RunnerGrant) error { return StageRunnerChunks(ctx, tx, g.RunID, upload, chunks(n)) })
	}
	finish := func(upload string, n int) error {
		return s.WithRunner(ctx, token, func(tx pgx.Tx, g RunnerGrant) error { return FinishRunnerIndex(ctx, tx, g.RunID, upload, pack, n) })
	}

	died, written := begin()
	if written {
		t.Fatal("a new upload found its pack written")
	}
	// An upload that died leaves staging, which the next one clears.
	if err := stage(died, 2); err != nil {
		t.Fatal(err)
	}
	stale, written := begin()
	if written {
		t.Fatal("an unsealed upload counted as written")
	}
	assertRunnerWriteState(t, s, id, "created", tables, false)
	// Three uploads overlap, and only the last holds. A stale one neither
	// stages nor seals, however its count comes out.
	if err := stage(stale, 1); err != nil {
		t.Fatal(err)
	}
	current, _ := begin()
	if err := stage(stale, 1); !errors.Is(err, ErrIndexStaging) {
		t.Fatalf("stale stage = %v, want ErrIndexStaging", err)
	}
	if err := finish(stale, 0); !errors.Is(err, ErrIndexStaging) {
		t.Fatalf("stale seal = %v, want ErrIndexStaging", err)
	}
	assertRunnerWriteState(t, s, id, "created", tables, false)
	if err := stage(current, 2); err != nil {
		t.Fatal(err)
	}
	if err := finish(current, 3); !errors.Is(err, ErrIndexStaging) {
		t.Fatalf("seal over too few chunks = %v, want ErrIndexStaging", err)
	}
	if err := stage(current, 1); err != nil {
		t.Fatal(err)
	}
	if err := finish(current, 3); err != nil {
		t.Fatal(err)
	}
	assertRunnerWriteState(t, s, id, "done", tables, true)
	// A stale upload adds nothing once the pack is written.
	if err := stage(stale, 1); !errors.Is(err, ErrIndexStaging) {
		t.Fatalf("stage after the seal = %v, want ErrIndexStaging", err)
	}
	if _, written := begin(); !written {
		t.Fatal("a sealed upload sent again must count as written")
	}
	assertRunnerWriteState(t, s, id, "done", tables, true)
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
