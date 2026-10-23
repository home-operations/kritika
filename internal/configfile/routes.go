package configfile

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"go.yaml.in/yaml/v4"

	"github.com/home-operations/kritika/internal/model"
)

// ReviewModel is review.model as a scope writes it: the "<provider>/<model>"
// the default pass reviews on. The agents of review.agents that review
// every category pick another for a pull request (see Models.Routes).
type ReviewModel struct {
	Default ModelRef
}

// ModelRoute is an agent that replaces the default pass, as the review's
// model routes it: the model a pull request one of When holds for is
// reviewed on, none holding always, with the effort and fallback that
// replace review.effort and review.fallback for it where it sets them.
// When takes trigger conditions, judged over the whole pull request
// before its review starts.
type ModelRoute struct {
	Name     string        `json:"name,omitempty"`
	Model    ModelRef      `json:"model"`
	Effort   *model.Effort `json:"effort,omitempty"`
	Fallback *ModelRef     `json:"fallback,omitempty"`
	When     []Filter      `json:"when"`
}

// UnmarshalYAML takes a model. A list, the routes review.model once took,
// is refused for the agents that replaced them.
func (m *ReviewModel) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.SequenceNode {
		return errors.New("review.model takes one model; pick another for some pull requests with an agent of review.agents " +
			"that has no categories, which replaces the default pass when its conditions hold")
	}
	return n.Decode(&m.Default)
}

// namedRef is a model setting by the key it is written under.
type namedRef struct {
	key string
	ref *ModelRef
}

// refs are the models m names with the keys they are written under; nil m
// names none.
func (m *ReviewModel) refs() []namedRef {
	if m == nil {
		return nil
	}
	return []namedRef{{keyModel, &m.Default}}
}

// String is the value as the Configuration page states it.
func (m ReviewModel) String() string { return string(m.Default) }

// NeedsDiff reports whether one of the routes' conditions, compiled, cannot
// be judged before the pull request's diff is read.
func (m Models) NeedsDiff() bool {
	return slices.ContainsFunc(m.Routes, func(r ModelRoute) bool { return slices.ContainsFunc(r.When, Filter.needsDiff) })
}

// Pick is m as a pull request with the filter variables vars and the diff
// d is reviewed on: the first route one of whose conditions holds replaces
// the review model, and the effort and fallback where it sets them, and
// Route names it; with none, m stands. Without d, nil when the diff could
// not be read, a condition that needs it does not hold; one that fails to
// evaluate does not hold either, and its error is returned for the log.
func (m Models) Pick(vars map[string]any, d *Diff) (Models, error) {
	if d != nil {
		vars = maps.Clone(vars)
		vars[linesVar] = d.Lines
	}
	var errs []error
	for _, r := range m.Routes {
		c, ok, err := firstHolding(r.When, vars, d)
		if err != nil {
			errs = append(errs, err)
		}
		if len(r.When) == 0 {
			// An agent with no conditions always replaces the default pass.
			ok = true
		}
		if !ok {
			continue
		}
		m.Review, m.Route = r.Model, routeLabel(r, c)
		if r.Effort != nil {
			m.Effort = *r.Effort
		}
		if r.Fallback != nil {
			m.Fallback = *r.Fallback
		}
		return m, errors.Join(errs...)
	}
	return m, errors.Join(errs...)
}

// firstHolding is the first of conds that holds for a pull request with
// the filter variables vars and the diff d, as Pick judges them: without
// d, one that needs it does not hold, and one that fails to evaluate does
// not hold either, its error returned for the log.
func firstHolding(conds []Filter, vars map[string]any, d *Diff) (Filter, bool, error) {
	var errs []error
	for _, c := range conds {
		// Settings read back from JSON carry no compiled program, and a
		// condition without one would hold on its globs alone.
		if c.prg == nil && strings.TrimSpace(c.Expr) != "" {
			if err := c.Compile(); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", c.Label(), err))
				continue
			}
		}
		if d == nil && c.needsDiff() {
			continue
		}
		ok, err := c.holds(vars, d)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", c.Label(), err))
			continue
		}
		if ok {
			return c, true, errors.Join(errs...)
		}
	}
	return Filter{}, false, errors.Join(errs...)
}

// routeLabel names route r and the condition c that picked it, as
// "deep (migrations)", the condition alone for a route with no name, or
// the name alone for one with no conditions.
func routeLabel(r ModelRoute, c Filter) string {
	switch label := c.Label(); {
	case r.Name == "":
		return label
	case label == "":
		return r.Name
	default:
		return r.Name + " (" + label + ")"
	}
}
