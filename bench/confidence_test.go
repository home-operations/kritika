//go:build bench

package bench

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/review"
)

type confidenceCase struct {
	ID           string              `json:"id"`
	Repository   string              `json:"repository"`
	Title        string              `json:"title"`
	Body         string              `json:"body"`
	Diff         string              `json:"diff"`
	Result       review.Result       `json:"result"`
	Sources      []string            `json:"sources"`
	Earlier      *review.EarlierRisk `json:"earlier,omitempty"`
	Instructions string              `json:"instructions,omitempty"`
	Score        [2]int              `json:"score"`
	Risk         review.Risk         `json:"risk"`
}

func loadConfidenceCases(path string) ([]stageCase, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var fixtures []confidenceCase
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		return nil, err
	}
	if len(fixtures) == 0 {
		return nil, fmt.Errorf("bench: no confidence cases")
	}
	var cases []stageCase
	seen := map[string]bool{}
	for _, f := range fixtures {
		if f.ID == "" || seen[f.ID] || f.Repository == "" || f.Diff == "" || !f.Risk.Valid() ||
			f.Score[0] < 0 || f.Score[1] > review.MaxConfidence || f.Score[0] > f.Score[1] {
			return nil, fmt.Errorf("bench: confidence case %q needs a unique id, repository, diff and valid score/risk expectations", f.ID)
		}
		seen[f.ID] = true
		system := review.ConfidenceSystemPrompt(f.Instructions)
		cases = append(cases, stageCase{id: f.ID, request: model.CompletionRequest{
			System: system,
			User: review.BuildConfidence(review.Input{Repository: f.Repository, Title: f.Title, Body: f.Body,
				Diff: f.Diff, Changed: review.ChangedPaths(f.Diff)}, f.Result, f.Sources, f.Earlier, system, review.DefaultBudgetTokens),
			Schema: review.ConfidenceSchema(), SchemaName: "confidence",
		}, check: func(raw string) error { return checkConfidence(f, raw) }})
	}
	return cases, nil
}

func checkConfidence(f confidenceCase, raw string) error {
	score, risk, reason, err := review.ParseConfidence(raw, f.Repository, f.Result.Counts())
	if err != nil {
		return err
	}
	var answer struct {
		Risk review.Risk `json:"risk"`
	}
	if err := json.Unmarshal([]byte(raw), &answer); err != nil {
		return err
	}
	if !answer.Risk.Valid() || strings.TrimSpace(reason) == "" {
		return fmt.Errorf("bench: incomplete risk or reason")
	}
	if score < f.Score[0] || score > f.Score[1] || risk != f.Risk {
		return fmt.Errorf("bench: score/risk %d/%s, want [%d,%d]/%s", score, risk, f.Score[0], f.Score[1], f.Risk)
	}
	return nil
}

func TestConfidenceBench(t *testing.T) {
	cases, err := loadConfidenceCases(envOr("KRITIKA_BENCH_CONFIDENCE_CASES", "cases/confidence.json"))
	if err != nil {
		t.Fatal(err)
	}
	runStageBench(t, "confidence", cases, 4096)
}

func TestConfidenceEvaluation(t *testing.T) {
	cases, err := loadConfidenceCases("cases/confidence.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		if c.request.SchemaName != "confidence" || !json.Valid(c.request.Schema) || c.request.User == "" {
			t.Fatalf("bad request: %+v", c)
		}
	}
	f := confidenceCase{Repository: "example/app", Score: [2]int{0, 2}, Risk: review.RiskCritical,
		Result: review.Result{Findings: []review.Finding{{Severity: review.SeverityP0}}}}
	for _, tt := range []struct {
		name, raw string
		bad       bool
	}{
		{"score ceiling", `{"score":5,"risk":"critical","reason":"Drops production records."}`, false},
		{"understates risk", `{"score":2,"risk":"low","reason":"Drops production records."}`, true},
		{"missing risk", `{"score":2,"reason":"Drops production records."}`, true},
		{"missing reason", `{"score":2,"risk":"critical"}`, true},
		{"malformed", `broken`, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := checkConfidence(f, tt.raw); (err != nil) != tt.bad {
				t.Fatalf("check = %v; want error %v", err, tt.bad)
			}
		})
	}
}
