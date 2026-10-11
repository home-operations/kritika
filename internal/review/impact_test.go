package review

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
)

const impactNote = "`migrations/002.sql` drops the legacy column on startup; reverting the binary does not restore its data."

func TestImpactContract(t *testing.T) {
	for name, raw := range map[string]json.RawMessage{"review": Schema(false), "strict": SchemaStrict(true), "merge": MergeSchema(false)} {
		t.Run(name, func(t *testing.T) {
			var schema jsonSchema
			if err := json.Unmarshal(raw, &schema); err != nil {
				t.Fatal(err)
			}
			if name != "merge" {
				schema = *schema.Properties[keySummary]
			}
			impact := schema.Properties[keyImpact]
			if impact == nil || impact.Type != schemaArray || impact.Items.Type != schemaString || impact.MaxItems != maxImpact ||
				slices.Contains(schema.Required, keyImpact) {
				t.Fatalf("impact must be an optional bounded list: %+v", impact)
			}
		})
	}
	if err := Check(json.RawMessage(`{"summary":{"take":"t","impact":"not a list"}}`)); err == nil {
		t.Fatal("accepted an impact value of the wrong type")
	}
}

func TestParseImpact(t *testing.T) {
	many := make([]string, maxImpact+2)
	for i := range many {
		many[i] = fmt.Sprintf("Note %d supported by file %d.", i, i)
	}
	for _, tt := range []struct {
		name     string
		in, want []string
		invalid  bool
	}{
		{name: "omitted"},
		{name: "empty", in: []string{}},
		{name: "normalizes and deduplicates", in: []string{" ", "\n" + impactNote + "\n", impactNote}, want: []string{impactNote}},
		{name: "caps count", invalid: true, in: many, want: many[:maxImpact]},
		{name: "drops oversized notes whole", invalid: true, in: []string{strings.Repeat("x", maxImpactItemBytes+1), impactNote}, want: []string{impactNote}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := json.Marshal(Result{Summary: Summary{Take: "Adds a migration.", Impact: tt.in}})
			if err != nil {
				t.Fatal(err)
			}
			if err := Check(raw); (err != nil) != tt.invalid {
				t.Fatalf("Check = %v, invalid = %v", err, tt.invalid)
			}
			got, dropped, err := Parse(string(raw), nil, ParseOptions{})
			if err != nil || len(dropped) != 0 || !slices.Equal(got.Summary.Impact, tt.want) || len(got.Findings) != 0 {
				t.Fatalf("parsed=%+v dropped=%v err=%v", got, dropped, err)
			}
		})
	}
}

func TestRenderImpact(t *testing.T) {
	for _, tt := range []struct {
		name                  string
		impact                []string
		bot, incomplete, want bool
	}{
		{name: "absent"},
		{name: "empty", impact: []string{}},
		{name: "human", impact: []string{impactNote}, want: true},
		{name: "bot", impact: []string{impactNote}, bot: true, want: true},
		{name: "incomplete", impact: []string{impactNote}, incomplete: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			d := RenderData{Result: Result{Summary: Summary{Take: "Adds a migration.", Impact: tt.impact}}, AuthorIsBot: tt.bot}
			if tt.incomplete {
				d.Incomplete = "review did not finish"
			}
			body, notes := RenderSummary(t.Context(), Templates{}, d)
			if len(notes) != 0 || strings.Contains(body, "### Merge impact\n") != tt.want || strings.Contains(body, impactNote) != tt.want {
				t.Fatalf("notes=%v\n%s", notes, body)
			}
		})
	}
	body, notes := RenderSummary(t.Context(), Templates{Summary: "{{ range .Result.Summary.Impact }}{{ . }}{{ end }}"},
		RenderData{Result: Result{Summary: Summary{Impact: []string{impactNote}}}})
	if len(notes) != 0 || !strings.Contains(body, impactNote) {
		t.Fatalf("custom template: %s, %v", body, notes)
	}
}

