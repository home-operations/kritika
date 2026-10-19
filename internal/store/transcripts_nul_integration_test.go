//go:build integration

package store

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/transcript"
)

func TestModelCallNUL(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, twoAccounts)); err != nil {
		t.Fatal(err)
	}
	account := accountID(t, s, "alpha")
	reviewID := insertReview(t, ctx, s, account)
	t.Cleanup(func() { deleteModelCalls(t, s, `review_id = $1`, reviewID) })
	req := model.StepRequest{
		System:   "system\x00text",
		Messages: []model.Message{{Role: model.RoleUser, Text: "text\x00end"}},
		Tools:    []model.ToolDef{{Name: "tool", InputSchema: json.RawMessage(`{"description":"\u0000","literal":"\\u0000"}`)}},
	}
	row := transcript.Delta(transcript.State{}, req, nil)
	row.Response = transcript.NewResponse(model.StepResponse{Text: "response\x00end",
		ToolCalls: []model.ToolCall{{ID: "call", Name: "tool", Input: json.RawMessage(`{"text":"\u0000"}`)}}}, nil)
	if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		return InsertModelCall(ctx, tx, ModelCall{AccountID: account, ReviewID: reviewID, Kind: ModelCallConfidence,
			Model: "model\x00name", Upstream: "up\x00stream", Stop: model.StopReason("stop\x00reason"), Error: "error\x00text", Row: row.Encode()})
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		rows, err := ReviewModelCalls(ctx, tx, reviewID)
		if err != nil {
			return err
		}
		if len(rows) != 1 {
			t.Fatalf("stored %d calls", len(rows))
		}
		got := rows[0]
		if *got.System != "system\ufffdtext" || got.Messages[0].Text != "text\ufffdend" || got.Response.Text != "response\ufffdend" ||
			got.Model != "model\ufffdname" || got.Upstream != "up\ufffdstream" || got.Error != "error\ufffdtext" {
			t.Fatalf("stored row = %+v", got)
		}
		if !strings.Contains(string((*got.Tools)[0].InputSchema), `\\u0000`) {
			t.Fatalf("literal escape was lost: %s", (*got.Tools)[0].InputSchema)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
