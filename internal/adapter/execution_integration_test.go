//go:build integration

package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/configfile/configfiletest"
	"github.com/home-operations/kritika/internal/metrics"
	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/store"
	"github.com/home-operations/kritika/internal/store/storetest"
)

func TestExecutionAccounting(t *testing.T) {
	st := storetest.Open(t)
	t.Setenv("PRIMARY_KEY", "primary-secret")
	t.Setenv("BACKUP_KEY", "backup-secret")
	file := configfiletest.Load(t, `providers:
  primary:
    type: openai
    baseUrl: https://primary.invalid
    apiKey: { env: PRIMARY_KEY }
  backup:
    type: openai
    baseUrl: https://backup.invalid
    apiKey: { env: BACKUP_KEY }
review: { model: primary/review }
apps:
  execution:
    accounts: [execution]
    clientId: Iv1.test
    privateKey: { env: PRIMARY_KEY }
    webhookSecret: { env: PRIMARY_KEY }
`)
	if err := st.ApplyConfig(t.Context(), file); err != nil {
		t.Fatal(err)
	}
	storetest.Repositories(t, st, file, "execution/repo")
	account, _ := file.Account(configfile.ForgeGitHub, "execution")
	repoID := configfile.RepositoryID(account.ID(), "execution/repo")
	for _, tt := range []struct {
		name                         string
		structured, fallback, cancel bool
		fail, invalid                bool
	}{
		{name: "runner settles reservation"},
		{name: "runner refunds failure", fail: true},
		{name: "structured fallback survives cancellation", structured: true, fallback: true, cancel: true},
		{name: "invalid structured answer is still billed", structured: true, invalid: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var runID string
			if err := st.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
				var err error
				runID, err = store.InsertRunnerRun(ctx, tx, account.ID(), store.RunnerKindReview, "", 0)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				ctx := context.Background()
				if err := st.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
					return store.FailRunnerRun(ctx, tx, runID, "test finished")
				}); err != nil {
					t.Error(err)
				}
			})
			var logs bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&logs, nil))
			steppers := &Steppers{Build: func(p configfile.Provider) (model.Stepper, error) {
				return model.StepperFunc(func(_ context.Context, req model.StepRequest) (model.StepResponse, error) {
					if tt.fail || (tt.fallback && strings.Contains(p.BaseURL, "primary")) {
						return model.StepResponse{}, errors.New("provider failed: primary-secret")
					}
					if tt.cancel {
						cancel()
					}
					resp := model.StepResponse{Model: "served-model", Usage: model.Usage{Input: 10, CacheRead: 2, Output: 3},
						CostUSD: 0.125, Stop: model.StopToolUse}
					if !tt.invalid {
						input, _ := json.Marshal(map[string]string{"text": p.APIKeyValue().Value()})
						resp.ToolCalls = []model.ToolCall{{ID: "answer", Name: "answer", Input: input}}
					}
					return resp, nil
				}), nil
			}}
			var fallback configfile.ModelRef
			if tt.fallback {
				fallback = "backup/review"
			}
			call, err := steppers.Execution(file, account, "primary/review", fallback, tt.structured, logger)
			if err != nil {
				t.Fatal(err)
			}
			reg := prometheus.NewRegistry()
			call.Recorder = Recorder{Store: st, Metrics: metrics.New(reg)}
			call.Record = store.ModelCall{AccountID: account.ID(), RunnerRunID: runID, Kind: store.ModelCallAgentStep}
			call.Usage = store.Usage{AccountID: account.ID(), RepositoryID: repoID, RunnerRunID: runID, Role: store.RoleReview}
			system := "primary-secret"
			if tt.fallback {
				system = "backup-secret"
			}
			if !tt.structured {
				var token string
				token, err = st.MintGatewayToken(ctx, store.GatewayGrant{AccountID: account.ID(), RepositoryID: repoID,
					RunID: runID, Model: "primary/review", Budget: 100}, time.Now().Add(time.Minute))
				if err != nil {
					t.Fatal(err)
				}
				if ok, err := st.ReserveGatewayTokens(ctx, token, 50); err != nil || !ok {
					t.Fatalf("reserve: %v, %v", ok, err)
				}
				call.Reservation = &Reservation{Token: token, Tokens: 50}
				_, err = call.Step(ctx, model.StepRequest{System: system})
			} else {
				call.Record.Kind = store.ModelCallConfidence
				_, err = call.Complete(ctx, model.CompletionRequest{System: system, SchemaName: "answer"})
			}
			if (err != nil) != (tt.fail || tt.invalid) {
				t.Fatalf("call: %v", err)
			}
			if tt.fallback && (call.Attempts != 2 || call.Served.Ref != fallback) {
				t.Fatalf("attempts=%d served=%s", call.Attempts, call.Served.Ref)
			}
			assertExecutionRecords(t, st, account.ID(), runID, logs.String(), tt.fail)
			if r := call.Reservation; r != nil {
				grant, err := st.LookupGatewayToken(t.Context(), r.Token)
				want := int64(15)
				if tt.fail {
					want = 0
				}
				if err != nil || grant.Spent != want {
					t.Fatalf("reservation spent=%d, want %d: %v", grant.Spent, want, err)
				}
			}
			if n := testutil.CollectAndCount(reg, "kritika_model_calls_total"); n != 1 {
				t.Fatalf("model metric series = %d", n)
			}
		})
	}
}

func assertExecutionRecords(t *testing.T, st *store.Store, accountID, runID, logs string, failed bool) {
	t.Helper()
	if err := st.WithAccount(t.Context(), accountID, func(tx pgx.Tx) error {
		var rows, tokens, calls int
		if err := tx.QueryRow(t.Context(), `SELECT count(*), coalesce(sum(input_tokens + output_tokens), 0)
			FROM usage WHERE runner_run_id = $1`, runID).Scan(&rows, &tokens); err != nil {
			return err
		}
		wantRows, wantTokens := 1, 15
		if failed {
			wantRows, wantTokens = 0, 0
		}
		if rows != wantRows || tokens != wantTokens {
			t.Fatalf("usage rows=%d tokens=%d, want %d/%d", rows, tokens, wantRows, wantTokens)
		}
		var transcript string
		if err := tx.QueryRow(t.Context(), `SELECT count(*), string_agg(coalesce(system, '') || response::text || error, '')
			FROM model_calls WHERE runner_run_id = $1`, runID).Scan(&calls, &transcript); err != nil {
			return err
		}
		if calls != 1 || strings.Contains(transcript+logs, "primary-secret") || strings.Contains(transcript, "backup-secret") {
			t.Fatalf("record count=%d, secrets were not masked", calls)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
