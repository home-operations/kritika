//go:build integration

package webapi

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/store"
	"github.com/home-operations/kritika/internal/transcript"
)

func testUnpricedDashboard(t *testing.T, e *apiEnv) {
	ctx := t.Context()
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
	a := "/api/v1/accounts/github/wa"
	for _, path := range []string{
		"/api/v1/accounts", a, a + "/reviews/" + e.a.reviewID,
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
	_, body := e.getBody("member-a", a+"/reviews/"+e.a.reviewID)
	var detail ReviewDetail
	if err := json.Unmarshal(body, &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Review.CostUSD != 0.5 || detail.Review.UnpricedCalls != 1 {
		t.Fatalf("review billing = %+v; usage = %+v", detail.Review, detail.Usage)
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
	_, body = e.getBody("member-a", a+"/reviews/"+e.a.reviewID+"/transcript")
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
	if account.Usage.ReviewUnpricedCalls != 1 {
		t.Fatalf("month review billing = %+v", account.Usage)
	}
	if err := e.st.WithAccount(ctx, e.a.accountID, func(tx pgx.Tx) error {
		return store.InsertUsage(ctx, tx, store.Usage{
			AccountID: e.a.accountID, RepositoryID: e.a.repoID, ReviewID: e.a.reviewID, RunnerRunID: e.a.runID,
			Role: store.RoleReview, Model: "api/unpriced-agent", Input: 100, Output: 10, Unpriced: true,
		})
	}); err != nil {
		t.Fatal(err)
	}
	_, body = e.getBody("member-a", a+"/reviews/"+e.a.reviewID)
	if err := json.Unmarshal(body, &detail); err != nil {
		t.Fatal(err)
	}
	if detail.AgentRun == nil || detail.AgentRun.UnpricedCalls != 1 || detail.Review.UnpricedCalls != 2 {
		t.Fatalf("agent billing = %+v; review = %+v", detail.AgentRun, detail.Review)
	}
}
