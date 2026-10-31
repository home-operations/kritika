//go:build bench

package bench

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/review"
)

// answering is a stepper that answers every structured call with raw, or
// fails with err. It records the last request.
func answering(raw string, err error) (model.Stepper, *model.StepRequest) {
	var last model.StepRequest
	return model.StepperFunc(func(_ context.Context, req model.StepRequest) (model.StepResponse, error) {
		last = req
		if err != nil {
			return model.StepResponse{}, err
		}
		return model.StepResponse{
			ToolCalls: []model.ToolCall{{ID: "1", Name: judgeSchemaName, Input: json.RawMessage(raw)}}, CostUSD: 0.001,
		}, nil
	}), &last
}

func TestJudge(t *testing.T) {
	e := Expected{Path: "a.go", Lines: [2]int{10, 12}, Description: "fix: close the body on every return", Must: true}
	f := review.Finding{Path: "a.go", Line: 30, Title: "response body leaks", Explanation: "The early return skips Close.", SuggestedFix: "defer it"}
	for _, tt := range []struct {
		name  string
		raw   string
		err   error
		match bool
	}{
		{name: "a confident match", raw: `{"reasoning":"same leak","match":true,"confidence":0.9}`, match: true},
		{name: "a match at the threshold", raw: `{"reasoning":"likely","match":true,"confidence":0.5}`, match: true},
		{name: "a match under the threshold", raw: `{"reasoning":"unsure","match":true,"confidence":0.4}`},
		{name: "no match", raw: `{"reasoning":"another problem","match":false,"confidence":0.9}`},
		{name: "an answer that is not a verdict", raw: `{"verdict":`},
		{name: "a confidence outside its range", raw: `{"reasoning":"sure","match":true,"confidence":50}`},
		{name: "no reasoning", raw: `{"reasoning":" ","match":true,"confidence":1}`},
		{name: "a provider error", err: errors.New("provider unavailable")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			stepper, last := answering(tt.raw, tt.err)
			j := newJudge(stepper, "vendor/judge", model.Effort("low"))
			v := j.Matches(t.Context(), e, f)
			if v.Match != tt.match || v.Expected != "a.go:10-12" || v.Finding != "a.go:30 response body leaks" {
				t.Fatalf("verdict = %+v, want match %v", v, tt.match)
			}
			if (v.Error == "") != (tt.err == nil && strings.HasSuffix(tt.raw, "}") && !strings.Contains(tt.raw, "50") && !strings.Contains(tt.raw, `" "`)) {
				t.Fatalf("verdict error = %q", v.Error)
			}
			if last.Model != "vendor/judge" || last.Effort != "low" || len(last.Tools) != 1 || last.Tools[0].Name != judgeSchemaName {
				t.Fatalf("request = %+v", *last)
			}
			user := last.Messages[0].Text
			for _, want := range []string{"File: a.go", "lines 10-12", e.Description, "line 30: response body leaks", "The early return", "defer it"} {
				if !strings.Contains(user, want) {
					t.Errorf("the judge was not shown %q:\n%s", want, user)
				}
			}
		})
	}
	if j := newJudge(nil, "vendor/judge", ""); j != nil || j.Name() != "" {
		t.Error("a judge with no stepper")
	}
	if j := newJudge(model.StepperFunc(nil), "", ""); j != nil {
		t.Error("a judge with no model")
	}
	if got := newJudge(model.StepperFunc(nil), "vendor/judge", "").Name(); got != "vendor/judge" {
		t.Errorf("Name() = %q", got)
	}
	if got := newJudge(model.StepperFunc(nil), "vendor/judge", "high").Name(); got != "vendor/judge@high" {
		t.Errorf("Name() = %q", got)
	}
}

func TestJudgeReplacement(t *testing.T) {
	for _, tt := range []struct {
		name string
		end  int
		want string
	}{
		{"one line", 0, "Replacement at lines 30-30:"},
		{"range", 32, "Replacement at lines 30-32:"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			stepper, last := answering(`{"reasoning":"same leak","match":true,"confidence":0.9}`, nil)
			j := newJudge(stepper, "vendor/judge", "")
			f := review.Finding{Path: "a.go", Line: 30, EndLine: tt.end, Title: "body leaks", Replacement: "defer resp.Body.Close()"}
			v := j.Matches(t.Context(), Expected{Path: "a.go", Lines: [2]int{10, 12}}, f)
			if !v.Match {
				t.Fatalf("verdict = %+v", v)
			}
			for _, want := range []string{tt.want, f.Replacement} {
				if !strings.Contains(last.Messages[0].Text, want) {
					t.Errorf("the judge was not shown %q:\n%s", want, last.Messages[0].Text)
				}
			}
		})
	}
}

// TestScoreJudged: a judge is asked only about an expected range that no
// finding landed on, and only about the findings in its file. Its match
// counts, once, and is told apart from a hit by line.
func TestScoreJudged(t *testing.T) {
	c := Case{Expected: []Expected{
		{Path: "a.go", Lines: [2]int{10, 10}, Must: true},
		{Path: "a.go", Lines: [2]int{50, 50}, Must: true},
		{Path: "b.go", Lines: [2]int{5, 5}},
	}}
	cr := &caseResult{Findings: []review.Finding{
		{Path: "a.go", Line: 11, Title: "on the first"},
		{Path: "a.go", Line: 30, Title: "near the second"},
		{Path: "c.go", Line: 5, Title: "elsewhere"},
	}}
	var asked []string
	stepper := model.StepperFunc(func(_ context.Context, req model.StepRequest) (model.StepResponse, error) {
		asked = append(asked, req.Messages[0].Text)
		return model.StepResponse{ToolCalls: []model.ToolCall{{ID: "1", Name: judgeSchemaName,
			Input: json.RawMessage(`{"reasoning":"r","match":true,"confidence":1}`)}}, CostUSD: 0.002}, nil
	})
	score(t.Context(), cr, c, newJudge(stepper, "vendor/judge", ""))
	if cr.MustHit != 2 || cr.NiceHit != 0 || cr.JudgeHit != 1 || cr.Matched != 1 || len(cr.Judged) != 1 || cr.JudgeCost != 0.002 {
		t.Fatalf("scored = %+v", cr)
	}
	if len(asked) != 1 || !strings.Contains(asked[0], "line 30: near the second") {
		t.Fatalf("the judge was asked %q, want the one pair in a.go off its lines", asked)
	}
	plain := &caseResult{Findings: cr.Findings}
	score(t.Context(), plain, c, nil)
	if plain.MustHit != 1 || plain.JudgeHit != 0 || plain.Judged != nil {
		t.Fatalf("scored without a judge = %+v", plain)
	}
}
