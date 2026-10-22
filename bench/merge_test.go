//go:build bench

package bench

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/review"
)

type mergeCase struct {
	ID       string             `json:"id"`
	Title    string             `json:"title"`
	Body     string             `json:"body"`
	Parts    []review.MergePart `json:"parts"`
	Findings []review.Finding   `json:"findings"`
	Diagram  bool               `json:"diagram"`
	Evidence []string           `json:"evidence"`
}

func loadMergeCases(path string) ([]stageCase, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var fixtures []mergeCase
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		return nil, err
	}
	if len(fixtures) == 0 {
		return nil, fmt.Errorf("bench: no merge cases")
	}
	var cases []stageCase
	seen := map[string]bool{}
	for _, f := range fixtures {
		if f.ID == "" || seen[f.ID] || len(f.Parts) < 2 || len(f.Evidence) == 0 {
			return nil, fmt.Errorf("bench: merge case %q needs a unique id, two parts and evidence checks", f.ID)
		}
		seen[f.ID] = true
		cases = append(cases, stageCase{id: f.ID, request: model.CompletionRequest{
			System: review.MergeSystemPrompt(f.Diagram),
			User:   review.BuildMerge(f.Title, f.Body, f.Parts, f.Findings, review.DefaultBudgetTokens),
			Schema: review.MergeSchema(f.Diagram), SchemaName: "summary",
		}, check: func(raw string) error { return checkMerge(f, raw) }})
	}
	return cases, nil
}

func checkMerge(f mergeCase, raw string) error {
	s, err := review.ParseMerge(raw, review.ParseOptions{Diagram: f.Diagram})
	if err != nil {
		return err
	}
	if f.Diagram && s.Diagram == "" {
		return fmt.Errorf("bench: missing diagram")
	}
	text := strings.ToLower(s.Take)
	for _, evidence := range f.Evidence {
		if !strings.Contains(text, strings.ToLower(evidence)) {
			return fmt.Errorf("bench: missing evidence marker %q (inspect paraphrases manually)", evidence)
		}
	}
	return nil
}

func TestMergeBench(t *testing.T) {
	cases, err := loadMergeCases(envOr("KRITIKA_BENCH_MERGE_CASES", "cases/merge.json"))
	if err != nil {
		t.Fatal(err)
	}
	runStageBench(t, "merge", cases, 16384)
}

func TestMergeEvaluation(t *testing.T) {
	cases, err := loadMergeCases("cases/merge.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		if c.request.SchemaName != "summary" || !json.Valid(c.request.Schema) || c.request.User == "" {
			t.Fatalf("bad request: %+v", c)
		}
		if r := evaluateStage(t.Context(), c, nil); r.Latency != 0 || r.Error != "" {
			t.Fatalf("dry result = %+v", r)
		}
	}
	f := mergeCase{Evidence: []string{"migrate.go", "after backup"}}
	for _, tt := range []struct {
		name, raw string
		bad       bool
	}{
		{"keeps conditions", `{"take":"migrate.go changes storage after backup.","praise":[]}`, false},
		{"drops condition", `{"take":"migrate.go changes storage.","praise":[]}`, true},
		{"malformed", `broken`, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := stageCase{id: tt.name, request: cases[0].request, check: func(raw string) error { return checkMerge(f, raw) }}
			stepper := model.StepperFunc(func(_ context.Context, req model.StepRequest) (model.StepResponse, error) {
				return model.StepResponse{ToolCalls: []model.ToolCall{{Name: req.Tools[0].Name, Input: json.RawMessage(tt.raw)}}}, nil
			})
			r := evaluateStage(t.Context(), c, stepper)
			if r.Error != "" || (r.CheckError != "") != tt.bad || r.Response.Raw != tt.raw {
				t.Fatalf("result = %+v", r)
			}
		})
	}
}
