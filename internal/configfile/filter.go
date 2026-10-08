package configfile

import (
	"cmp"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/home-operations/kritika/internal/chunk"
	"github.com/home-operations/kritika/internal/prfilter"
)

// Filter is one condition on a pull request in a list that includes or
// excludes pull requests: Expr, CEL over pr, Paths, globs one of which a
// changed path must match, or both, when both must hold. Name, when set,
// says which condition decided, and lets a narrower scope replace it.
type Filter struct {
	Name  string   `yaml:"name,omitempty" json:"name"`
	Expr  string   `yaml:"expr,omitempty" json:"expr"`
	Paths []string `yaml:"paths,omitempty" json:"paths,omitempty"`

	prg *prfilter.Program
}

// Label is what the condition is called where one is named: its name, or
// without one its expression, or its globs.
func (f Filter) Label() string { return cmp.Or(f.Name, f.Expr, strings.Join(f.Paths, ", ")) }

// Compile checks the condition's globs, compiles its expression and
// smoke-tests it against SamplePR, so one that type-checks but fails at
// runtime (a field of the wrong type, a non-boolean result) is caught when
// it is read rather than on the first pull request.
func (f *Filter) Compile() error {
	if strings.TrimSpace(f.Expr) == "" && len(f.Paths) == 0 {
		return errors.New("expr or paths is required")
	}
	for i, g := range f.Paths {
		if !ValidGlob(g) {
			return fmt.Errorf("paths[%d] %q is not a valid glob", i, g)
		}
	}
	if strings.TrimSpace(f.Expr) == "" {
		return nil
	}
	prg, err := prfilter.Compile(f.Expr)
	if err != nil {
		return err
	}
	if _, err := prg.Eval(SamplePR()); err != nil {
		return fmt.Errorf("smoke test against a sample pull request: %w", err)
	}
	f.prg = prg
	return nil
}

// Diff is what a condition judges of a pull request once its diff is
// fetched: the lines it adds and removes, ignored paths left out, which an
// expression reads as pr.lines, and the paths it changes.
type Diff struct {
	Lines   int
	Changed []string
}

// needsDiff reports whether the condition, compiled, cannot be judged
// before the pull request's diff is fetched.
func (f Filter) needsDiff() bool {
	return len(f.Paths) > 0 || (f.prg != nil && f.prg.Uses(linesVar))
}

// holds reports whether vars, and for a condition with globs the changed
// paths of d, meet the condition.
func (f Filter) holds(vars map[string]any, d *Diff) (bool, error) {
	if f.prg != nil {
		if ok, err := f.prg.Eval(vars); err != nil || !ok {
			return false, err
		}
	}
	return len(f.Paths) == 0 || slices.ContainsFunc(d.Changed, func(c string) bool { return chunk.Matches(f.Paths, c) }), nil
}

// linesVar is the pr field only a fetched diff gives.
const linesVar = "lines"

// Filters decide whether a pull request is reviewed: it is when one of
// Include holds, or Include is empty, and none of Exclude does.
type Filters struct {
	Include []Filter `yaml:"include,omitempty" json:"include"`
	Exclude []Filter `yaml:"exclude,omitempty" json:"exclude"`
}

// Compile compiles both lists in place, and rejects a name given twice in
// one of them: a name stands for one condition. The errors name the list
// and the condition, as "include[1]: ...".
func (fs *Filters) Compile() error {
	for _, l := range []struct {
		key  string
		list []Filter
	}{{"include", fs.Include}, {"exclude", fs.Exclude}} {
		for i := range l.list {
			f := &l.list[i]
			if f.Name != "" && slices.ContainsFunc(l.list[:i], func(o Filter) bool { return o.Name == f.Name }) {
				return fmt.Errorf("%s[%d]: name %q is given twice", l.key, i, f.Name)
			}
			if err := f.Compile(); err != nil {
				return fmt.Errorf("%s[%d]: %w", l.key, i, err)
			}
		}
	}
	return nil
}

// Skips reports whether fs, compiled, keep a pull request with the filter
// variables vars from being reviewed, and the condition that decided: the
// exclusion that holds, or nil when it is that no inclusion does. A
// condition that fails to evaluate skips, and is returned with its error.
//
// d is the pull request's diff, nil before it is fetched. Then a condition
// that needs it is left for later: such an exclusion does not yet hold,
// and such an inclusion may, so the pull request is not kept out for want
// of an inclusion while one is still to be judged.
func (fs Filters) Skips(vars map[string]any, d *Diff) (bool, *Filter, error) {
	if d != nil {
		vars = maps.Clone(vars)
		vars[linesVar] = d.Lines
	}
	for i := range fs.Exclude {
		if d == nil && fs.Exclude[i].needsDiff() {
			continue
		}
		if ok, err := fs.Exclude[i].holds(vars, d); err != nil || ok {
			return true, &fs.Exclude[i], err
		}
	}
	pending := false
	for i := range fs.Include {
		if d == nil && fs.Include[i].needsDiff() {
			pending = true
			continue
		}
		ok, err := fs.Include[i].holds(vars, d)
		if err != nil {
			return true, &fs.Include[i], err
		}
		if ok {
			return false, nil, nil
		}
	}
	return len(fs.Include) > 0 && !pending, nil, nil
}

// NeedsDiff reports whether one of fs's conditions, compiled, cannot be
// judged before the pull request's diff is fetched.
func (fs Filters) NeedsDiff() bool {
	return slices.ContainsFunc(fs.Include, Filter.needsDiff) || slices.ContainsFunc(fs.Exclude, Filter.needsDiff)
}

// Empty reports whether fs hold no condition.
func (fs Filters) Empty() bool { return len(fs.Include) == 0 && len(fs.Exclude) == 0 }

// with is fs with more laid over them, list by list: a condition of more
// with a name the list already has replaces that condition where it
// stands, and the rest follow in order.
func (fs Filters) with(more Filters) Filters {
	return Filters{Include: withFilters(fs.Include, more.Include), Exclude: withFilters(fs.Exclude, more.Exclude)}
}

func withFilters(filters, more []Filter) []Filter {
	if len(more) == 0 {
		return filters
	}
	out := slices.Clone(filters)
	for _, f := range more {
		if i := slices.IndexFunc(out, func(o Filter) bool { return f.Name != "" && o.Name == f.Name }); i >= 0 {
			out[i] = f
		} else {
			out = append(out, f)
		}
	}
	return out
}

// Named reports whether one of fs's conditions, in either list, has name.
func (fs Filters) Named(name string) bool {
	has := func(o Filter) bool { return o.Name == name }
	return slices.ContainsFunc(fs.Include, has) || slices.ContainsFunc(fs.Exclude, has)
}
