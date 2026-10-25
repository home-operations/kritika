package review

// Summary is the review's overall judgement for the sticky comment.
type Summary struct {
	// Headline is one sentence on what the change does, the summary
	// section's first line.
	Headline string   `json:"headline,omitempty"`
	Take     string   `json:"take"`
	Praise   []string `json:"praise"`
	// Diagram is Mermaid source for the flow the change adds or alters,
	// without fences; "" when the change has no flow worth drawing or the
	// model's diagram was not one Parse keeps.
	Diagram string `json:"diagram,omitempty"`
	// Checked are the review's notes for the next review of the pull
	// request on what it checked and found sound, one line each; no
	// comment shows them.
	Checked []string `json:"checked,omitempty"`
}
