//go:build integration

package webapi

import (
	"bytes"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/store"
	"github.com/home-operations/kritika/internal/transcript"
)

func testChatGPTBilling(t *testing.T, e *apiEnv) {
	ctx := t.Context()
	err := e.st.WithAccount(ctx, e.a.accountID, func(tx pgx.Tx) error {
		if err := store.InsertUsage(ctx, tx, store.Usage{
			AccountID: e.a.accountID, RepositoryID: e.a.repoID, ReviewID: e.a.reviewID, RunnerRunID: e.a.runID,
			Role: store.RoleReview, Model: "historical/plan", Input: 500, Output: 50, ChatGPTPlan: true,
		}); err != nil {
			return err
		}
		return store.InsertModelCall(ctx, tx, store.ModelCall{
			AccountID: e.a.accountID, ReviewID: e.a.reviewID, Kind: store.ModelCallConfidence, Model: "historical/plan",
			Row: transcript.Delta(transcript.State{}, model.StepRequest{}, nil).Encode(), ChatGPTPlan: true,
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	a := "/api/v1/accounts/github/wa"
	for _, tt := range []struct {
		path string
		want []string
	}{
		{a + "/reviews/" + e.a.reviewID, []string{`"costUsd":0.5,"calls":2,"planCalls":1`, `"costUsd":0,"chatgptPlan":true`, `"runnerRunId":"` + e.a.runID + `"`}},
		{a + "/reviews/" + e.a.reviewID + "/transcript", []string{`"model":"historical/plan"`, `"costUsd":0,"chatgptPlan":true`}},
		{a + "/pulls/wa/one/7", []string{`"costUsd":0.5,"calls":2,"planCalls":1`}},
		{a + "/usage?group=model", []string{`"key":"historical/plan"`, `"costUsd":0,"calls":1,"planCalls":1`}},
		{a + "/usage?group=hour&billing=chatgpt", []string{`"inputTokens":500`, `"costUsd":0,"calls":1,"planCalls":1`}},
		{a + "/usage?group=week&billing=chatgpt", []string{`"inputTokens":500`, `"costUsd":0,"calls":1,"planCalls":1`}},
	} {
		t.Run(tt.path, func(t *testing.T) {
			status, body := e.getBody("member-a", tt.path)
			if status != 200 {
				t.Fatalf("status %d: %s", status, body)
			}
			for _, want := range tt.want {
				if !bytes.Contains(body, []byte(want)) {
					t.Errorf("missing %s in %s", want, body)
				}
			}
		})
	}
}
