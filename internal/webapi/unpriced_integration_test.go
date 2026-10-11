//go:build integration

package webapi

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/store"
	"github.com/home-operations/kritika/internal/transcript"
)

// testUnpricedDashboard adds unpriced calls to the shared fixture's review
// and takes them away again, so the subtests around it see the fixture as
// it was. Its figures are what the calls add to the review's own.
func testUnpricedDashboard(t *testing.T, e *apiEnv) {
	ctx := t.Context()
	a := "/api/v1/accounts/github/wa"
	reviewPath := a + "/reviews/" + e.a.reviewID
	before := e.reviewDetail(t, reviewPath)
	var createdAt time.Time
	if err := e.owner.QueryRow(ctx, `SELECT created_at FROM reviews WHERE id = $1`, e.a.reviewID).Scan(&createdAt); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		for _, q := range []struct {
			sql  string
			args []any
		}{
			{`DELETE FROM usage WHERE review_id = $1 AND unpriced`, []any{e.a.reviewID}},
			{`DELETE FROM model_calls WHERE review_id = $1 AND unpriced`, []any{e.a.reviewID}},
			{`UPDATE agent_runs SET unpriced_steps = 0 WHERE runner_run_id = $1`, []any{e.a.runID}},
			{`UPDATE reviews SET created_at = $2 WHERE id = $1`, []any{e.a.reviewID, createdAt}},
		} {
			if _, err := e.owner.Exec(ctx, q.sql, q.args...); err != nil {
				t.Errorf("clean up: %v", err)
			}
		}
	})
	if err := e.st.WithAccount(ctx, e.a.accountID, func(tx pgx.Tx) error {
		if err := store.InsertUsage(ctx, tx, store.Usage{
			AccountID: e.a.accountID, RepositoryID: e.a.repoID, ReviewID: e.a.reviewID,
			Role: store.RoleConfidence, Model: "anthropic/new-model", Input: 38489, Output: 1210, Unpriced: true,
		}); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE reviews SET created_at = now() WHERE id = $1`, e.a.reviewID); err != nil {
			return err
		}
		return store.InsertModelCall(ctx, tx, store.ModelCall{
			AccountID: e.a.accountID, ReviewID: e.a.reviewID, Kind: store.ModelCallConfidence, Model: "anthropic/new-model",
			Row:   transcript.Delta(transcript.State{}, model.StepRequest{}, nil).Encode(),
			Usage: model.Usage{Input: 38489, Output: 1210}, Unpriced: true,
		})
	}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"/api/v1/accounts", a, reviewPath,
		a + "/pulls/wa/one/7", a + "/pulls", a + "/usage?group=model", a + "/usage?group=role",
		a + "/analytics",
	} {
		t.Run(path, func(t *testing.T) {
			status, body := e.getBody("member-a", path)
			if status != 200 || !bytes.Contains(body, []byte(`"unpricedCalls":1`)) {
				t.Fatalf("status %d: %s", status, body)
			}
		})
	}
	detail := e.reviewDetail(t, reviewPath)
	if detail.Review.CostUSD != before.Review.CostUSD || detail.Review.UnpricedCalls != before.Review.UnpricedCalls+1 {
		t.Fatalf("review billing = %+v, before %+v; usage = %+v", detail.Review, before.Review, detail.Usage)
	}
	var unpriced int
	for _, u := range detail.Usage {
		if u.Unpriced {
			unpriced++
			if u.Model != "anthropic/new-model" || u.InputTokens != 38489 {
				t.Errorf("unpriced usage = %+v", u)
			}
		}
	}
	if unpriced != 1 {
		t.Fatalf("unpriced usage rows = %d", unpriced)
	}
	_, body := e.getBody("member-a", reviewPath+"/transcript")
	var turns Transcript
	if err := json.Unmarshal(body, &turns); err != nil {
		t.Fatal(err)
	}
	unpriced = 0
	for _, turn := range turns.Turns {
		if turn.Unpriced {
			unpriced++
			if turn.Model != "anthropic/new-model" {
				t.Errorf("unpriced turn = %+v", turn)
			}
		}
	}
	if unpriced != 1 {
		t.Fatalf("unpriced turns = %d", unpriced)
	}
	_, body = e.getBody("member-a", a)
	var account AccountDetail
	if err := json.Unmarshal(body, &account); err != nil {
		t.Fatal(err)
	}
	if account.Usage.UnpricedReviews != 1 {
		t.Fatalf("month review billing = %+v", account.Usage)
	}
	checkUnpricedAgentSteps(t, e, reviewPath, before)
}

// checkUnpricedAgentSteps: a call the worker makes for the run, as a split
// review's merge is, is the review's but none of the agent's steps, which
// the runner counts.
func checkUnpricedAgentSteps(t *testing.T, e *apiEnv, reviewPath string, before ReviewDetail) {
	ctx := t.Context()
	if err := e.st.WithAccount(ctx, e.a.accountID, func(tx pgx.Tx) error {
		return store.InsertUsage(ctx, tx, store.Usage{
			AccountID: e.a.accountID, RepositoryID: e.a.repoID, ReviewID: e.a.reviewID, RunnerRunID: e.a.runID,
			Role: store.RoleReview, Model: "api/unpriced-merge", Input: 100, Output: 10, Unpriced: true,
		})
	}); err != nil {
		t.Fatal(err)
	}
	detail := e.reviewDetail(t, reviewPath)
	if detail.AgentRun == nil || detail.AgentRun.UnpricedSteps != 0 || detail.Review.UnpricedCalls != before.Review.UnpricedCalls+2 {
		t.Fatalf("agent billing = %+v; review = %+v", detail.AgentRun, detail.Review)
	}
	if _, err := e.owner.Exec(ctx, `UPDATE agent_runs SET unpriced_steps = 1 WHERE runner_run_id = $1`, e.a.runID); err != nil {
		t.Fatal(err)
	}
	if detail = e.reviewDetail(t, reviewPath); detail.AgentRun == nil || detail.AgentRun.UnpricedSteps != 1 {
		t.Fatalf("agent billing = %+v, want the runner's unpriced step", detail.AgentRun)
	}
}

// reviewDetail reads a review's page as member-a.
func (e *apiEnv) reviewDetail(t *testing.T, path string) ReviewDetail {
	t.Helper()
	status, body := e.getBody("member-a", path)
	var detail ReviewDetail
	if err := json.Unmarshal(body, &detail); status != 200 || err != nil {
		t.Fatalf("status %d, %v: %s", status, err, body)
	}
	return detail
}