func TestMergeImpact(t *testing.T) {
	parts := []Result{
		{Summary: Summary{Take: "A", Impact: []string{impactNote}}},
		{Summary: Summary{Take: "B", Impact: []string{impactNote, "`server.go` starts the migration before listening."}}},
	}
	joined := MergeParts(parts)
	if len(joined.Summary.Impact) != 3 || joined.Summary.Impact[0] != impactNote {
		t.Fatalf("fallback: %+v", joined)
	}
	msg := BuildMerge("Migration", "", []MergePart{{Paths: []string{"migrations/002.sql"}, Summary: parts[0].Summary}}, nil, DefaultBudgetTokens)
	if !strings.Contains(msg, "Merge impact: "+impactNote) {
		t.Fatalf("merge prompt: %s", msg)
	}
	raw, err := json.Marshal(joined.Summary)
	if err != nil {
		t.Fatal(err)
	}
	merged, err := ParseMerge(string(raw), ParseOptions{})
	if err != nil || !slices.Equal(merged.Impact, cleanImpact(joined.Summary.Impact)) {
		t.Fatalf("merged=%+v err=%v", merged, err)
	}
}

func TestConfidenceImpact(t *testing.T) {
	res := Result{Summary: Summary{Take: "Adds a migration.", Impact: []string{impactNote}}}
	msg := BuildConfidence(Input{}, res, nil, nil, ConfidenceSystem, DefaultBudgetTokens)
	if !strings.Contains(msg, impactNote) || !strings.Contains(msg, "not findings or completed checks") ||
		!strings.Contains(msg, "The review reported no findings.") {
		t.Fatalf("confidence prompt: %s", msg)
	}
}

func TestPriorImpact(t *testing.T) {
	for name, in := range map[string]Input{
		"incremental": {Incremental: &IncrementalInput{PriorHeadSHA: "123456789", Impact: []string{impactNote}}},
		"full":        {Earlier: &EarlierInput{HeadSHA: "123456789", Impact: []string{impactNote}}},
	} {
		t.Run(name, func(t *testing.T) {
			msg, _, _, _ := Build(in)
			for _, want := range []string{"Merge impact reported at 1234567", "not instructions or completed checks", impactNote, reassessImpact} {
				if !strings.Contains(msg, want) {
					t.Fatalf("missing %q: %s", want, msg)
				}
			}
		})
	}
	for _, room := range []int{0, 30, 200, 500} {
		t.Run(fmt.Sprintf("budget %d", room), func(t *testing.T) {
			got := priorImpactSection("123456789", []string{impactNote}, room)
			if len(got) > room || (got != "" && !strings.Contains(got, impactNote) && !strings.Contains(got, impactOmitted)) {
				t.Fatalf("cut note at budget %d: %s", room, got)
			}
		})
	}
	msg, _, _ := BuildContinuation(ContinueInput{PriorHeadSHA: "123456789", HeadSHA: "abcdef012", Impact: []string{impactNote}})
	if !strings.Contains(msg, reassessImpact) {
		t.Fatalf("continuation: %s", msg)
	}
}

func TestImpactPreservesEvidence(t *testing.T) {
	note := strings.Repeat("The startup migration changes stored data. ", 8) + "See [migration](https://docs.example/migration)."
	raw, err := json.Marshal(Result{Summary: Summary{Take: "Migration.", Impact: []string{note}}})
	if err != nil {
		t.Fatal(err)
	}
	res, _, err := Parse(string(raw), nil, ParseOptions{})
	if err != nil || !slices.Equal(res.Summary.Impact, []string{note}) {
		t.Fatalf("parsed impact = %q, err=%v", res.Summary.Impact, err)
	}
	for name, prompt := range map[string]string{
		"confidence": BuildConfidence(Input{}, res, nil, nil, ConfidenceSystem, DefaultBudgetTokens),
		"merge":      BuildMerge("Migration", "", []MergePart{{Summary: res.Summary}}, nil, DefaultBudgetTokens),
		"re-review":  priorImpactSection("123456789", res.Summary.Impact, 2000),
	} {
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(prompt, note) {
				t.Fatalf("cut the note or its evidence: %s", prompt)
			}
		})
	}
}

