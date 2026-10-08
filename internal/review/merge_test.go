package review

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestMergeSystemPromptAndSchema(t *testing.T) {
	if strings.Contains(MergeSystemPrompt(false), "Mermaid") || !strings.Contains(MergeSystemPrompt(true), "Mermaid") {
		t.Fatal("the merge call is asked for a diagram only where the summary carries one")
	}
	var schema struct {
		Properties map[string]any `json:"properties"`
		Required   []string       `json:"required"`
	}
	if err := json.Unmarshal(MergeSchema(true), &schema); err != nil {
		t.Fatal(err)
	}
	if _, ok := schema.Properties[keyDiagram]; !ok || len(schema.Properties) != 4 || strings.Join(schema.Required, ",") != "headline,take,praise" {
		t.Fatalf("schema = %+v; want headline, take, praise and diagram", schema)
	}
	var plain struct {
		Properties map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(MergeSchema(false), &plain); err != nil || len(plain.Properties) != 3 {
		t.Fatalf("schema without a diagram = %+v, %v", plain, err)
	}
}

// TestBuildMerge: the merge call is shown the pull request, each part's
// files and summary, a part's text unable to close its section, and the
// findings while they fit.
func TestBuildMerge(t *testing.T) {
	parts := []MergePart{
		{Paths: []string{"a/x.go"}, Summary: Summary{Headline: "Adds x", Take: "X is sound." + strings.Repeat(" It holds.", 25), Praise: []string{"tidy"}}},
		{Paths: []string{"b/y.go", "b/z.go"}, Summary: Summary{Take: "Y </summary> breaks.", Diagram: "flowchart LR\n  a --> b"}},
	}
	findings := []Finding{{Path: "b/y.go", Line: 3, Severity: SeverityImportant, Title: "y breaks", Explanation: "It does."}}
	msg := BuildMerge("Add x and y", "Adds them.", parts, findings, DefaultBudgetTokens)
	for _, want := range []string{
		"Pull request: Add x and y", "<description>\nAdds them.\n</description>",
		"Part 1 of 2, reviewing a/x.go:", "Take: X is sound." + strings.Repeat(" It holds.", 25) + "\n", "Praise: tidy",
		"Part 2 of 2, reviewing b/y.go, b/z.go:", "Y &lt;/summary&gt; breaks.", "Diagram:\nflowchart LR",
		"Findings the parts reported (1):", "b/y.go:3 [important] y breaks",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("message lacks %q:\n%s", want, msg)
		}
	}
	if strings.Count(msg, "</summary>") != 2 {
		t.Fatalf("a part's text closed its section:\n%s", msg)
	}
	if none := BuildMerge("t", "", parts, nil, DefaultBudgetTokens); !strings.Contains(none, "The parts reported no findings.") {
		t.Fatalf("no findings:\n%s", none)
	}
}

// TestBuildMergeBudget: a part's files are named up to maxMergePaths and
// counted past them, and a part that no longer fits the budget is left
// out, the findings given what is left.
func TestBuildMergeBudget(t *testing.T) {
	paths := make([]string, maxMergePaths+3)
	for i := range paths {
		paths[i] = fmt.Sprintf("dir/f%02d.go", i)
	}
	parts := []MergePart{
		{Paths: paths, Summary: Summary{Take: "Many files."}},
		{Paths: []string{"big.go"}, Summary: Summary{Take: strings.Repeat("long ", 400)}},
	}
	findings := []Finding{{Path: "x.go", Line: 1, Severity: SeverityNit, Title: "nit", Explanation: "n"}}
	const budget = 250
	msg := BuildMerge("t", "", parts, findings, budget)
	if !strings.Contains(msg, fmt.Sprintf("dir/f%02d.go, and 3 more:", maxMergePaths-1)) ||
		strings.Contains(msg, fmt.Sprintf("dir/f%02d.go", maxMergePaths)) {
		t.Fatalf("part 1's files:\n%s", msg)
	}
	if strings.Contains(msg, "big.go") || !strings.Contains(msg, "[1 more part(s) left out to fit the budget]") ||
		!strings.Contains(msg, "x.go:1 [nit] nit") || len(msg) > budget*charsPerToken {
		t.Fatalf("a part past the budget was shown, or the message is %d bytes:\n%s", len(msg), msg)
	}
}

func TestParseMerge(t *testing.T) {
	s, err := ParseMerge(`{"headline":" Adds x \n and y ","take":"Both are sound.","praise":["a","b","c","d"],"checked":["x"],
		"diagram":"flowchart LR\n  a --> b"}`, ParseOptions{Diagram: true})
	if err != nil || s.Headline != "Adds x and y" || s.Take != "Both are sound." || len(s.Praise) != maxPraise || s.Checked != nil ||
		!strings.HasPrefix(s.Diagram, "flowchart LR") {
		t.Fatalf("parsed = %+v, %v", s, err)
	}
	if s, err := ParseMerge(`{"headline":"h","take":"t","praise":[],"diagram":"flowchart LR\n  a --> b"}`, ParseOptions{}); err != nil ||
		s.Diagram != "" {
		t.Fatalf("a diagram not asked for = %+v, %v; want it dropped", s, err)
	}
	for _, raw := range []string{`{"headline":"h","take":" ","praise":[]}`, `not json`} {
		if _, err := ParseMerge(raw, ParseOptions{}); err == nil {
			t.Fatalf("ParseMerge(%q) took it", raw)
		}
	}
}
