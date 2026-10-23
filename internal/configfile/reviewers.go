package configfile

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/home-operations/kritika/internal/jobtimeout"
	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/review"
)

// Reviewer is a reviewer a review runs beside the default one, under the
// name the reviewers map keys it by: an agent of its own over the change,
// or the part of it Paths and When leave it, with instructions,
// categories, a model, rules and skills of its own. Under the name
// review.DefaultReviewer, Enabled alone may be written, which turns the
// default reviewer off; its model is review.model and its rules the
// configuration's.
type Reviewer struct {
	// Enabled false turns the reviewer off; unset is on.
	Enabled *bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	// Instructions are what the reviewer reviews for, added after
	// kritika's own; File, instead, names a repository file read from the
	// merge base whose content they are.
	Instructions string `yaml:"instructions,omitempty" json:"instructions,omitempty"`
	File         string `yaml:"file,omitempty" json:"file,omitempty"`
	// Categories are the only categories the reviewer may report, every
	// one when none is listed.
	Categories []review.Category `yaml:"categories,omitempty" json:"categories,omitempty"`
	// Model, Fallback and Effort are its review model, the one that takes
	// a step the model fails and how hard they reason, the repository's
	// review.model, review.fallback and review.effort when unset.
	Model    ModelRef      `yaml:"model,omitempty" json:"model,omitempty"`
	Fallback ModelRef      `yaml:"fallback,omitempty" json:"fallback,omitempty"`
	Effort   *model.Effort `yaml:"effort,omitempty" json:"effort,omitempty"`
	// Paths narrow the reviewer to the changed files one of them matches,
	// and When to a pull request one of those conditions holds for; a
	// pull request that changes none of them is not reviewed by it.
	Paths []string `yaml:"paths,omitempty" json:"paths,omitempty"`
	When  []When   `yaml:"when,omitempty" json:"when,omitempty"`
	// Rules are checks the reviewer makes, in place of the
	// configuration's, which are the default reviewer's.
	Rules []Rule `yaml:"rules,omitempty" json:"rules,omitempty"`
	// Skills lays scopes over the repository's for the reviewer's agent;
	// its paths may not be written, the repository's skills being one set.
	Skills SkillsSpec `yaml:"skills,omitempty" json:"skills,omitzero"`
	// Agent bounds the reviewer's agent within the repository's own
	// bounds, which it may lower and never raise.
	Agent ReviewerAgent `yaml:"agent,omitempty" json:"agent,omitzero"`
}

// ReviewerAgent bounds one reviewer's agent, or each part of it in a
// split review: its steps, its tokens and its time.
type ReviewerAgent struct {
	Steps   *int           `yaml:"steps,omitempty" json:"steps,omitempty"`
	Tokens  *int64         `yaml:"tokens,omitempty" json:"tokens,omitempty"`
	Timeout *time.Duration `yaml:"timeout,omitempty" json:"timeout,omitempty"`
}

// On reports whether the reviewer is on.
func (r Reviewer) On() bool { return r.Enabled == nil || *r.Enabled }

// Bounds are a, the repository's agent bounds, with r's laid over them
// where they are lower.
func (r Reviewer) Bounds(a AgentSettings) AgentSettings {
	if s := r.Agent.Steps; s != nil {
		a.MaxSteps = min(a.MaxSteps, *s)
	}
	if t := r.Agent.Tokens; t != nil {
		a.MaxTokens = min(a.MaxTokens, *t)
	}
	if t := r.Agent.Timeout; t != nil {
		a.Timeout = min(a.Timeout, *t)
	}
	return a
}

// MaxReviewerChars bounds a reviewer's instructions written as text.
const MaxReviewerChars = 8000

// MaxReviewers bounds how many reviewers a configuration, or a
// repository's file, writes beside the default one: each runs an agent of
// its own over the change.
const MaxReviewers = 8

// CheckReviewers rejects reviewers under a name that is not valid, the
// default reviewer with anything but enabled written, more than
// MaxReviewers others, or one whose instructions are both or overlong,
// whose file is outside the repository, whose categories are not
// categories, whose paths, conditions, rules, skills or bounds are not
// valid, or whose effort is not an effort. Its models are its caller's to
// check: what they may name depends on the scope. The error starts with
// the key, as reviewers.name.
func CheckReviewers(reviewers map[string]Reviewer) error {
	n := len(reviewers)
	if _, ok := reviewers[review.DefaultReviewer]; ok {
		n--
	}
	if n > MaxReviewers {
		return fmt.Errorf("reviewers: %d reviewers besides the default one are over the %d allowed", n, MaxReviewers)
	}
	for _, name := range slices.Sorted(maps.Keys(reviewers)) {
		if err := checkReviewer(name, reviewers[name]); err != nil {
			return fmt.Errorf("reviewers.%s%w", name, err)
		}
	}
	return nil
}

