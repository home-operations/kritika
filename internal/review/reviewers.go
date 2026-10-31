package review

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// DefaultReviewer is the reviewer every review has unless it is turned
// off: the one review.model reviews with, under kritika's instructions
// alone.
const DefaultReviewer = "default"

// RaisedBy reports whether reviewer raised f, a finding with no reviewers
// being the default reviewer's.
func (f Finding) RaisedBy(reviewer string) bool {
	if len(f.Agents) == 0 {
		return reviewer == DefaultReviewer
	}
	return slices.Contains(f.Agents, reviewer)
}

// Specialist is the first reviewer other than DefaultReviewer that raised
// f, "" when only the default reviewer did.
func (f Finding) Specialist() string {
	for _, r := range f.Agents {
		if r != DefaultReviewer {
			return r
		}
	}
	return ""
}

// AgentNames names the reviewers that raised f, as comments credit them,
// or "" when the default reviewer alone did.
func (f Finding) AgentNames() string {
	if f.Specialist() == "" {
		return ""
	}
	return strings.Join(f.Agents, ", ")
}

// reviewers is f's reviewers that opts names, once each, nil when none
// is, which is the default reviewer's.
func (opts ParseOptions) reviewers(f Finding) []string {
	var out []string
	for _, r := range f.Agents {
		if slices.Contains(opts.Reviewers, r) && !slices.Contains(out, r) {
			out = append(out, r)
		}
	}
	if len(out) == 1 && out[0] == DefaultReviewer {
		return nil
	}
	return out
}

// allows reports whether one of f's reviewers can report its category.
func (opts ParseOptions) allows(f Finding) bool {
	reviewers := f.Agents
	if len(reviewers) == 0 {
		reviewers = []string{DefaultReviewer}
	}
	return slices.ContainsFunc(reviewers, func(r string) bool {
		allowed, scoped := opts.Categories[r]
		return !scoped || slices.Contains(allowed, f.Category)
	})
}

// SchemaFor is the JSON Schema of the answer the model must produce:
// suggested_fix required on every finding when strict, summary.diagram
// when diagram is set, and findings of categories alone, every category
// when it names none.
func SchemaFor(strict, diagram bool, categories []Category) json.RawMessage {
	if len(categories) == 0 {
		return slices.Clone(contracts[contract{strict, diagram}])
	}
	s := contractSchema(strict, diagram)
	kinds := make([]string, len(categories))
	for i, c := range categories {
		kinds[i] = string(c)
	}
	s.Properties[keyFindings].Items.Properties[keyCategory].Enum = kinds
	return s.mustMarshal()
}

// CheckCategories is check, which also refuses a finding in a category
// outside categories. So a reviewer that can report only some corrects one
// that it strayed into. Every category passes when categories is empty.
func CheckCategories(check func(json.RawMessage) error, categories []Category) func(json.RawMessage) error {
	if len(categories) == 0 {
		return check
	}
	return func(raw json.RawMessage) error {
		if err := check(raw); err != nil {
			return err
		}
		var res Result
		// check decoded the same input.
		_ = json.Unmarshal(raw, &res)
		for _, f := range res.Findings {
			if !slices.Contains(categories, f.Category) {
				return fmt.Errorf("review: %s:%d is a %s finding; you report only %s findings, so leave it to the other reviewers",
					strings.TrimSpace(f.Path), f.Line, f.Category, joinCategories(categories))
			}
		}
		return nil
	}
}

// joinCategories names categories for a prompt or an error.
func joinCategories(categories []Category) string {
	names := make([]string, len(categories))
	for i, c := range categories {
		names[i] = string(c)
	}
	return strings.Join(names, ", ")
}

// Reviewer is one reviewer of a review with several, as its system prompt
// gives it: its name, the instructions it reviews by, and the categories it
// can report, every category when none.
type Reviewer struct {
	Name, Instructions string
	Categories         []Category
}

// reviewerRole follows the rules for a reviewer other than the default one.
// %s is its name.
const reviewerRole = `

## Your role

This pull request is reviewed by several reviewers, each an agent of its own, and you are the %s reviewer. The others
review it as well, so keep to what this role asks of you.`

// reviewerCategories follows reviewerRole for a reviewer that can report only
// some categories. %s names them.
const reviewerCategories = ` Report only %s findings, whatever else you notice: the other reviewers cover the rest, and
submit_review refuses a finding in another category.`

