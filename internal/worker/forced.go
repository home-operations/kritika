package worker

import (
	"fmt"

	"github.com/home-operations/kritika/internal/agent"
	"github.com/home-operations/kritika/internal/store"
)

// whenForced is when the loop told an agent to submit, as a note states
// it, by the limit that made it.
var whenForced = map[string]string{
	string(agent.ForcedBySteps):  "on the last step it may take",
	string(agent.ForcedByTokens): "with 90% of its tokens spent",
	string(agent.ForcedByTime):   "as its time ran out",
}

// cutShort is what the summary states when the loop told the review's agent
// to submit what it found before it chose to. In a split review, it covers
// each part's agent that submitted so. The review can miss what it did not
// read yet.
func cutShort(run store.AgentRunRow) []string {
	const missed = "so it may not cover all it meant to check"
	if len(run.Parts) == 0 {
		if when, ok := whenForced[run.ForcedBy]; ok {
			return []string{fmt.Sprintf("The review's agent was told to submit what it had found %s, %s", when, missed)}
		}
		return nil
	}
	var notes []string
	for i, part := range run.Parts {
		when, ok := whenForced[part.ForcedBy]
		if !ok || part.Stop != string(agent.StopSubmitted) {
			continue
		}
		who := fmt.Sprintf("Part %d of %d", i+1, len(run.Parts))
		if part.Reviewer != "" {
			who += fmt.Sprintf(", the %s agent's,", part.Reviewer)
		}
		notes = append(notes, fmt.Sprintf("%s was told to submit what it had found %s, %s", who, when, missed))
	}
	return notes
}
