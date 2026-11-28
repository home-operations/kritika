package configfile

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v4"

	"github.com/home-operations/kritika/internal/prfilter"
	"github.com/home-operations/kritika/internal/review"
)

// Switch is a setting that a file writes as true or false, or as a block
// whose when lists the conditions, as a rule's does. A pull request must hold
// one of those conditions for the setting to be on for it. confidence.gate
// is a switch. The block turns the setting on and lists at least one
// condition. true is how it is on for every pull request.
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

// Approve is review.approve: true or false, or a block that turns approval
// on and sets what it allows. Findings is the most severe finding that a
// review can leave open and still be approved, p2 or none. Risk is the
// highest risk that a change can be rated and still be approved. When lists
// the conditions, one of which a pull request must hold for to be approved.
// The block sets at least one of them: true is on with the values that
// stand.
type Approve struct {
	On       bool            `yaml:"-"`
	When     []When          `yaml:"when"`
	Findings review.Severity `yaml:"findings"`
	Risk     review.Risk     `yaml:"risk"`
}

// UnmarshalYAML takes a boolean, or a mapping with when, findings or risk.
// A key written empty or null is refused rather than taken as left out, so
// a block that lost its conditions does not approve every pull request.
func (a *Approve) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		return n.Decode(&a.On)
	}
	// The values are read once loosely, aliases and merge keys resolved, to
	// tell a key written empty from one left out.
	var written map[string]any
	if err := n.Decode(&written); err != nil {
		return err
	}
	for _, key := range []string{"when", "findings", "risk"} {
		v, ok := written[key]
		if list, isList := v.([]any); ok && (v == nil || v == "" || isList && len(list) == 0) {
			return fmt.Errorf("%s must not be empty", key)
		}
	}
	type block Approve
	var b block
	// Node.Decode drops the strictness Parse asked for, so Load asks again.
	if err := n.Load(&b, yaml.WithKnownFields()); err != nil {
		return err
	}
	if len(b.When) == 0 && b.Findings == "" && b.Risk == "" {
		return errors.New("a block must set when, findings or risk; true is on for every pull request")
	}
	*a = Approve(b)
	a.On = true
	return nil
}

// String is the value as the Configuration page states it.
func (a Approve) String() string {
	var parts []string
	if a.Findings != "" {
		parts = append(parts, "findings "+string(a.Findings))
	}
	if a.Risk != "" {
		parts = append(parts, "risk "+string(a.Risk))
	}
	if len(a.When) > 0 {
		parts = append(parts, "when "+whenLabel(a.When))
	}
	if len(parts) == 0 {
		return strconv.FormatBool(a.On)
	}
	return strings.Join(parts, ", ")
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
	if a := r.Review.Approve; a != nil {
		if err := CheckWhen(a.When); err != nil {
			return fmt.Errorf("configfile: %s.%w", keyApprove, err)
		}
	}
	if g := r.Confidence.Gate; g != nil {
		if err := CheckWhen(g.When); err != nil {
			return fmt.Errorf("configfile: %s.%w", keyGate, err)
		}
	}
	if err := CheckWhen(r.Confidence.When); err != nil {
		return fmt.Errorf("configfile: confidence.%w", err)
	}
	return nil
}
