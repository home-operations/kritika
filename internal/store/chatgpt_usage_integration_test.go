//go:build integration

package store

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/transcript"
)

func TestChatGPTUsagePeriods(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, twoAccounts)); err != nil {
		t.Fatal(err)
	}
	account := accountID(t, s, "alpha")
	review := insertReview(t, ctx, s, account)
	run := uuid.NewString()
	base := time.Date(2050, 1, 3, 0, 0, 0, 0, time.UTC)
	monday := base.AddDate(0, 0, -(int(base.Weekday())+6)%7)
	from, to := monday.Add(-time.Hour), monday.Add(time.Hour)
	times := []time.Time{monday.Add(-30 * time.Minute), monday.Add(30 * time.Minute), monday.Add(45 * time.Minute), to}
	t.Cleanup(func() {
		deleteModelCalls(t, s, `review_id = $1`, review)
		if _, err := s.owner.Exec(context.Background(), `DELETE FROM usage WHERE review_id = $1`, review); err != nil {
			t.Error(err)
		}
	})
	if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		var repo string
		if err := tx.QueryRow(ctx, `SELECT p.repository_id FROM reviews v JOIN pull_requests p ON p.id = v.pull_request_id
			WHERE v.id = $1`, review).Scan(&repo); err != nil {
			return err
		}
		for i, at := range times {
			plan := i != 2
			cost := 0.25
			if plan {
				cost = 0
			}
			if err := InsertUsage(ctx, tx, Usage{AccountID: account, RepositoryID: repo, ReviewID: review, RunnerRunID: run,
				Role: RoleReview, Model: review, Input: 100, Output: 10, ChatGPTPlan: plan, CostUSD: cost}); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE usage SET created_at = $2 WHERE review_id = $1 AND created_at = now()`, review, at); err != nil {
				return err
			}
			if err := InsertModelCall(ctx, tx, ModelCall{AccountID: account, ReviewID: review, Kind: ModelCallConfidence,
				Step: i, Model: review, ChatGPTPlan: plan, Usage: model.Usage{Input: 20, CacheRead: 80, Output: 10},
				Row: transcript.Delta(transcript.State{}, model.StepRequest{}, nil).Encode()}); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE model_calls SET created_at = $2 WHERE review_id = $1 AND step = $3`, review, at, i); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, group := range []UsageGroup{UsageByHour, UsageByWeek} {
		for _, planOnly := range []bool{false, true} {
			t.Run(string(group)+"/plan="+map[bool]string{false: "false", true: "true"}[planOnly], func(t *testing.T) {
				if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
					if _, err := tx.Exec(ctx, `SET LOCAL TIME ZONE 'Pacific/Kiritimati'`); err != nil {
						return err
					}
					rows, err := UsageSeries(ctx, tx, group, from, to, planOnly)
					if err != nil {
						return err
					}
					if len(rows) != 2 {
						t.Fatalf("rows = %+v", rows)
					}
					keys := []string{from.Format("2006-01-02T15:00:00Z"), monday.Format("2006-01-02T15:00:00Z")}
					if group == UsageByWeek {
						keys = []string{monday.AddDate(0, 0, -7).Format(time.DateOnly), monday.Format(time.DateOnly)}
					}
					checkPlanPeriods(t, rows, keys, planOnly)
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
	deleteModelCalls(t, s, `review_id = $1`, review)
	if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		rows, err := ListReviewUsage(ctx, tx, review)
		if err != nil {
			return err
		}
		if len(rows) != len(times) {
			t.Fatalf("durable rows = %d", len(rows))
		}
		for _, row := range rows {
			if row.RunnerRunID != run {
				t.Errorf("run association after transcript pruning = %q", row.RunnerRunID)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func checkPlanPeriods(t *testing.T, rows []UsageSeriesRow, keys []string, planOnly bool) {
	t.Helper()
	for i, row := range rows {
		calls, cost := int64(1), 0.0
		if i == 1 && !planOnly {
			calls, cost = 2, 0.25
		}
		if row.Key != keys[i] || row.Calls != calls || row.PlanCalls != 1 || row.InputTokens != calls*100 ||
			row.OutputTokens != calls*10 || row.CacheReadTokens != calls*80 || row.CostUSD != cost {
			t.Errorf("row %d = %+v", i, row)
		}
	}
}
