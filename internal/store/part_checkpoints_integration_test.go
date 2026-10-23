//go:build integration

package store

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestPartCheckpointRetries(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, twoAccounts)); err != nil {
		t.Fatal(err)
	}
	listRepos(t, s, "alpha", "alpha/one", "alpha/two")
	account := accountID(t, s, "alpha")
	id := insertReview(t, ctx, s, account)
	var pullID string
	if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT pull_request_id FROM reviews WHERE id = $1`, id).Scan(&pullID)
	}); err != nil {
		t.Fatal(err)
	}
	runner, err := Open(ctx, Options{AppURL: testEnv(t, "KRITIKA_TEST_RUNNER_URL"), Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runner.Close)
	start := func(jobID int64) Started {
		t.Helper()
		var started Started
		if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
			var err error
			started, err = StartReview(ctx, tx, NewReview{AccountID: account, PullRequestID: pullID, HeadSHA: "abc", JobID: jobID})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			ctx := context.Background()
			if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
				return FailRunnerRun(ctx, tx, started.RunID, "test finished")
			}); err != nil {
				t.Error(err)
			}
		})
		return started
	}
	first := start(8877001)
	token, err := s.MintRunnerToken(ctx, account, first.RunID)
	if err != nil {
		t.Fatal(err)
	}
	payload := json.RawMessage(`{"Result":{"Stop":"submitted","Submitted":{"findings":[]}}}`)
	for _, p := range []json.RawMessage{payload, json.RawMessage(`{"replacement":true}`)} {
		if err := runner.ForRunner(token).WithRunnerJob(ctx, first.RunID, func(tx pgx.Tx) error {
			return SavePartCheckpoint(ctx, tx, first.RunID, "inputs", 1, p)
		}); err != nil {
			t.Fatal(err)
		}
	}
	retry := start(8877001)
	freshToken, err := s.MintRunnerToken(ctx, account, retry.RunID)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name, token, run, hash string
		want                   int
	}{
		{"retry reads copy", freshToken, retry.RunID, "inputs", 1},
		{"changed inputs", freshToken, retry.RunID, "changed", 0},
		{"old token cannot read retry", token, retry.RunID, "inputs", 0},
		{"retry cannot read source", freshToken, first.RunID, "inputs", 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := runner.ForRunner(tt.token).WithRunnerJob(ctx, tt.run, func(tx pgx.Tx) error {
				rows, err := ReadPartCheckpoints(ctx, tx, tt.run, tt.hash)
				if err != nil {
					return err
				}
				if len(rows) != tt.want {
					t.Fatalf("checkpoints = %d, want %d", len(rows), tt.want)
				}
				if len(rows) > 0 {
					var got map[string]any
					if err := json.Unmarshal(rows[0].Payload, &got); err != nil {
						return err
					}
					if rows[0].SourceRun != first.RunID || got["Result"] == nil {
						t.Fatalf("checkpoint replaced or lost provenance: %+v", rows[0])
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
	manual := start(8877002)
	if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		rows, err := ReadPartCheckpoints(ctx, tx, manual.RunID, "inputs")
		if len(rows) != 0 {
			t.Fatal("manual review reused an earlier job")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
