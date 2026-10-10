//go:build integration

package store

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/transcript"
)

func TestChatGPTBilling(t *testing.T) {
	s := openStore(t)
	if err := s.ApplyConfig(t.Context(), parse(t, twoAccounts)); err != nil {
		t.Fatal(err)
	}
	account := accountID(t, s, "alpha")
	for _, tt := range []struct {
		name      string
		calls     []Usage
		wantCost  float64
		wantPlans int64
	}{
		{"plan", []Usage{{ChatGPTPlan: true}}, 0, 1},
		{"mixed", []Usage{{ChatGPTPlan: true}, {CostUSD: 0.25}, {}}, 0.25, 1},
		{"free API", []Usage{{}}, 0, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := t.Context()
			id := insertReview(t, ctx, s, account)
			run := uuid.NewString()
			wantPlan := make(map[string]bool)
			for i, u := range tt.calls {
				wantPlan[strconv.Itoa(i)] = u.ChatGPTPlan
			}
			t.Cleanup(func() {
				deleteModelCalls(t, s, `review_id = $1`, id)
				if _, err := s.owner.Exec(context.Background(), `DELETE FROM usage WHERE review_id = $1`, id); err != nil {
					t.Error(err)
				}
			})
			if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
				var repo string
				if err := tx.QueryRow(ctx, `SELECT p.repository_id FROM reviews v JOIN pull_requests p ON p.id = v.pull_request_id
					WHERE v.id = $1`, id).Scan(&repo); err != nil {
					return err
				}
				for i, u := range tt.calls {
					u.RunnerRunID = run
					u.AccountID, u.RepositoryID, u.ReviewID, u.Role, u.Model = account, repo, id, RoleReview, id
					u.Input, u.Output, u.Upstream = 100, 10, strconv.Itoa(i)
					if err := InsertUsage(ctx, tx, u); err != nil {
						return err
					}
					if err := InsertModelCall(ctx, tx, ModelCall{
						AccountID: account, ReviewID: id, Kind: ModelCallConfidence, Step: i, Model: id, Upstream: u.Upstream,
						Row:   transcript.Delta(transcript.State{}, model.StepRequest{}, nil).Encode(),
						Usage: model.Usage{Input: 100, Output: 10}, CostUSD: u.CostUSD, ChatGPTPlan: u.ChatGPTPlan,
					}); err != nil {
						return err
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
				v, err := FindReview(ctx, tx, id)
				if err != nil {
					return err
				}
				if v.CostUSD != tt.wantCost || v.PlanCalls != tt.wantPlans || v.Calls != int64(len(tt.calls)) {
					t.Errorf("review billing = %v, %d/%d plan calls", v.CostUSD, v.PlanCalls, v.Calls)
				}
				usage, err := ListReviewUsage(ctx, tx, id)
				if err != nil {
					return err
				}
				if len(usage) != len(tt.calls) {
					t.Fatalf("usage rows = %d", len(usage))
				}
				checkPlanUsage(t, usage, wantPlan, run)
				checkPlanTranscript(t, tx, id, wantPlan)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
				rows, err := UsageSeries(ctx, tx, UsageByModel, time.Now().Add(-time.Minute), time.Now().Add(time.Minute), false)
				if err != nil {
					return err
				}
				for _, u := range rows {
					if u.Key != id {
						continue
					}
					if u.CostUSD != tt.wantCost || u.PlanCalls != tt.wantPlans || u.Calls != int64(len(tt.calls)) ||
						u.InputTokens != 100*u.Calls || u.OutputTokens != 10*u.Calls {
						t.Errorf("usage series billing = %+v", u)
					}
					return nil
				}
				t.Error("model missing from usage series")
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestChatGPTPlanCostRefused(t *testing.T) {
	s := openStore(t)
	if err := s.ApplyConfig(t.Context(), parse(t, twoAccounts)); err != nil {
		t.Fatal(err)
	}
	account := accountID(t, s, "alpha")
	ctx := t.Context()
	id := insertReview(t, ctx, s, account)
	t.Cleanup(func() {
		deleteModelCalls(t, s, `review_id = $1`, id)
		if _, err := s.owner.Exec(context.Background(), `DELETE FROM usage WHERE review_id = $1`, id); err != nil {
			t.Error(err)
		}
	})
	if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		var repo string
		if err := tx.QueryRow(ctx, `SELECT p.repository_id FROM reviews v JOIN pull_requests p ON p.id = v.pull_request_id
			WHERE v.id = $1`, id).Scan(&repo); err != nil {
			return err
		}
		return InsertUsage(ctx, tx, Usage{AccountID: account, RepositoryID: repo, ReviewID: id, Role: RoleReview, ChatGPTPlan: true, CostUSD: 99})
	}); err == nil || !strings.Contains(err.Error(), "usage_plan_cost") {
		t.Fatalf("a plan usage row with a cost = %v", err)
	}
	if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		return InsertModelCall(ctx, tx, ModelCall{
			AccountID: account, ReviewID: id, Kind: ModelCallConfidence, Row: transcript.Delta(transcript.State{}, model.StepRequest{}, nil).Encode(),
			CostUSD: 99, ChatGPTPlan: true,
		})
	}); err == nil || !strings.Contains(err.Error(), "model_calls_plan_cost") {
		t.Fatalf("a plan model call with a cost = %v", err)
	}
}

func checkPlanUsage(t *testing.T, usage []UsageRow, wantPlan map[string]bool, run string) {
	t.Helper()
	for i, u := range usage {
		if u.RunnerRunID != run || u.ChatGPTPlan != wantPlan[u.Upstream] || (u.ChatGPTPlan && u.CostUSD != 0) {
			t.Errorf("usage %d = %+v", i, u)
		}
	}
}

func checkPlanTranscript(t *testing.T, tx pgx.Tx, id string, wantPlan map[string]bool) {
	t.Helper()
	rows, err := ReviewModelCalls(t.Context(), tx, id)
	if err != nil {
		t.Fatal(err)
	}
	turns := transcript.Rebuild(rows).Turns
	if len(turns) != len(wantPlan) {
		t.Fatalf("turns = %d", len(turns))
	}
	for i, turn := range turns {
		if turn.ChatGPTPlan != wantPlan[turn.Upstream] || (turn.ChatGPTPlan && turn.CostUSD != 0) {
			t.Errorf("turn %d = %+v", i, turn)
		}
	}
}
