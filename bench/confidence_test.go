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
		// The judge gives a reach to the versions it is listed, and rates the
		// rest in its risk, as the worker has it.
		bumps := review.Bumps(f.Diff)
		listed := review.ListedBumps(bumps)
		cases = append(cases, stageCase{id: f.ID, request: model.CompletionRequest{
			System: system,
			User: review.BuildConfidence(review.Input{Repository: f.Repository, Title: f.Title, Body: f.Body,
				Diff: f.Diff, Changed: review.ChangedPaths(f.Diff), Bumps: bumps}, f.Result, review.Record{Sources: f.Sources}, f.Earlier, system,
				review.DefaultBudgetTokens),
			Schema: review.ConfidenceSchema(), SchemaName: "confidence",
		}, check: func(raw string) error { return checkConfidence(f, listed, raw) }})
	}
	return cases, nil
}

// checkConfidence judges the answer as the worker does: the score derives
// from the findings and the concern, and the risk is held to the steps of
// the versions the judge was listed, bumps, by the reaches the answer gives
// them.
func checkConfidence(f confidenceCase, bumps []review.Bump, raw string) error {
	// ParseConfidence reads the first JSON value. The answer must be one.
	if !json.Valid([]byte(raw)) {
		return fmt.Errorf("bench: answer is not one JSON value")
	}
	p, err := review.ParseConfidence(raw, f.Repository, bumps)
	if err != nil {
		return err
	}
	if p.Defaulted() || strings.TrimSpace(p.Reason) == "" {
		return fmt.Errorf("bench: incomplete risk or reason")
	}
	c := review.Confidence{Score: review.Score(f.Result.Counts(), p.Concern), Risk: p.Risk}
	c.HoldStep(review.StepFloor(bumps, p.Reaches))
	if c.Score < f.Score[0] || c.Score > f.Score[1] || c.Risk != f.Risk {
		return fmt.Errorf("bench: score/risk %d/%s (judge %s), want [%d,%d]/%s", c.Score, c.Risk, p.Risk, f.Score[0], f.Score[1], f.Risk)
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
		{"within range", `{"risk":"critical","concern":false,"bumps":[],"reason":"Drops production records."}`, false},
		{"a concern changes nothing under a ceiling", `{"risk":"critical","concern":true,"bumps":[],"reason":"Drops production records."}`, false},
		{"understates risk", `{"risk":"low","concern":false,"bumps":[],"reason":"Drops production records."}`, true},
		{"missing risk", `{"concern":false,"bumps":[],"reason":"Drops production records."}`, true},
		{"missing concern", `{"risk":"critical","bumps":[],"reason":"Drops production records."}`, true},
		{"missing reason", `{"risk":"critical","concern":false,"bumps":[]}`, true},
		{"malformed", `broken`, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := checkConfidence(f, nil, tt.raw); (err != nil) != tt.bad {
				t.Fatalf("check = %v; want error %v", err, tt.bad)
			}
		})
	}
	// The risk is held to the step of a moved version by the reach the
	// answer gives it, as the worker holds it.
	bump := confidenceCase{Repository: "example/app", Score: [2]int{5, 5}, Risk: review.RiskHigh,
		Diff: "diff --git a/Chart.yaml b/Chart.yaml\n--- a/Chart.yaml\n+++ b/Chart.yaml\n@@ -1 +1 @@\n-version: 1.2.0\n+version: 1.3.0\n"}
	bumps := review.Bumps(bump.Diff)
	if len(bumps) != 1 {
		t.Fatalf("bumps = %+v, want the chart version", bumps)
	}
	for _, tt := range []struct {
		name, raw string
		bad       bool
	}{
		{"a central one rates high", `{"risk":"low","concern":false,"bumps":[{"path":"Chart.yaml","line":1,"reach":"central"}],"reason":"Routine."}`, false},
		{"a standalone one rates as the judge says", `{"risk":"low","concern":false,"bumps":[{"path":"Chart.yaml","line":1,"reach":"standalone"}],"reason":"Routine."}`, true},
		{"no reach is standalone", `{"risk":"low","concern":false,"bumps":[],"reason":"Routine."}`, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := checkConfidence(bump, bumps, tt.raw); (err != nil) != tt.bad {
				t.Fatalf("check = %v; want error %v", err, tt.bad)
			}
		})
	}
	// A version past the list's end gets no floor, as in the worker.
	var many strings.Builder
	many.WriteString("diff --git a/README.md b/README.md\n--- a/README.md\n+++ b/README.md\n@@ -1,80 +1,80 @@\n")
	for range 80 {
		many.WriteString("-see v1.0 of the docs\n")
	}
	for range 80 {
		many.WriteString("+see v2.0 of the docs\n")
	}
	long := confidenceCase{Repository: "example/app", Score: [2]int{5, 5}, Risk: review.RiskLow, Diff: many.String()}
	all := review.Bumps(long.Diff)
	listed := review.ListedBumps(all)
	if len(all) != 80 || len(listed) == 0 || len(listed) == 80 {
		t.Fatalf("%d bumps, %d listed; want 80 with the list cut", len(all), len(listed))
	}
	answered := make([]string, 0, len(listed))
	for _, b := range listed {
		answered = append(answered, fmt.Sprintf(`{"path":"README.md","line":%d,"reach":"none"}`, b.Line))
	}
	raw := `{"risk":"low","concern":false,"bumps":[` + strings.Join(answered, ",") + `],"reason":"Docs only."}`
	if err := checkConfidence(long, listed, raw); err != nil {
		t.Fatalf("a reach of none for every listed version: %v", err)
	}
	if err := checkConfidence(long, all, raw); err == nil {
		t.Fatal("the unlisted versions got no floor")
	}
}
