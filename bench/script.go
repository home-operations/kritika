//go:build bench

package bench

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/review"
)

// scripted is a model that answers from the corpus rather than from a
// provider: a review request gets a submission with a finding on every
// expected range of the case it was told, and a judge request a match.
// It stands in for the vendor so a run exercises every other line of the
// bench and of the review code it calls, fetch to report, with no key and
// at no cost: the wiring smoke CI runs.
type scripted struct {
	mu   sync.Mutex
	c    Case
	runs int
}

// use sets the case the next review requests are answered for.
func (s *scripted) use(c Case) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.c = c
}

func (s *scripted) Step(_ context.Context, req model.StepRequest) (model.StepResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runs++
	usage := model.Usage{Input: int64(len(req.System)+len(req.Messages[0].Text)) / 4, Output: 64}
	for _, t := range req.Tools {
		switch t.Name {
		case judgeSchemaName:
			verdict := json.RawMessage(`{"reasoning":"scripted","match":true,"confidence":1}`)
			return model.StepResponse{
				ToolCalls: []model.ToolCall{{ID: "judge", Name: t.Name, Input: verdict}}, Stop: model.StopToolUse, Usage: usage, Model: "scripted",
			}, nil
		case "submit_review":
			res := review.Result{Summary: review.Summary{
				Headline: "Scripted review.", Take: "A scripted answer, one finding on each expected range.", Praise: []string{},
			}}
			for _, e := range s.c.Expected {
				res.Findings = append(res.Findings, review.Finding{
					Path: e.Path, Line: e.Lines[0], Severity: review.SeverityP1, Category: review.CategoryCorrectness,
					Title: strings.TrimSpace(e.Description), Explanation: "The corpus expects a finding here.",
				})
			}
			raw, err := json.Marshal(res)
			if err != nil {
				return model.StepResponse{}, err
			}
			return model.StepResponse{
				ToolCalls: []model.ToolCall{{ID: "submit", Name: t.Name, Input: raw}}, Stop: model.StopToolUse, Usage: usage, Model: "scripted",
			}, nil
		}
	}
	return model.StepResponse{}, fmt.Errorf("bench: scripted: no tool to answer with among %d offered", len(req.Tools))
}