// WithReviewer is system, a review's or a follow-up's system prompt, with
// what the reviewer r is given beyond it appended: its role, the categories
// it can report, and its instructions, the maintainers' own. The default
// reviewer, or none, is given nothing.
func WithReviewer(system string, r *Reviewer) string {
	if r == nil || r.Name == DefaultReviewer {
		return system
	}
	system += fmt.Sprintf(reviewerRole, r.Name)
	if len(r.Categories) > 0 {
		system += fmt.Sprintf(reviewerCategories, joinCategories(r.Categories))
	}
	if text := strings.TrimSpace(r.Instructions); text != "" {
		system += "\n\nThe maintainers' instructions for this role:\n\n" + text
	}
	return system
}

// MergeReviewers merges, in place of the first, each pair of findings that
// different reviewers raised about the same thing: on one path, over lines
// that overlap, in one category or under one title. The more severe finding
// is kept, the first when they are as severe. The kept finding credits the
// other's reviewers after its own. It keeps the other's fingerprint when the
// other carried a prior finding's and the kept one did not. So the thread
// that an earlier review opened goes on. Findings that one reviewer raised
// are never merged with each other: it meant them apart.
func MergeReviewers(findings []Finding) []Finding {
	out := make([]Finding, 0, len(findings))
	for _, f := range findings {
		i := slices.IndexFunc(out, func(k Finding) bool { return sameIssue(k, f) })
		if i < 0 {
			out = append(out, f)
			continue
		}
		out[i] = mergeFinding(out[i], f)
	}
	return out
}

// mergeReviewers is MergeReviewers over findings that Parse kept. Each
// carries the fingerprint of the prior finding that it reports again, its own
// title's included, so a merge under another title keeps that thread.
// Afterwards, a finding carries one only where it differs from its title's,
// as Parse leaves them.
func mergeReviewers(findings []Finding) []Finding {
	out := MergeReviewers(findings)
	for i := range out {
		if out[i].Fingerprint == titleFingerprint(out[i]) {
			out[i].Fingerprint = ""
		}
	}
	return out
}

// mergeDropped is dropped with its findings off the diff merged as
// mergeReviewers merges the kept ones, which a review lists apart. The others
// stay as they are, and every one's fingerprint is left as Parse leaves them.
func mergeDropped(dropped []Dropped) []Dropped {
	var off []Finding
	out := make([]Dropped, 0, len(dropped))
	for _, d := range dropped {
		if d.Reason == DropUnanchored {
			off = append(off, d.Finding)
			continue
		}
		if d.Finding.Fingerprint == titleFingerprint(d.Finding) {
			d.Finding.Fingerprint = ""
		}
		out = append(out, d)
	}
	for _, f := range mergeReviewers(off) {
		out = append(out, Dropped{Finding: f, Reason: DropUnanchored})
	}
	return out
}

// sameIssue reports whether a and b, raised by no reviewer in common, are
// about the same thing.
func sameIssue(a, b Finding) bool {
	if a.Path != b.Path || slices.ContainsFunc(raisedBy(a), func(r string) bool { return slices.Contains(raisedBy(b), r) }) {
		return false
	}
	if max(a.Line, a.EndLine) < b.Line || max(b.Line, b.EndLine) < a.Line {
		return false
	}
	return a.Category == b.Category || titleFingerprint(a) == titleFingerprint(b)
}

// mergeFinding is kept and other as one finding (see MergeReviewers).
func mergeFinding(kept, other Finding) Finding {
	if other.Severity.Rank() < kept.Severity.Rank() {
		kept, other = other, kept
	}
	reviewers := raisedBy(kept)
	for _, r := range raisedBy(other) {
		if !slices.Contains(reviewers, r) {
			reviewers = append(reviewers, r)
		}
	}
	kept.Agents = reviewers
	if kept.Fingerprint == "" && other.Fingerprint != "" {
		kept.Fingerprint = other.Fingerprint
	}
	return kept
}

// raisedBy is f's reviewers, DefaultReviewer for a finding that names
// none.
func raisedBy(f Finding) []string {
	if len(f.Agents) == 0 {
		return []string{DefaultReviewer}
	}
	return slices.Clone(f.Agents)
}
