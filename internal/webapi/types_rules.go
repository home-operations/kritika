package webapi

import "github.com/home-operations/kritika/internal/configfile"

// The shapes of the rules in force, which routes_rules.go serves.

// RuleKind is what a rule is to a review: a check written in the
// configuration, as text or a file, or a context file that explains the
// code.
type RuleKind string

// Rule kinds.
const (
	RuleWritten RuleKind = "rule"
	RuleContext RuleKind = "context"
)

// RuleSource is where a rule is set: a layer of the configuration, as
// configfile.Source names it, RuleFromEntry, an account's entry for the
// repository, or RuleFromRepository, the repository's own .kritika.yaml.
type RuleSource string

// Rule sources beyond the configuration's layers.
const (
	RuleFromRepository RuleSource = "repository"
	RuleFromEntry      RuleSource = "entry"
)

// Rule is one written rule or file reviews read, with where it is set,
// the paths it applies to (every change when empty), and the running
// repositories whose reviews read it. ID is a written rule's, with its
// Text or, for a file rule, its Path, and WhenExpr, the CEL expression
// over the pull request it applies only when true of; Path and
// Description are a context file's. Findings and Addressed are a written
// rule's too: its repositories' findings that cite its id, once per pull
// request as the findings list counts them, and how many of those were
// addressed.
type Rule struct {
	Kind         RuleKind          `json:"kind"`
	ID           string            `json:"id"`
	Text         string            `json:"text"`
	Path         string            `json:"path"`
	Description  string            `json:"description"`
	Paths        []string          `json:"paths"`
	When         []configfile.When `json:"when"`
	Source       RuleSource        `json:"source"`
	Repositories []string          `json:"repositories"`
	Findings     int               `json:"findings"`
	Addressed    int               `json:"addressed"`
}
