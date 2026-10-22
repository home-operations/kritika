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

// Approve is review.approve as a file writes it: true or false, or a
// block whose when lists the conditions, one of which a pull request must
// hold for to be approved, as a rule's do. The block turns approval on and
// lists at least one condition; true is how every pull request is.
type Approve struct {
	On   bool
	When []When
}

// UnmarshalYAML takes a boolean, or a mapping with when.
func (a *Approve) UnmarshalYAML(n *yaml.Node) error {
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
		return errors.New("an approve block must list when conditions; true approves every pull request")
	}
	a.On, a.When = true, block.When
	return nil
}

// String is the value as the Configuration page states it.
func (a Approve) String() string {
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
// compile or evaluate does not hold, as it could not say it does.
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

// checkConditions checks the approval and confidence conditions one scope
// writes, as a rule's are checked.
func checkConditions(where string, r *Overrides) error {
	if a := r.Review.Approve; a != nil {
		if err := CheckWhen(a.When); err != nil {
			return fmt.Errorf("configfile: %s%s.%w", where, keyApprove, err)
		}
	}
	if err := CheckWhen(r.Confidence.When); err != nil {
		return fmt.Errorf("configfile: %sconfidence.%w", where, err)
	}
	return nil
}
