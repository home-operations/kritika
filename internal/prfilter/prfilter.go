// Package prfilter compiles and evaluates a CEL boolean expression that
// decides which pull requests kritika reviews (a condition of the
// configuration's trigger.include and trigger.exclude lists).
//
// The expression sees a single variable, pr: a map of the PR's fields. The
// caller supplies that map (ingest builds it from the webhook's pull request,
// the worker from the stored row). So this package stays decoupled from the
// forge model and is testable with plain maps. An expression must evaluate to
// a boolean. The program is type-checked once at Compile, so a malformed
// filter fails fast at startup rather than per request.
//
// CEL is the right tool here: it is a safe, bounded, non-Turing-complete
// expression language (no I/O, no unbounded loops). So an admin-supplied
// predicate cannot hang or escape. And the home-ops/Kubernetes audience
// already knows it from admission policies.
package prfilter

import (
	"fmt"

	"cel.dev/cel-go/cel"
	celast "cel.dev/cel-go/common/ast"
	"cel.dev/cel-go/common/operators"
	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/interpreter"
)

// Program is a compiled, type-checked PR-filter expression.
type Program struct {
	prg cel.Program
	ast *celast.AST
	src string
}

// evalCostLimit caps the CEL cost of a single filter evaluation. The
// expression is admin-supplied (trusted) and its attacker-influenced inputs
// (pr.title, pr.labels) are forge-bounded. So this is defense-in-depth
// against a pathological admin's expression rather than a likely attack. It
// is a ceiling that no reasonable PR predicate approaches, while it still
// bounds an accidental blow-up. CEL has no loops, so a finite cost is
// guaranteed to exist.
const evalCostLimit = 1_000_000

// Compile parses and type-checks expr and returns a runnable Program. It
// fails when the expression is syntactically invalid, references unknown
// variables/functions, or cannot produce a boolean. It also fails when the
// expression gives matches() a constant pattern that is not a valid regular
// expression.
func Compile(expr string) (*Program, error) {
	// Keep the lazy error catalog (cel-expr/cel-go#1500) local to each
	// compilation. cel.NewEnv already reuses the standard library.
	env, err := cel.NewEnv(cel.Variable("pr", cel.MapType(cel.StringType, cel.DynType)))
	if err != nil {
		return nil, fmt.Errorf("prfilter: build env: %w", err)
	}
	ast, iss := env.Compile(expr)
	if iss != nil && iss.Err() != nil {
		return nil, fmt.Errorf("prfilter: %w", iss.Err())
	}
	// The filter is a yes/no decision. Field access on the dyn-valued pr map
	// is statically dyn (only known boolean at runtime), so accept bool or
	// dyn here. A literal of the wrong type (for example, 42, "x") is
	// statically typed and rejected. Eval enforces an actual boolean result.
	switch ast.OutputType().Kind() {
	case types.BoolKind, types.DynKind:
	default:
		return nil, fmt.Errorf("prfilter: expression must evaluate to a boolean, got %s", ast.OutputType())
	}
	// OptimizeRegex compiles a constant pattern here, so a malformed one
	// fails Compile instead of every Eval.
	prg, err := env.Program(ast, cel.CostLimit(evalCostLimit), cel.OptimizeRegex(interpreter.MatchesRegexOptimization))
	if err != nil {
		return nil, fmt.Errorf("prfilter: program: %w", err)
	}
	return &Program{prg: prg, ast: ast.NativeRep(), src: expr}, nil
}

// Uses reports whether the expression can read field of pr. It does not
// only when every mention of pr selects or indexes another field by name:
// pr used whole (aliased in a list, tested with "in", compared) or indexed
// by a key that is not a literal can read any field.
func (p *Program) Uses(field string) bool {
	return len(celast.MatchDescendants(celast.NavigateAST(p.ast), func(e celast.NavigableExpr) bool {
		if e.Kind() != celast.IdentKind || e.AsIdent() != "pr" {
			return false
		}
		parent, ok := e.Parent()
		if !ok {
			return true
		}
		switch parent.Kind() {
		case celast.SelectKind:
			return parent.AsSelect().FieldName() == field
		case celast.CallKind:
			call := parent.AsCall()
			if call.FunctionName() != operators.Index || len(call.Args()) != 2 || call.Args()[0].ID() != e.ID() {
				return true
			}
			key := call.Args()[1]
			return key.Kind() != celast.LiteralKind || key.AsLiteral().Value() == field
		}
		return true
	})) > 0
}

// Eval runs the expression against the given pr field map and reports whether
// the PR is allowed. A runtime error or a non-boolean result is returned as
// an error. The caller decides the fail-safe: kritika skips the PR and logs.
func (p *Program) Eval(pr map[string]any) (bool, error) {
	out, _, err := p.prg.Eval(map[string]any{"pr": pr})
	if err != nil {
		return false, fmt.Errorf("prfilter: eval %q: %w", p.src, err)
	}
	b, ok := out.Value().(bool)
	if !ok {
		return false, fmt.Errorf("prfilter: %q produced %T, want bool", p.src, out.Value())
	}
	return b, nil
}

// Source returns the original expression text (for logs and diagnostics).
func (p *Program) Source() string { return p.src }
