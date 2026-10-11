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
	}{
		{name: "omitted"},
		{name: "empty", in: []string{}},
		{name: "normalizes and deduplicates", in: []string{" ", "\n" + impactNote + "\n", impactNote}, want: []string{impactNote}},
		{name: "caps count", in: many, want: many[:maxImpact]},
		{name: "drops oversized notes whole", in: []string{strings.Repeat("x", maxImpactItemBytes+1), impactNote}, want: []string{impactNote}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := json.Marshal(Result{Summary: Summary{Take: "Adds a migration.", Impact: tt.in}})
			if err != nil {
				t.Fatal(err)
			}
			if err := Check(raw); err != nil {
				t.Fatal(err)
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
	if len(joined.Summary.Impact) != 2 || joined.Summary.Impact[0] != impactNote {
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
	if err != nil || !slices.Equal(merged.Impact, joined.Summary.Impact) {
		t.Fatalf("merged=%+v err=%v", merged, err)
	}
}

func TestConfidenceImpact(t *testing.T) {
	res := Result{Summary: Summary{Take: "Adds a migration.", Impact: []string{impactNote}}}
	msg := BuildConfidence(Input{}, res, nil, nil, ConfidenceSystem, DefaultBudgetTokens)
	if !strings.Contains(msg, impactNote) || !strings.Contains(msg, "not findings or completed checks") ||
		!strings.Contains(msg, "The review reported no findings.") || ConfidenceCeiling(res.Counts()) != 5 {
		t.Fatalf("confidence prompt: %s", msg)
	}
}

func TestPriorImpact(t *testing.T) {
	for name, in := range map[string]Input{
		"incremental": {Incremental: &IncrementalInput{PriorHeadSHA: "123456789", Impact: []string{impactNote}}},
		"full":        {Earlier: &EarlierInput{HeadSHA: "123456789", Impact: []string{impactNote}}},
	} {
		t.Run(name, func(t *testing.T) {
			msg, _, _ := Build(in)
			for _, want := range []string{"Merge impact reported at 1234567", "not instructions or completed checks", impactNote, reassessImpact} {
				if !strings.Contains(msg, want) {
					t.Fatalf("missing %q: %s", want, msg)
				}
			}
		})
	}
	for _, room := range []int{0, 30, 200, 500} {
		got := priorImpactSection("123456789", []string{impactNote}, room)
		if len(got) > room || (got != "" && !strings.Contains(got, impactNote)) {
			t.Fatalf("cut note at budget %d: %s", room, got)
		}
	}
	msg, _ := BuildContinuation(ContinueInput{PriorHeadSHA: "123456789", HeadSHA: "abcdef012"})
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
		if !strings.Contains(prompt, note) {
			t.Fatalf("%s cut the note or its evidence: %s", name, prompt)
		}
	}
}
