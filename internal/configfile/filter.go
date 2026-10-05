package configfile

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/home-operations/kritika/internal/prfilter"
)

// Filter is one condition on a pull request, CEL over pr, in a list that
// includes or excludes pull requests. Name, when set, says which condition
// decided, and lets a narrower scope replace it.
type Filter struct {
	Name string `yaml:"name,omitempty" json:"name"`
	Expr string `yaml:"expr" json:"expr"`

	prg *prfilter.Program
}

// Label is what the condition is called where one is named: its name, or
// its expression without one.
func (f Filter) Label() string { return cmp.Or(f.Name, f.Expr) }

// Compile compiles the condition and smoke-tests it against SamplePR, so
// one that type-checks but fails at runtime (a field of the wrong type, a
// non-boolean result) is caught when it is read rather than on the first
// pull request.
func (f *Filter) Compile() error {
	if strings.TrimSpace(f.Expr) == "" {
		return errors.New("expr is required")
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
func (fs Filters) Skips(vars map[string]any) (bool, *Filter, error) {
	for i := range fs.Exclude {
		if ok, err := fs.Exclude[i].prg.Eval(vars); err != nil || ok {
			return true, &fs.Exclude[i], err
		}
	}
	for i := range fs.Include {
		ok, err := fs.Include[i].prg.Eval(vars)
		if err != nil {
			return true, &fs.Include[i], err
		}
		if ok {
			return false, nil, nil
		}
	}
	return len(fs.Include) > 0, nil, nil
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
