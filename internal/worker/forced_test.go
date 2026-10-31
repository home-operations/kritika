package worker

import (
	"slices"
	"testing"

	"github.com/home-operations/kritika/internal/store"
)

// TestCutShort: a review whose agent the loop told to submit says so, by the
// limit that made it. Each part of a split review that submitted so does too.
// A part that never submitted is unfinishedParts' to name.
func TestCutShort(t *testing.T) {
	tests := []struct {
		name string
		run  store.AgentRunRow
		want []string
	}{
		{"an agent that submitted on its own", store.AgentRunRow{}, nil},
		{"the step limit", store.AgentRunRow{ForcedBy: "steps"},
			[]string{"The review's agent was told to submit what it had found on the last step it may take, so it may not cover all it meant to check"}},
		{"the time limit", store.AgentRunRow{ForcedBy: "time"},
			[]string{"The review's agent was told to submit what it had found as its time ran out, so it may not cover all it meant to check"}},
		{"the parts of a split review", store.AgentRunRow{Parts: []store.AgentPart{
			{Stop: "submitted"},
			{Stop: "submitted", ForcedBy: "tokens", Reviewer: "security"},
			{Stop: "no_submit", ForcedBy: "steps"},
			{Stop: "submitted", ForcedBy: "time"},
		}}, []string{
			"Part 2 of 4, the security agent's, was told to submit what it had found with 90% of its tokens spent, so it may not cover all it meant to check",
			"Part 4 of 4 was told to submit what it had found as its time ran out, so it may not cover all it meant to check",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cutShort(tt.run); !slices.Equal(got, tt.want) {
				t.Fatalf("notes = %q, want %q", got, tt.want)
			}
		})
	}
}
