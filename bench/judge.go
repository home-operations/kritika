//go:build bench

package bench

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/review"
)

// judgeSchemaName is the tool the judge answers by calling.
const judgeSchemaName = "verdict"

// MatchConfidence is the least confidence a judge's match counts at: a
// coin toss and under is no match.
const MatchConfidence = 0.5

// judgeMaxTokens bounds a verdict, which is a sentence and two numbers.
const judgeMaxTokens = 1024

var judgeSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"reasoning": {"type": "string", "description": "One or two sentences on why, written before the verdict."},
		"match": {"type": "boolean", "description": "Whether the finding identifies the same underlying defect the fix addressed."},
		"confidence": {"type": "number", "minimum": 0, "maximum": 1, "description": "How sure the verdict is, 0 to 1."}
	},
	"required": ["reasoning", "match", "confidence"],
	"additionalProperties": false
}`)

const judgeSystem = "You compare one code review finding with one known defect in the same file, which a later commit fixed. " +
	"Say whether the finding identifies the same underlying problem the fix addressed, whatever lines it points at and however " +
	"it words it. A finding about a different problem in the file, a general remark, or one that names the symptom without " +
	"the defect does not match. Judge from what is given; do not assume the finding is right because it is near the lines."

// judge decides, as Kodus's recall judge does, whether a finding identifies
// the defect an expected entry describes when its lines do not land on it,
// so a review that found the defect and anchored it a few lines away, or
// on its cause, still counts. It is a model of its own, with its verdicts
// recorded so a floor can be pinned to it.
type judge struct {
	completer model.Structured
	model     string
	effort    model.Effort
}

// newJudge is a judge on stepper, nil when name is "": the bench then
// matches on lines alone.
func newJudge(stepper model.Stepper, name string, effort model.Effort) *judge {
	if name == "" || stepper == nil {
		return nil
	}
	return &judge{completer: model.Structured{Stepper: stepper}, model: name, effort: effort}
}

// Name is the judge as a floor is pinned to it: its model and effort.
func (j *judge) Name() string {
	if j == nil {
		return ""
	}
	if j.effort == "" {
		return j.model
	}
	return j.model + "@" + string(j.effort)
}

// verdict is one judged pair.
type verdict struct {
	Expected   string  `json:"expected"`
	Finding    string  `json:"finding"`
	Match      bool    `json:"match"`
	Confidence float64 `json:"confidence"`
	Reasoning  string  `json:"reasoning,omitempty"`
	CostUSD    float64 `json:"cost_usd"`
	Error      string  `json:"error,omitempty"`
}

// Matches asks whether f identifies the defect e describes. An error is
// no match, recorded on the verdict; the pair is never asked again.
func (j *judge) Matches(ctx context.Context, e Expected, f review.Finding) verdict {
	v := verdict{Expected: fmt.Sprintf("%s:%d-%d", e.Path, e.Lines[0], e.Lines[1]), Finding: fmt.Sprintf("%s:%d %s", f.Path, f.Line, f.Title)}
	var b strings.Builder
	fmt.Fprintf(&b, "File: %s\n\nKnown defect, at lines %d-%d of the file as the pull request left it, fixed later by a commit "+
		"titled:\n%s\n\n", e.Path, e.Lines[0], e.Lines[1], e.Description)
	fmt.Fprintf(&b, "Candidate finding, at line %d: %s\n\n%s\n", f.Line, f.Title, f.Explanation)
	if f.SuggestedFix != "" {
		fmt.Fprintf(&b, "\nSuggested fix: %s\n", f.SuggestedFix)
	}
	b.WriteString("\nDoes the candidate identify the same underlying defect the fix addressed?")
	resp, err := j.completer.Complete(ctx, model.CompletionRequest{
		System: judgeSystem, User: b.String(), Model: j.model, Schema: judgeSchema, SchemaName: judgeSchemaName,
		MaxTokens: judgeMaxTokens, Effort: j.effort,
	})
	v.CostUSD = resp.CostUSD
	if err != nil {
		v.Error = err.Error()
		return v
	}
	var answer struct {
		Reasoning  string  `json:"reasoning"`
		Match      bool    `json:"match"`
		Confidence float64 `json:"confidence"`
	}
	if err := json.Unmarshal([]byte(resp.Raw), &answer); err != nil {
		v.Error = "verdict: " + err.Error()
		return v
	}
	if answer.Confidence < 0 || answer.Confidence > 1 || strings.TrimSpace(answer.Reasoning) == "" {
		v.Error = fmt.Sprintf("verdict: confidence %v outside 0 to 1, or no reasoning", answer.Confidence)
		return v
	}
	v.Reasoning, v.Confidence = answer.Reasoning, answer.Confidence
	v.Match = answer.Match && answer.Confidence >= MatchConfidence
	return v
}
