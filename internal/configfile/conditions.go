package configfile

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v4"

	"github.com/home-operations/kritika/internal/prfilter"
)

// Switch is a setting that a file writes as true or false, or as a block
// whose when lists the conditions, as a rule's does. A pull request must hold
// one of those conditions for the setting to be on for it. review.approve
// and confidence.gate are switches. The block turns the setting on and lists
// at least one condition. true is how it is on for every pull request.
type Switch struct {
	On   bool
	When []When
}

// UnmarshalYAML takes a boolean, or a mapping with when.
func (a *Switch) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		return n.Decode(&a.On)
	}
	var block struct {
		When []When `yaml:"when"`
	}
	// Node.Decode drops the strictness Parse asked for, so Load asks again.
	if err := n.Load(&block, yaml.WithKnownFields()); err != nil {
		return err
	}
	if len(block.When) == 0 {
		return errors.New("a block must list when conditions; true is on for every pull request")
	}
	a.On, a.When = true, block.When
	return nil
}

// String is the value as the Configuration page states it.
func (a Switch) String() string {
	if len(a.When) == 0 {
		return strconv.FormatBool(a.On)
	}
	return "when " + whenLabel(a.When)
}

// whenLabel names conditions, each by its name or else its expression.
func whenLabel(when []When) string {
	labels := make([]string, len(when))
	for i, w := range when {
		labels[i] = w.Label()
	}
	return strings.Join(labels, ", ")
}

// Label is the condition's name, or its expression when it has none.
func (w When) Label() string {
	if w.Name != "" {
		return w.Name
	}
	return w.Expr
}

// AnyHolds reports whether one of when holds for the pull request with
// the filter variables vars, or when is empty. A condition that does not
// compile or evaluate does not hold, because it cannot say that it does.
func AnyHolds(when []When, vars map[string]any) bool {
	return len(when) == 0 || slices.ContainsFunc(when, func(w When) bool {
		prg, err := prfilter.Compile(w.Expr)
		if err != nil {
			return false
		}
		ok, err := prg.Eval(vars)
		return err == nil && ok
	})
}

// ApprovesFor reports whether the review's approval conditions hold for
// the pull request with the filter variables vars: one of the admin's,
// and one of the repository file's own, where either lists any.
func (r Review) ApprovesFor(vars map[string]any) bool {
	return AnyHolds(r.ApproveWhen, vars) && AnyHolds(r.ApproveWhenInRepo, vars)
}

// checkConditions makes sure that the approval, gate, and confidence
// conditions that one scope writes are valid, the same way as a rule's.
func checkConditions(r *Overrides) error {
	for key, s := range map[string]*Switch{keyApprove: r.Review.Approve, keyGate: r.Confidence.Gate} {
		if s == nil {
			continue
		}
		if err := CheckWhen(s.When); err != nil {
			return fmt.Errorf("configfile: %s.%w", key, err)
		}
	}
	if err := CheckWhen(r.Confidence.When); err != nil {
		return fmt.Errorf("configfile: confidence.%w", err)
	}
	return nil
}