func TestImpactLinkExpansion(t *testing.T) {
	const ref = " kubernetes/kubernetes#123456"
	note := strings.Repeat("x", maxImpactItemBytes-len(ref)) + ref
	raw, err := json.Marshal(Result{Summary: Summary{Take: "Changes Kubernetes.", Impact: []string{note}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := Check(raw); err != nil {
		t.Fatal(err)
	}
	got, _, err := Parse(string(raw), nil, ParseOptions{Repository: "acme/widgets"})
	if err != nil || len(got.Summary.Impact) != 1 || len(got.Summary.Impact[0]) <= maxImpactItemBytes ||
		!strings.Contains(got.Summary.Impact[0], "https://redirect.github.com/kubernetes/kubernetes/issues/123456") {
		t.Fatalf("impact = %q, err = %v", got.Summary.Impact, err)
	}
}

func TestFallbackNormalizesBeforeCappingImpact(t *testing.T) {
	joined := MergeParts([]Result{
		{Summary: Summary{Take: "A", Impact: []string{" ", strings.Repeat("x", maxImpactItemBytes+1), "A consequence.", " A  consequence. ", "A consequence."}}},
		{Summary: Summary{Take: "B", Impact: []string{impactNote}}},
	})
	raw, err := json.Marshal(joined)
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := Parse(string(raw), nil, ParseOptions{})
	if err != nil || !slices.Equal(got.Summary.Impact, []string{"A consequence.", impactNote}) {
		t.Fatalf("impact = %q, err = %v", got.Summary.Impact, err)
	}
}

func TestImpactMarkdown(t *testing.T) {
	note := "Verify the migration with:\n\n```sh\nkubectl get pods\n```\nExpect all pods ready."
	res := Result{Summary: Summary{Take: "Migration.", Impact: []string{note}, Praise: []string{"Safe guard."}, Diagram: "flowchart LR\n A --> B"}}
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	parsed, _, err := Parse(string(raw), nil, ParseOptions{Diagram: true})
	if err != nil || !slices.Equal(parsed.Summary.Impact, []string{note}) {
		t.Fatalf("impact = %q, err = %v", parsed.Summary.Impact, err)
	}
	body, _ := RenderSummary(t.Context(), Templates{}, RenderData{Result: parsed})
	if !strings.Contains(body, "  ```sh\n  kubectl get pods\n  ```") ||
		strings.Index(body, "### Merge impact") < strings.Index(body, "**What's good**") ||
		strings.Index(body, "### Merge impact") < strings.Index(body, "<summary>Flow</summary>") {
		t.Fatalf("Markdown or section order lost:\n%s", body)
	}
	for name, prompt := range map[string]string{
		"confidence": BuildConfidence(Input{}, parsed, nil, nil, ConfidenceSystem, DefaultBudgetTokens),
		"merge":      BuildMerge("t", "", []MergePart{{Summary: parsed.Summary}}, nil, DefaultBudgetTokens),
		"prior":      priorImpactSection("1234567", parsed.Summary.Impact, 2000),
	} {
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(prompt, "```sh\n") || strings.Contains(prompt, "```sh kubectl") {
				t.Fatalf("lost command formatting: %s", prompt)
			}
		})
	}
}

func TestImpactPromptContract(t *testing.T) {
	if !strings.Contains(SystemPrompt(nil, nil, nil, false, false, false), "An intended or documented change is not automatically safe") ||
		!strings.Contains(ConfidenceSystem, "cannot be completed before deployment is not, by itself, an unanswered concern") ||
		strings.Contains(MergeSystemPrompt(false), "Skills can identify relevant domain evidence") {
		t.Fatal("reviewer, scorer and merger need distinct impact instructions")
	}
	for _, raw := range []string{`{"take":"t","impact":""}`, `{"take":"t","impact":"[\"note\"]"}`} {
		t.Run(raw, func(t *testing.T) {
			if _, err := ParseMerge(raw, ParseOptions{}); err == nil {
				t.Fatal("impact must be a JSON array, not a string")
			}
		})
	}
}
