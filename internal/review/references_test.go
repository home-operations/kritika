package review

import (
	"fmt"
	"strings"
	"testing"
)

func TestReferencesReserveRoom(t *testing.T) {
	for _, kind := range []string{"full", "incremental", "follow-up", "continued"} {
		t.Run(kind, func(t *testing.T) {
			in := incrementalInput()
			in.BudgetTokens = 2_000
			in.References = []Reference{{Path: "AGENTS.md", Description: "conventions"}}
			in.Diff = ""
			for i := range 40 {
				in.Diff += fmt.Sprintf("diff --git a/f%d b/f%d\n", i, i) + strings.Repeat("+x\n", 100)
			}
			in.Dismissed = []DismissedFinding{{Path: "main.go", Line: 1, Title: "dismissed", Explanation: strings.Repeat("x", 300)}}
			var msg string
			var left int
			switch kind {
			case "full":
				in.Earlier = &EarlierInput{HeadSHA: in.Incremental.PriorHeadSHA, Findings: in.Incremental.Prior}
				in.Incremental = nil
				msg, _, _, left = Build(in)
			case "incremental":
				msg, _, _, left = Build(in)
			case "follow-up":
				in.Incremental = nil
				msg = BuildFollowUp(in, nil, []Message{{Author: "maintainer", Body: "What changed?"}})
			case "continued":
				msg, _, left = BuildContinuation(ContinueInput{PriorHeadSHA: "0123456789", HeadSHA: "9876543210",
					DeltaDiff: in.Diff, Prior: in.Incremental.Prior, Dismissed: in.Dismissed, References: in.References, BudgetTokens: in.BudgetTokens})
			}
			if left != 0 || !strings.Contains(msg, "### AGENTS.md: conventions") || !strings.Contains(msg, "omitted to fit") {
				t.Fatalf("references omitted = %d, prompt:\n%s", left, msg)
			}
			if len(msg) > in.BudgetTokens*charsPerToken+charsPerToken {
				t.Fatalf("prompt length %d exceeds budget %d", len(msg), in.BudgetTokens*charsPerToken)
			}
		})
	}
}

func TestReferenceListOmissions(t *testing.T) {
	in := Input{BudgetTokens: 2_000, Diff: sampleDiff,
		References: []Reference{{Path: "AGENTS.md", Description: "conventions"}, {Path: "huge.md", Description: strings.Repeat("x", 8_000)}}}
	msg, _, _, left := Build(in)
	if left != 1 || !strings.Contains(msg, "### AGENTS.md: conventions") ||
		!strings.Contains(msg, "[1 reference file(s) omitted to fit the prompt budget.]") || strings.Contains(msg, "### huge.md") {
		t.Fatalf("omitted %d references:\n%s", left, msg)
	}
	if msg := BuildFollowUp(in, nil, []Message{{Body: "What changed?"}}); !strings.Contains(msg, "1 reference file(s) omitted") {
		t.Fatalf("follow-up does not state the cut:\n%s", msg)
	}
	msg, _, left = BuildContinuation(ContinueInput{References: in.References, BudgetTokens: in.BudgetTokens})
	if left != 1 || !strings.Contains(msg, "1 reference file(s) omitted") {
		t.Fatalf("continuation omitted %d references:\n%s", left, msg)
	}
}
