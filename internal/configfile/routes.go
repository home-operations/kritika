package configfile

import (
	"cmp"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"go.yaml.in/yaml/v4"

	"github.com/home-operations/kritika/internal/model"
)

// ReviewModel is review.model as a scope writes it: the "<provider>/<model>"
// every pull request is reviewed on, or a list whose last entry is that
// model and whose others are routes, the first one of whose conditions
// holds for a pull request picking its model instead.
type ReviewModel struct {
	Default ModelRef
	Routes  []ModelRoute
}

// ModelRoute is an entry of review.model's list but the last: the model a
// pull request one of When holds for is reviewed on, with the effort and
// fallback that replace review.effort and review.fallback for it where it
// sets them. When takes trigger conditions, judged over the whole pull
// request before its review starts.
type ModelRoute struct {
	Name     string        `yaml:"name,omitempty" json:"name,omitempty"`
	Model    ModelRef      `yaml:"model" json:"model"`
	Effort   *model.Effort `yaml:"effort,omitempty" json:"effort,omitempty"`
	Fallback *ModelRef     `yaml:"fallback,omitempty" json:"fallback,omitempty"`
	When     []Filter      `yaml:"when,omitempty" json:"when"`
}

// UnmarshalYAML takes a model, or a list of routes that ends with the
// default model, an entry with model alone.
func (m *ReviewModel) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.SequenceNode {
		return n.Decode(&m.Default)
	}
	var entries []ModelRoute
	// Node.Decode drops the strictness Parse asked for, so Load asks again.
	if err := n.Load(&entries, yaml.WithKnownFields()); err != nil {
		return err
	}
	if len(entries) == 0 {
		return errors.New("a list must end with the default model, an entry with model alone")
	}
	for i, r := range entries {
		if r.Model == "" {
			return fmt.Errorf("[%d]: model is required", i)
		}
	}
	last := len(entries) - 1
	if d := entries[last]; d.Name != "" || d.Effort != nil || d.Fallback != nil || len(d.When) > 0 {
		return fmt.Errorf("[%d]: the last entry is the default model and takes model alone; "+
			"review.effort and review.fallback are its effort and fallback", last)
	}
	m.Default, m.Routes = entries[last].Model, entries[:last]
	for i, r := range m.Routes {
		if len(r.When) == 0 {
			return fmt.Errorf("[%d]: when is required; only the last entry, the default model, has none", i)
		}
	}
	return nil
}

// Compile compiles the routes' conditions in place. The errors name the
// entry and the condition, as "[0].when[1]: ...".
func (m *ReviewModel) Compile() error {
	if m == nil {
		return nil
	}
	for i := range m.Routes {
		for j := range m.Routes[i].When {
			if err := m.Routes[i].When[j].Compile(); err != nil {
				return fmt.Errorf("[%d].when[%d]: %w", i, j, err)
			}
		}
	}
	return nil
}

// check compiles m's conditions in place and checks its routes' efforts,
// named in errors under where as validateOverrides names its keys.
func (m *ReviewModel) check(where string) error {
	if m == nil {
		return nil
	}
	if err := m.Compile(); err != nil {
		return fmt.Errorf("configfile: %s%s%w", where, keyModel, err)
	}
	for i, r := range m.Routes {
		if r.Effort != nil && *r.Effort != "" && !r.Effort.Valid() {
			return fmt.Errorf("configfile: %s%s[%d].effort must be %s, got %q", where, keyModel, i, model.EffortLevels, *r.Effort)
		}
	}
	return nil
}

// namedRef is a model setting by the key it is written under.
type namedRef struct {
	key string
	ref *ModelRef
}

// refs are the models m names with the keys they are written under: the
// default, then each route's model and fallback; nil m names none.
func (m *ReviewModel) refs() []namedRef {
	if m == nil {
		return nil
	}
	out := []namedRef{{keyModel, &m.Default}}
	for i := range m.Routes {
		r := &m.Routes[i]
		out = append(out, namedRef{fmt.Sprintf("%s[%d].model", keyModel, i), &r.Model},
			namedRef{fmt.Sprintf("%s[%d].fallback", keyModel, i), r.Fallback})
	}
	return out
}

// String is the value as the Configuration page states it: the model, or
// the default model and each route by its name, or its model without one.
func (m ReviewModel) String() string {
	if len(m.Routes) == 0 {
		return string(m.Default)
	}
	routes := make([]string, len(m.Routes))
	for i, r := range m.Routes {
		routes[i] = cmp.Or(r.Name, string(r.Model))
	}
	return fmt.Sprintf("%s, routes %s", m.Default, strings.Join(routes, ", "))
}

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
		for _, c := range r.When {
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
	}
	return m, errors.Join(errs...)
}

// routeLabel names route r and the condition c that picked it, as
// "deep (migrations)", or the condition alone for a route with no name.
func routeLabel(r ModelRoute, c Filter) string {
	if r.Name == "" {
		return c.Label()
	}
	return r.Name + " (" + c.Label() + ")"
}
