//go:build integration

package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
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

// fallbackRef is the backup provider's model, whose one slot a fallback
// step takes. jobID is the review job that holds it.
const (
	fallbackRef configfile.ModelRef = "backup/review"
	jobID       int64               = 7
)

// executionFixture is a store with the execution account applied, its file,
// account and repository.
func executionFixture(t *testing.T) (*store.Store, *configfile.File, *configfile.Account, string) {
	t.Helper()
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
    concurrency: 1
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
	return st, file, account, configfile.RepositoryID(account.ID(), "execution/repo")
}

func TestExecutionAccounting(t *testing.T) {
	st, file, account, repoID := executionFixture(t)
	for _, tt := range []struct {
		name                         string
		structured, fallback, cancel bool
		fail, invalid                bool
	}{
		{name: "runner settles reservation"},
		{name: "runner refunds failure", fail: true},
		{name: "runner fallback holds the fallback's slot", fallback: true},
		{name: "structured fallback survives cancellation", structured: true, fallback: true, cancel: true},
		{name: "invalid structured answer is still billed", structured: true, invalid: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			runID := newRunnerRun(t, st, account.ID())
			var logs bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&logs, nil))
			steppers := &Steppers{Build: func(p configfile.Provider) (model.Stepper, error) {
				return model.StepperFunc(func(sctx context.Context, req model.StepRequest) (model.StepResponse, error) {
					if tt.fail || (tt.fallback && strings.Contains(p.BaseURL, "primary")) {
						return model.StepResponse{}, errors.New("provider failed: primary-secret")
					}
					if strings.Contains(p.BaseURL, "backup") {
						// The fallback's slot is held by the review's job while
						// the fallback answers.
						if held := liveLeases(t, st, account.ID(), jobID); held != 1 {
							t.Errorf("the fallback stepped with %d live leases of its job on %s, want 1", held, fallbackRef)
						}
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
			var configured configfile.ModelRef
			if tt.fallback {
				configured = fallbackRef
			}
			call, err := steppers.Execution(file, account, "primary/review", configured, tt.structured, logger)
			if err != nil {
				t.Fatal(err)
			}
			reg := prometheus.NewRegistry()
			call.Recorder = Recorder{Store: st, Metrics: metrics.New(reg)}
			call.Record = store.ModelCall{AccountID: account.ID(), RunnerRunID: runID, Kind: store.ModelCallAgentStep}
			call.Usage = store.Usage{AccountID: account.ID(), RepositoryID: repoID, RunnerRunID: runID, Role: store.RoleReview}
			call.Job = func(context.Context) (int64, error) { return jobID, nil }
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
				call.Reservation = &Reservation{Token: token, Tokens: 50}
				_, err = call.Step(ctx, model.StepRequest{System: system})
			} else {
				call.Record.Kind = store.ModelCallConfidence
				_, err = call.Complete(ctx, model.CompletionRequest{System: system, SchemaName: "answer"})
			}
			if (err != nil) != (tt.fail || tt.invalid) {
				t.Fatalf("call: %v", err)
			}
			if tt.fallback && (call.Attempts != 2 || call.Served.Ref != fallbackRef) {
				t.Fatalf("attempts=%d served=%s", call.Attempts, call.Served.Ref)
			}
			if tt.fallback {
				// The fallback's one slot, which exists once taken, is let go
				// with the step.
				if held := liveLeases(t, st, account.ID(), 0); held != 0 {
					t.Fatalf("%d live leases on %s after the step, want none", held, fallbackRef)
				}
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

// TestExecutionFallbackWaitsForSlot: a fallback step waits while another job
// holds the fallback's sole slot, and goes on once it is let go.
func TestExecutionFallbackWaitsForSlot(t *testing.T) {
	st, file, account, repoID := executionFixture(t)
	ctx := t.Context()
	runID := newRunnerRun(t, st, account.ID())
	holder, err := st.TakeLease(ctx, account.ID(), string(fallbackRef), 1, 99)
	if err != nil || holder == nil {
		t.Fatalf("TakeLease = %v, %v", holder, err)
	}
	released := false
	defer func() {
		if !released {
			_, _ = holder.Release(context.Background())
		}
	}()
	var backupSteps atomic.Int32
	steppers := &Steppers{Build: func(p configfile.Provider) (model.Stepper, error) {
		return model.StepperFunc(func(context.Context, model.StepRequest) (model.StepResponse, error) {
			if strings.Contains(p.BaseURL, "primary") {
				return model.StepResponse{}, errors.New("provider failed")
			}
			backupSteps.Add(1)
			input, _ := json.Marshal(map[string]string{"text": "ok"})
			return model.StepResponse{Model: "served-model", Usage: model.Usage{Input: 10, Output: 3}, Stop: model.StopToolUse,
				ToolCalls: []model.ToolCall{{ID: "answer", Name: "answer", Input: input}}}, nil
		}), nil
	}}
	call, err := steppers.Execution(file, account, "primary/review", fallbackRef, true, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	reg := prometheus.NewRegistry()
	call.Recorder = Recorder{Store: st, Metrics: metrics.New(reg)}
	call.Record = store.ModelCall{AccountID: account.ID(), RunnerRunID: runID, Kind: store.ModelCallConfidence}
	call.Usage = store.Usage{AccountID: account.ID(), RepositoryID: repoID, RunnerRunID: runID, Role: store.RoleConfidence}
	call.Job = func(context.Context) (int64, error) { return jobID, nil }
	done := make(chan error, 1)
	go func() {
		_, err := call.Complete(ctx, model.CompletionRequest{System: "sys", SchemaName: "answer"})
		done <- err
	}()
	// The call waits for the slot: well past the lease poll, the backup has
	// not stepped and the slot is still the holder's.
	select {
	case err := <-done:
		t.Fatalf("the call ended while the slot was held: %v", err)
	case <-time.After(3 * time.Second):
	}
	if n := backupSteps.Load(); n != 0 {
		t.Fatalf("the backup stepped %d times while its slot was held", n)
	}
	if held := liveLeases(t, st, account.ID(), 99); held != 1 {
		t.Fatalf("%d live leases of the holder, want 1", held)
	}
	if _, err := holder.Release(ctx); err != nil {
		t.Fatal(err)
	}
	released = true
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the call failed once the slot was free: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the call did not go on once the slot was free")
	}
	if n := backupSteps.Load(); n != 1 || call.Served.Ref != fallbackRef {
		t.Fatalf("backup steps = %d, served %s; want 1 on %s", n, call.Served.Ref, fallbackRef)
	}
	if held := liveLeases(t, st, account.ID(), 0); held != 0 {
		t.Fatalf("%d live leases on %s after the step, want none", held, fallbackRef)
	}
	if n := testutil.CollectAndCount(reg, "kritika_lease_wait_seconds"); n != 1 {
		t.Fatalf("lease wait series = %d, want 1", n)
	}
}

// newRunnerRun inserts a review run of the account, failed once the test ends.
func newRunnerRun(t *testing.T, st *store.Store, accountID string) string {
	t.Helper()
	ctx := t.Context()
	var runID string
	if err := st.WithAccount(ctx, accountID, func(tx pgx.Tx) error {
		var err error
		runID, err = store.InsertRunnerRun(ctx, tx, accountID, store.RunnerKindReview, "", 0)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		if err := st.WithAccount(ctx, accountID, func(tx pgx.Tx) error {
			return store.FailRunnerRun(ctx, tx, runID, "test finished")
		}); err != nil {
			t.Error(err)
		}
	})
	return runID
}

// liveLeases counts the account's live leases on fallbackRef: those of
// jobID, or of any job when jobID is 0.
func liveLeases(t *testing.T, st *store.Store, accountID string, jobID int64) int {
	t.Helper()
	ctx := context.Background()
	var held int
	if err := st.WithAccount(ctx, accountID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM model_leases WHERE account_id = $1 AND model_key = $2
			AND job_id IS NOT NULL AND expires_at >= now() AND ($3 = 0 OR job_id = $3)`, accountID, string(fallbackRef), jobID).Scan(&held)
	}); err != nil {
		t.Fatal(err)
	}
	return held
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
