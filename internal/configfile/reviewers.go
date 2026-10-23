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
	// Model, Fallback and Effort are its review model, as review.model
	// writes one, a model or routes ending with one, the one that takes a
	// step the model fails and how hard they reason; unset, the model
	// review.model picks for the pull request, with its fallback and
	// effort.
	Model    *ReviewModel  `yaml:"model,omitempty" json:"model,omitempty"`
	Fallback ModelRef      `yaml:"fallback,omitempty" json:"fallback,omitempty"`
	Effort   *model.Effort `yaml:"effort,omitempty" json:"effort,omitempty"`
	// Paths are the changed files the reviewer reads, every one when
	// none: a pull request that changes none of them is not reviewed by
	// it. When are conditions as a model route's, judged over the whole
	// pull request, one of which must hold for the reviewer to run.
	Paths []string `yaml:"paths,omitempty" json:"paths,omitempty"`
	When  []Filter `yaml:"when,omitempty" json:"when,omitempty"`
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

// Holds reports whether r's when conditions let it run on a pull request
// with the filter variables vars and the diff d, nil when the diff could
// not be read: one of them holds, or there are none. A condition is
// judged as a model route's is (see Models.Pick), and the error of one
// that fails to evaluate is returned for the log.
func (r Reviewer) Holds(vars map[string]any, d *Diff) (bool, error) {
	if len(r.When) == 0 {
		return true, nil
	}
	if d != nil {
		vars = maps.Clone(vars)
		vars[linesVar] = d.Lines
	}
	_, ok, err := firstHolding(r.When, vars, d)
	return ok, err
}

// NeedsDiff reports whether one of r's conditions, or of its model's
// routes, cannot be judged before the pull request's diff is read.
func (r Reviewer) NeedsDiff() bool {
	if slices.ContainsFunc(r.When, Filter.needsDiff) {
		return true
	}
	return r.Model != nil && (Models{Routes: r.Model.Routes}).NeedsDiff()
}

// Models are r's models, from which Models.Pick picks a pull request's:
// its own, or defaults where it writes no model, which are the default
// reviewer's as review.model picked them; its fallback and effort replace
// theirs where it writes them.
func (r Reviewer) Models(defaults Models) Models {
	out := defaults
	out.Routes = nil
	if r.Model != nil {
		out.Review, out.Routes, out.Route = r.Model.Default, r.Model.Routes, ""
	}
	if r.Fallback != "" {
		out.Fallback = r.Fallback
	}
	if r.Effort != nil {
		out.Effort = *r.Effort
	}
	return out
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
// valid, or whose effort is not an effort, and compiles their when
// conditions and model routes in place. Its models are its caller's to
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
	for i := range r.When {
		w := &r.When[i]
		if w.Name != "" && slices.ContainsFunc(r.When[:i], func(o Filter) bool { return o.Name == w.Name }) {
			return fmt.Errorf(".when[%d]: name %q is given twice", i, w.Name)
		}
		if err := w.Compile(); err != nil {
			return fmt.Errorf(".when[%d]: %w", i, err)
		}
	}
	if err := r.Model.checkAt(".model"); err != nil {
		return err
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
	// The job document carries whole seconds.
	if a.Timeout != nil && (*a.Timeout < time.Second || *a.Timeout > jobtimeout.MaxAgentTimeout) {
		return fmt.Errorf(".agent.timeout must be at least 1s and at most %s", jobtimeout.MaxAgentTimeout)
	}
	return nil
}

// onlyEnabled reports whether r writes no key but enabled.
func (r Reviewer) onlyEnabled() bool {
	return r.Instructions == "" && r.File == "" && len(r.Categories) == 0 && r.Model == nil && r.Fallback == "" &&
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
		refs := append(r.Model.refsAt("reviewers."+name+".model"), namedRef{"reviewers." + name + ".fallback", &r.Fallback})
		for _, m := range refs {
			if m.ref == nil || *m.ref == "" {
				continue
			}
			if err := f.checkModelRef(m.key, *m.ref); err != nil {
				return err
			}
		}
	}
	return nil
}
