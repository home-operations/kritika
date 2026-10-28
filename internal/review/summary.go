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

// normalizeSummary applies the contract to a summary's text: one-line
// headline and checked notes, prose with its references redirected,
// praise and notes to their caps, and the diagram only where opts ask for
// one and it is one diagram allows.
func normalizeSummary(s Summary, opts ParseOptions) Summary {
	s.Headline = oneLine(prose(s.Headline, opts.Repository))
	s.Take = prose(s.Take, opts.Repository)
	praise := make([]string, 0, maxPraise)
	for _, p := range s.Praise {
		if p = prose(p, opts.Repository); p != "" && len(praise) < maxPraise {
			praise = append(praise, p)
		}
	}
	s.Praise = praise
	var checked []string
	for _, c := range s.Checked {
		if c = oneLine(c); c != "" && len(checked) < maxChecked {
			checked = append(checked, c)
		}
	}
	s.Checked = checked
	if opts.Diagram {
		s.Diagram = diagram(s.Diagram)
	} else {
		s.Diagram = ""
	}
	return s
}
