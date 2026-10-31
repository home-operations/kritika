package configfile

import (
	"cmp"
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

// Reviewer is an agent of review.agents, by its Name. One with Categories
// is a specialist: an agent of its own that reviews the change, or the
// part of it that Paths leave it, for those categories alone. It runs beside
// the other agents, with instructions, a model, rules, and skills of its own.
//
// One without Categories replaces the default pass, review.model's, when its
// When hold. The first such agent that holds does this, with its model,
// fallback, and effort, as a route of the review's model (see Route). It
// takes nothing else.
type Reviewer struct {
	// Name names the agent to comments, commands and the dashboard.
	Name string `yaml:"name" json:"name"`
	// Enabled false turns the agent off. Unset is on.
	Enabled *bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	// Instructions are what the agent reviews for, added after kritika's
	// own. File, instead, names a repository file read from the merge base
	// whose content they are.
	Instructions string `yaml:"instructions,omitempty" json:"instructions,omitempty"`
	File         string `yaml:"file,omitempty" json:"file,omitempty"`
	// Categories are the only categories the agent can report. Written,
	// they make it a specialist.
	Categories []review.Category `yaml:"categories,omitempty" json:"categories,omitempty"`
	// Model, Fallback, and Effort are its model, the one that takes a step
	// the model fails, and how hard they reason. A specialist's unset are
	// the default pass's, as the review's agents picked it.
	Model    ModelRef      `yaml:"model,omitempty" json:"model,omitempty"`
	Fallback ModelRef      `yaml:"fallback,omitempty" json:"fallback,omitempty"`
	Effort   *model.Effort `yaml:"effort,omitempty" json:"effort,omitempty"`
	// Paths are the changed files a specialist reads, every one when none:
	// a pull request that changes none of them is not reviewed by it. When
	// are conditions as a trigger's, judged over the whole pull request,
	// one of which must hold for the agent to run. An agent with none always
	// holds.
	Paths []string `yaml:"paths,omitempty" json:"paths,omitempty"`
	When  []Filter `yaml:"when,omitempty" json:"when,omitempty"`
	// Rules are checks a specialist makes, in place of the configuration's,
	// which are the default pass's.
	Rules []Rule `yaml:"rules,omitempty" json:"rules,omitempty"`
	// Skills lays scopes over the repository's for a specialist. Its paths
	// cannot be written, because the repository's skills are one set.
	Skills SkillsSpec `yaml:"skills,omitempty" json:"skills,omitzero"`
	// Agent bounds a specialist within the repository's own bounds, which
	// it can lower and never raise.
	Agent ReviewerAgent `yaml:"agent,omitempty" json:"agent,omitzero"`
}

// ReviewerAgent bounds one specialist's agent, or each part of it in a
// split review: its steps, its tokens and its time.
type ReviewerAgent struct {
	Steps   *int           `yaml:"steps,omitempty" json:"steps,omitempty"`
	Tokens  *int64         `yaml:"tokens,omitempty" json:"tokens,omitempty"`
	Timeout *time.Duration `yaml:"timeout,omitempty" json:"timeout,omitempty"`
}

// On reports whether the agent is on.
func (r Reviewer) On() bool { return r.Enabled == nil || *r.Enabled }

// Specialist reports whether the agent reviews some categories alone,
// beside the others, rather than replacing the default pass.
func (r Reviewer) Specialist() bool { return len(r.Categories) > 0 }

// Route is an agent that replaces the default pass as a route of the
// review's model: its model, effort and fallback, and its conditions,
// none of which holding always.
func (r Reviewer) Route() ModelRoute {
	route := ModelRoute{Name: r.Name, Model: r.Model, Effort: r.Effort, When: r.When}
	if r.Fallback != "" {
		route.Fallback = new(r.Fallback)
	}
	return route
}

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
// with the filter variables vars and the diff d. d is nil when the read of
// the diff failed. The conditions let it run when one of them holds, or there
// are none. A condition is judged as a model route's is (see Models.Pick),
// and the error of one that fails to evaluate is returned for the log.
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

// NeedsDiff reports whether one of r's conditions cannot be judged before
// the pull request's diff is read.
func (r Reviewer) NeedsDiff() bool { return slices.ContainsFunc(r.When, Filter.needsDiff) }

// Models are a specialist's models: its own where it writes them, and
// otherwise defaults', the default pass's as the review's agents picked
// them, with the route that picked them.
func (r Reviewer) Models(defaults Models) Models {
	out := defaults
	out.Routes = nil
	if r.Model != "" {
		out.Review, out.Route = r.Model, ""
	}
	out.Fallback = cmp.Or(r.Fallback, out.Fallback)
	if r.Effort != nil {
		out.Effort = *r.Effort
	}
	return out
}

// MaxReviewerChars bounds an agent's instructions written as text.
const MaxReviewerChars = 8000

// MaxReviewers bounds how many agents a configuration, or a repository's
// file, writes: each runs an agent of its own over the change, or picks
// the one the default pass runs.
const MaxReviewers = 8

// CheckAgents rejects agents with a name that is not valid, is the default
// pass's, or is given twice. It rejects more than MaxReviewers. It rejects a
// specialist whose instructions are both or overlong, or whose file is
// outside the repository. It also rejects a specialist whose categories are
// not categories, or whose paths, rules, skills, or bounds are not valid. It
// rejects an agent that replaces the default pass with no model or with
// anything but its name, model, fallback, effort, when, and enabled. It also
// rejects any agent whose conditions or effort are not valid.
//
// CheckAgents compiles their conditions in place. The caller makes sure that
// their models are valid, because what they can name depends on the scope.
// The error starts with the agent's place, as review.agents[0] (security).
func CheckAgents(agents []Reviewer) error {
	if len(agents) > MaxReviewers {
		return fmt.Errorf("review.agents: %d agents are over the %d allowed", len(agents), MaxReviewers)
	}
	for i := range agents {
		r := &agents[i]
		where := fmt.Sprintf("review.agents[%d] (%s)", i, r.Name)
		switch {
		case !ruleIDRe.MatchString(r.Name):
			return fmt.Errorf("%s: name must be lowercase letters, digits and hyphens, starting with a letter or digit, "+
				"at most 64 characters", where)
		case r.Name == review.DefaultReviewer:
			return fmt.Errorf("%s: %q is the default pass, review.model's; an agent without categories replaces it", where, r.Name)
		case slices.ContainsFunc(agents[:i], func(o Reviewer) bool { return o.Name == r.Name }):
			return fmt.Errorf("%s: name is given twice", where)
		}
		if err := checkAgent(r); err != nil {
			return fmt.Errorf("%s%w", where, err)
		}
	}
	return nil
}

func checkAgent(r *Reviewer) error {
	if r.Effort != nil && *r.Effort != "" && !r.Effort.Valid() {
		return fmt.Errorf(".effort must be %s, got %q", model.EffortLevels, *r.Effort)
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
	if !r.Specialist() {
		return checkReplacing(*r)
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
	for i, g := range r.Paths {
		if !ValidGlob(g) {
			return fmt.Errorf(".paths[%d] %q is not a valid glob", i, g)
		}
	}
	if err := CheckRules(r.Rules); err != nil {
		return fmt.Errorf(".%w", err)
	}
	if r.Skills.Paths != nil {
		return errors.New(".skills.paths: an agent takes skill scopes alone; the repository's skills.paths says where skills are")
	}
	if err := CheckSkills(nil, r.Skills.Scope); err != nil {
		return fmt.Errorf(".%w", err)
	}
	return r.Agent.check()
}

// checkReplacing rejects an agent that replaces the default pass with no
// model, or with a key of a specialist's.
func checkReplacing(r Reviewer) error {
	if r.Model == "" {
		return errors.New(": an agent without categories replaces the default pass and needs a model")
	}
	r.Name, r.Enabled, r.Model, r.Fallback, r.Effort, r.When = "", nil, "", "", nil, nil
	if r.Instructions != "" || r.File != "" || len(r.Paths) > 0 || len(r.Rules) > 0 || r.Skills.Paths != nil ||
		len(r.Skills.Scope) > 0 || r.Agent != (ReviewerAgent{}) {
		return errors.New(": an agent without categories replaces the default pass, and takes only name, enabled, model, " +
			"fallback, effort and when; give it categories to review beside it")
	}
	return nil
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

// categoryNames lists the categories for an error.
func categoryNames() string {
	names := make([]string, 0, len(review.Categories()))
	for _, c := range review.Categories() {
		names = append(names, string(c))
	}
	return strings.Join(names, ", ")
}

// SplitAgents is agents as a review runs them: its specialists by name,
// and the routes of those on that replace the default pass, in order.
func SplitAgents(agents []Reviewer) (map[string]Reviewer, []ModelRoute) {
	var specialists map[string]Reviewer
	var routes []ModelRoute
	for _, r := range agents {
		switch {
		case r.Specialist():
			if specialists == nil {
				specialists = map[string]Reviewer{}
			}
			specialists[r.Name] = r
		case r.On():
			routes = append(routes, r.Route())
		}
	}
	return specialists, routes
}

// ReviewerFiles lists the repository paths the specialists name whose
// contents a review reads: each one's instructions file, then its rules'
// files, in the order of their names.
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

// validateAgents makes sure that the file's agents are valid, as
// validateOverrides does for its models.
func (f *File) validateAgents(agents []Reviewer) error {
	if err := CheckAgents(agents); err != nil {
		return fmt.Errorf("configfile: %w", err)
	}
	for i, r := range agents {
		for _, m := range []namedRef{{"model", &r.Model}, {"fallback", &r.Fallback}} {
			if *m.ref == "" {
				continue
			}
			if err := f.checkModelRef(fmt.Sprintf("review.agents[%d].%s", i, m.key), *m.ref); err != nil {
				return err
			}
		}
	}
	return nil
}
