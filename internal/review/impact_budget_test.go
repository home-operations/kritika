package review

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestPriorImpactDoesNotDisplaceDiff(t *testing.T) {
	large := "diff --git a/large.go b/large.go\n" + strings.Repeat("+x\n", 4400) + "+last_line\n"
	notes := make([]string, maxImpact)
	for i := range notes {
		notes[i] = fmt.Sprintf("Note %d: ", i) + strings.Repeat("evidence ", 100)
	}
	for _, incremental := range []bool{false, true} {
		t.Run(fmt.Sprintf("incremental=%t", incremental), func(t *testing.T) {
			in := Input{BudgetTokens: 5000, Body: strings.Repeat("description ", 2000), Diff: large}
			if incremental {
				in.Diff = sampleDiff
				in.Incremental = &IncrementalInput{PriorHeadSHA: "1234567", DeltaDiff: large}
			} else {
				in.Earlier = &EarlierInput{HeadSHA: "1234567"}
			}
			before, omittedBefore, _, _ := Build(in)
			if !strings.Contains(before, "+last_line") {
				t.Fatal("fixture does not fit without earlier impact")
			}
			if incremental {
				in.Incremental.Impact = notes
			} else {
				in.Earlier.Impact = notes
			}
			after, omittedAfter, _, _ := Build(in)
			if !slices.Equal(omittedBefore, omittedAfter) || !strings.Contains(after, "+last_line") ||
				!strings.Contains(after, impactOmitted) || len(after) > in.BudgetTokens*charsPerToken {
				t.Fatalf("impact displaced the diff or exceeded the budget: omitted=%v, prompt:\n%s", omittedAfter, after)
			}
		})
	}
}

func TestPriorImpactOmissionMarker(t *testing.T) {
	in := Input{BudgetTokens: 2000, Diff: sampleDiff, Earlier: &EarlierInput{HeadSHA: "1234567", Impact: []string{impactNote}}}
	for i := range 100 {
		in.Earlier.Findings = append(in.Earlier.Findings, Finding{Path: "x.go", Line: i + 1, Title: "Earlier defect", Explanation: strings.Repeat("x", 200)})
		in.Dismissed = append(in.Dismissed, DismissedFinding{Path: "x.go", Line: i + 1, Title: "Dismissed", Explanation: strings.Repeat("y", 200)})
	}
	msg, _, _, _ := Build(in)
	if !strings.Contains(msg, impactOmitted) || strings.Contains(msg, impactNote) || len(msg) > in.BudgetTokens*charsPerToken {
		t.Fatalf("a completely omitted impact list must be marked within budget:\n%s", msg)
	}
}

func TestContinuationUsesLatestImpact(t *testing.T) {
	for _, notes := range [][]string{nil, {impactNote}} {
		t.Run(fmt.Sprintf("notes=%d", len(notes)), func(t *testing.T) {
			msg, _, _ := BuildContinuation(ContinueInput{PriorHeadSHA: "1234567", HeadSHA: "abcdef0", Impact: notes})
			if len(notes) == 0 {
				if !strings.Contains(msg, noPriorImpact) || strings.Contains(msg, reassessImpact) {
					t.Fatalf("empty latest impact must not request reassessment of older notes:\n%s", msg)
				}
			} else if !strings.Contains(msg, impactNote) || !strings.Contains(msg, reassessImpact) {
				t.Fatalf("latest impact was not shown:\n%s", msg)
			}
		})
	}
}

func TestMergeImpactDoesNotDisplaceParts(t *testing.T) {
	parts := make([]MergePart, 8)
	for i := range parts {
		parts[i].Summary.Take = strings.Repeat("summary ", 200)
		parts[i].Summary.Diagram = "flowchart LR\n" + strings.Repeat(" A --> B\n", 400)
		for j := range maxImpact {
			parts[i].Summary.Impact = append(parts[i].Summary.Impact, fmt.Sprintf("Part %d note %d: ", i, j)+strings.Repeat("evidence ", 110))
		}
	}
	findings := []Finding{{Path: "last.go", Line: 1, Title: "Last finding", Explanation: "Concrete defect."}}
	msg := BuildMerge("t", strings.Repeat("description ", 2000), parts, findings, DefaultBudgetTokens)
	if strings.Contains(msg, "more part(s) omitted") || !strings.Contains(msg, "Part 8 of 8") ||
		!strings.Contains(msg, "Last finding") || !strings.Contains(msg, "impact notes omitted") || len(msg) > DefaultBudgetTokens*charsPerToken {
		t.Fatalf("impact crowded out a core summary or finding, or exceeded the budget (%d bytes):\n%s", len(msg), msg)
	}
}
