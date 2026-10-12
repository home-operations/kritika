//go:build integration

package store

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/transcript"
)

func TestUsageFollowupGrouping(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, soloAccount("usage-followup"))); err != nil {
		t.Fatal(err)
	}
	account := accountID(t, s, "usage-followup")
	review := insertReview(t, ctx, s, account)
	if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		var repo string
		if err := tx.QueryRow(ctx, `SELECT repository_id FROM pull_requests WHERE id = (SELECT pull_request_id FROM reviews WHERE id = $1)`, review).Scan(&repo); err != nil {
			return err
		}
		for _, followup := range []bool{false, true} {
			id, role, comment := review, RoleReview, int64(0)
			if followup {
				id, role, comment = "", RoleFollowUp, 123
			}
			if err := InsertUsage(ctx, tx, Usage{AccountID: account, RepositoryID: repo, ReviewID: id, Role: role, Model: "test", Input: 100, Output: 10, CostUSD: 1}); err != nil {
				return err
			}
			if err := InsertModelCall(ctx, tx, ModelCall{AccountID: account, ReviewID: review, FollowupCommentID: comment, Kind: ModelCallAgentStep, Model: "test", Usage: model.Usage{CacheRead: 80, CacheWrite: 5}, Row: transcript.Delta(transcript.State{}, model.StepRequest{}, nil).Encode()}); err != nil {
				return err
			}
		}
		rows, err := UsageSeries(ctx, tx, UsageByReview, time.Now().Add(-time.Hour), time.Now().Add(time.Hour), false, UsageFilter{Repository: new("usage-followup/one"), Model: new("test")})
		if err != nil {
			return err
		}
		if len(rows) != 2 {
			t.Fatalf("rows = %+v", rows)
		}
		for i, key := range []string{"", review} {
			r := rows[i]
			if r.Key != key || r.InputTokens != 100 || r.OutputTokens != 10 || r.CacheReadTokens != 80 || r.CacheWriteTokens != 5 || r.Calls != 1 || r.CostUSD != 1 {
				t.Fatalf("row = %+v, key %q", r, key)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestUsageReviewLimit(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, soloAccount("usage-limit"))); err != nil {
		t.Fatal(err)
	}
	account := accountID(t, s, "usage-limit")
	review := insertReview(t, ctx, s, account)
	from, to := time.Date(2052, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2052, 1, 2, 0, 0, 0, 0, time.UTC)
	if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `WITH seeded AS (
   INSERT INTO reviews (account_id, pull_request_id, head_sha, status)
   SELECT $1, pull_request_id, n::text, 'completed' FROM reviews CROSS JOIN generate_series(1, 55) n WHERE id = $2 RETURNING id, pull_request_id, head_sha)
   INSERT INTO usage (account_id, repository_id, review_id, role, model, input_tokens, output_tokens, cost_usd, created_at)
   SELECT $1, p.repository_id, v.id, 'review', 'test', 56 - v.head_sha::int, 1, v.head_sha::int, $3 FROM seeded v JOIN pull_requests p ON p.id = v.pull_request_id`, account, review, from)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO usage (account_id, repository_id, role, model, input_tokens, output_tokens, cost_usd, created_at)
   SELECT $1, repository_id, 'followup', 'test', 100, 10, 10000, $3 FROM pull_requests
   WHERE id = (SELECT pull_request_id FROM reviews WHERE id = $2)`, account, review, from)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, byTokens := range []bool{false, true} {
		t.Run(map[bool]string{false: "cost", true: "tokens"}[byTokens], func(t *testing.T) {
			if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
				filter := UsageFilter{OrderByTokens: byTokens}
				rows, err := UsageSeries(ctx, tx, UsageByReview, from, to, false, filter)
				if err != nil {
					return err
				}
				if len(rows) != 52 {
					t.Fatalf("rows = %d, want 50 plus unattributed and remainder", len(rows))
				}
				var cost float64
				var calls, tokens int64
				for _, r := range rows {
					cost += r.CostUSD
					calls += r.Calls
					tokens += r.InputTokens + r.OutputTokens
					if r.Key == "__other__" {
						want := 15.0
						if byTokens {
							want = 265
						}
						if r.CostUSD != want || r.Calls != 5 {
							t.Errorf("remainder = %+v", r)
						}
					}
				}
				if cost != 11540 || calls != 56 || tokens != 1705 {
					t.Fatalf("totals: %v, %d, %d", cost, calls, tokens)
				}
				for _, group := range []UsageGroup{UsageByHour, UsageByDay, UsageByWeek, UsageByReview, UsageByModel} {
					previous, err := UsageComparison(ctx, tx, group, from, to, false, filter)
					if err != nil {
						return err
					}
					if len(previous) != 1 || previous[0].Calls != calls || previous[0].CostUSD != cost || previous[0].InputTokens+previous[0].OutputTokens != tokens {
						t.Fatalf("comparison %s = %+v", group, previous)
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestQueueAttention(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, twoAccounts)); err != nil {
		t.Fatal(err)
	}
	account := accountID(t, s, "alpha")
	other := accountID(t, s, "beta")
	var repo string
	if err := s.owner.QueryRow(ctx, `SELECT id FROM repositories WHERE account_id = $1 LIMIT 1`, account).Scan(&repo); err != nil {
		t.Fatal(err)
	}
	add := func(account, state string, number int, age time.Duration) int64 {
		t.Helper()
		id := insertRiverJob(t, ctx, s, "review", state, 1, 5, age, `{}`)
		if _, err := s.owner.Exec(ctx, `UPDATE river_job SET args = jsonb_build_object('account_id', $2::text, 'repository_id', $3::text, 'number', $4::int), finalized_at = CASE WHEN finalized_at IS NOT NULL THEN now() - $5 * interval '1 second' END WHERE id = $1`, id, account, repo, number, age.Seconds()); err != nil {
			t.Fatal(err)
		}
		return id
	}
	stale := add(account, "running", 1, 10*time.Minute)
	fresh := add(account, "running", 2, time.Minute)
	beating := add(account, "running", 3, 10*time.Minute)
	if err := s.HeartbeatJob(ctx, beating); err != nil {
		t.Fatal(err)
	}
	if err := s.HeartbeatJob(ctx, fresh); err != nil {
		t.Fatal(err)
	}
	if _, err := s.owner.Exec(ctx, `UPDATE job_heartbeats SET heartbeat_at = now() - interval '1 hour' WHERE job_id = $1`, fresh); err != nil {
		t.Fatal(err)
	}
	recent := add(account, "discarded", 4, time.Hour)
	old := add(account, "discarded", 5, 25*time.Hour)
	recovered := add(account, "discarded", 6, time.Hour)
	add(account, "completed", 6, time.Minute)
	retrying := add(account, "discarded", 7, time.Hour)
	add(account, "available", 7, 0)
	add(other, "completed", 4, time.Minute)
	if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		rows, err := ListQueue(ctx, tx)
		if err != nil {
			return err
		}
		got := map[int64]JobRow{}
		for _, r := range rows {
			got[r.ID] = r
		}
		for _, tc := range []struct {
			id               int64
			overdue, failure bool
		}{
			{stale, true, false}, {fresh, false, false}, {beating, false, false}, {recent, false, true}, {old, false, false}, {recovered, false, false}, {retrying, false, false},
		} {
			r, ok := got[tc.id]
			if !ok || r.HeartbeatOverdue != tc.overdue || r.RecentFailure != tc.failure {
				t.Errorf("job %d = %+v, want %+v", tc.id, r, tc)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
