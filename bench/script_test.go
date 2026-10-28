//go:build bench

package bench

import (
	"encoding/json"
	"testing"

	"github.com/home-operations/kritika/internal/agent"
	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/review"
	"github.com/home-operations/kritika/internal/runner"
)

// TestScripted: the script answers a review with a finding on each
// expected range of its case, as a submission the loop takes, and a
// judge with a match.
func TestScripted(t *testing.T) {
	s := &scripted{}
	s.use(Case{Expected: []Expected{
		{Path: "a.go", Lines: [2]int{3, 4}, Description: "fix: close the body", Must: true},
		{Path: "b.go", Lines: [2]int{7, 7}, Description: "fix: nil check"},
	}})
	out := agent.Run{Stepper: s, Model: "scripted", System: "s", User: "u", Submit: runner.SubmitTool(false, false), Limits: agent.Limits{MaxSteps: 1}}.Do(t.Context())
	if out.Stop != agent.StopSubmitted || out.Steps != 1 {
		t.Fatalf("run = %+v", out)
	}
	var res review.Result
	if err := json.Unmarshal(out.Submitted, &res); err != nil || len(res.Findings) != 2 || res.Findings[0].Path != "a.go" ||
		res.Findings[0].Line != 3 || res.Findings[0].Title != "fix: close the body" || res.Findings[1].Line != 7 || res.Summary.Take == "" {
		t.Fatalf("submitted = %s, %v", out.Submitted, err)
	}
	if err := review.Check(out.Submitted); err != nil {
		t.Fatalf("the script's submission breaks the contract: %v", err)
	}
	v := newJudge(s, "scripted", "").Matches(t.Context(), Expected{Path: "a.go"}, review.Finding{Path: "a.go"})
	if !v.Match || v.Confidence != 1 || v.Error != "" {
		t.Fatalf("judged = %+v", v)
	}
	if _, err := s.Step(t.Context(), model.StepRequest{Messages: []model.Message{{}}, Tools: []model.ToolDef{{Name: "grep"}}}); err == nil {
		t.Error("a request with no tool the script answers with")
	}
}