func checkReviewer(name string, r Reviewer) error {
	if !ruleIDRe.MatchString(name) {
		return errors.New(": a name must be lowercase letters, digits and hyphens, starting with a letter or digit, at most 64 characters")
	}
	if name == review.DefaultReviewer {
		if !r.onlyEnabled() {
			return errors.New(": only enabled may be set; the default reviewer is review.model's, with the configuration's rules")
		}
		return nil
	}
	if strings.TrimSpace(r.Instructions) != "" && r.File != "" {
		return errors.New(": set one of instructions or file")
	}
	if n := utf8.RuneCountInString(r.Instructions); n > MaxReviewerChars {
		return fmt.Errorf(".instructions is %d characters, over the %d allowed", n, MaxReviewerChars)
	}
	if r.File != "" {
		if err := CheckRepoPath(r.File); err != nil {
			return fmt.Errorf(".file: %w", err)
		}
	}
	for i, c := range r.Categories {
		if !c.Valid() {
			return fmt.Errorf(".categories[%d] %q is not one of %s", i, c, categoryNames())
		}
		if slices.Contains(r.Categories[:i], c) {
			return fmt.Errorf(".categories[%d] %q is listed twice", i, c)
		}
	}
	if r.Effort != nil && *r.Effort != "" && !r.Effort.Valid() {
		return fmt.Errorf(".effort must be %s, got %q", model.EffortLevels, *r.Effort)
	}
	for i, g := range r.Paths {
		if !ValidGlob(g) {
			return fmt.Errorf(".paths[%d] %q is not a valid glob", i, g)
		}
	}
	if err := CheckWhen(r.When); err != nil {
		return fmt.Errorf(".%w", err)
	}
	if err := CheckRules(r.Rules); err != nil {
		return fmt.Errorf(".%w", err)
	}
	if r.Skills.Paths != nil {
		return errors.New(".skills.paths: a reviewer takes skill scopes alone; the repository's skills.paths says where skills are")
	}
	if err := CheckSkills(nil, r.Skills.Scope); err != nil {
		return fmt.Errorf(".%w", err)
	}
	return r.Agent.check()
}

func (a ReviewerAgent) check() error {
	if a.Steps != nil && *a.Steps <= 0 {
		return errors.New(".agent.steps must be positive")
	}
	if a.Tokens != nil && *a.Tokens <= 0 {
		return errors.New(".agent.tokens must be positive")
	}
	if a.Timeout != nil && (*a.Timeout <= 0 || *a.Timeout > jobtimeout.MaxAgentTimeout) {
		return fmt.Errorf(".agent.timeout must be positive and at most %s", jobtimeout.MaxAgentTimeout)
	}
	return nil
}

// onlyEnabled reports whether r writes no key but enabled.
func (r Reviewer) onlyEnabled() bool {
	return r.Instructions == "" && r.File == "" && len(r.Categories) == 0 && r.Model == "" && r.Fallback == "" &&
		r.Effort == nil && len(r.Paths) == 0 && len(r.When) == 0 && len(r.Rules) == 0 && r.Skills.Paths == nil &&
		len(r.Skills.Scope) == 0 && r.Agent == (ReviewerAgent{})
}

// categoryNames lists the categories for an error.
func categoryNames() string {
	names := make([]string, 0, len(review.Categories()))
	for _, c := range review.Categories() {
		names = append(names, string(c))
	}
	return strings.Join(names, ", ")
}

// ReviewerFiles lists the repository paths the reviewers name whose contents
// a review reads: each one's instructions file, then its rules' files, in
// the order of their names.
func ReviewerFiles(reviewers map[string]Reviewer) []string {
	var out []string
	for _, name := range slices.Sorted(maps.Keys(reviewers)) {
		r := reviewers[name]
		for _, p := range append([]string{r.File}, ruleFiles(r.Rules)...) {
			if p != "" && !slices.Contains(out, p) {
				out = append(out, p)
			}
		}
	}
	return out
}

// ruleFiles is the files of rules, "" for each written as text.
func ruleFiles(rules []Rule) []string {
	out := make([]string, len(rules))
	for i, r := range rules {
		out[i] = r.File
	}
	return out
}

// DefaultOn reports whether reviewers leaves the default reviewer on.
func DefaultOn(reviewers map[string]Reviewer) bool {
	r, ok := reviewers[review.DefaultReviewer]
	return !ok || r.On()
}

// validateReviewers checks the file's reviewers, as validateOverrides
// checks its models.
func (f *File) validateReviewers(reviewers map[string]Reviewer) error {
	if err := CheckReviewers(reviewers); err != nil {
		return fmt.Errorf("configfile: %w", err)
	}
	for _, name := range slices.Sorted(maps.Keys(reviewers)) {
		r := reviewers[name]
		for _, m := range []struct {
			key string
			ref ModelRef
		}{{"model", r.Model}, {"fallback", r.Fallback}} {
			if m.ref == "" {
				continue
			}
			if err := f.checkModelRef("reviewers."+name+"."+m.key, m.ref); err != nil {
				return err
			}
		}
	}
	return nil
}
