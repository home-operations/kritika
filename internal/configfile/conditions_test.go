package configfile

import (
	"strings"
	"testing"
)

// TestApproveForms: review.approve is true, false, or a block of when
// conditions, which turns approval on.
func TestApproveForms(t *testing.T) {
	authEnv(t)
	for approve, want := range map[string]struct {
		on   bool
		when string
	}{
		"true":                             {true, ""},
		"{ when: [{ expr: 'pr.draft' }] }": {true, "pr.draft"},
		"false":                            {false, ""},
	} {
		f := mustLoad(t, "review:\n  approve: "+approve+"\n"+minimal)
		r := f.Settings(&f.Accounts[0]).Review
		if r.Approve != want.on || whenLabel(r.ApproveWhen) != want.when || len(r.ApproveWhenInRepo) != 0 {
			t.Fatalf("approve %s: approve %v when %q, want %v and %q", approve, r.Approve, whenLabel(r.ApproveWhen), want.on, want.when)
		}
	}
	root := mustLoad(t, "review:\n  approve:\n    when:\n      - { name: bumps, expr: pr.bot }\n"+minimal)
	if r := root.Settings(&root.Accounts[0]).Review; !r.Approve || whenLabel(r.ApproveWhen) != "bumps" {
		t.Fatalf("root approve = %v when %q", r.Approve, whenLabel(r.ApproveWhen))
	}
	if got := root.FileLayer().Defaults; !strings.Contains(defaultValue(got, keyApprove), "when bumps") {
		t.Fatalf("the Configuration page states %q", defaultValue(got, keyApprove))
	}
	bot := with(SamplePR(), "bot", true)
	if r := root.Settings(&root.Accounts[0]).Review; !r.ApprovesFor(bot) || r.ApprovesFor(SamplePR()) {
		t.Fatal("approval conditions must hold for a bot's pull request alone")
	}
}

func defaultValue(defaults []FileDefault, key string) string {
	for _, d := range defaults {
		if d.Key == key {
			return d.Value
		}
	}
	return ""
}

func TestApproveRejects(t *testing.T) {
	authEnv(t)
	for name, tt := range map[string]struct{ approve, want string }{
		"an empty block":       {"{}", "a block must list when conditions"},
		"an empty list":        {"{ when: [] }", "a block must list when conditions"},
		"an unknown key":       {"{ if: [] }", "field if not found"},
		"a number":             {"3", "cannot"},
		"no expression":        {"{ when: [{ name: a }] }", "review.approve.when[0]: expr is required"},
		"a broken expression":  {"{ when: [{ expr: 'pr.(' }] }", "review.approve.when[0]"},
		"a diff condition":     {"{ when: [{ expr: 'pr.lines > 1' }] }", "pr.lines is known to a trigger condition alone"},
		"a name given twice":   {"{ when: [{ name: a, expr: pr.bot }, { name: a, expr: pr.draft }] }", "name \"a\" is given twice"},
		"a non-boolean result": {"{ when: [{ expr: pr.title }] }", "review.approve.when[0]"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := load(t, "review:\n  approve: "+tt.approve+"\n"+minimal); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}

// TestConfidenceWhen: confidence.when scopes scoring as review.approve's
// conditions do approval. Written empty, every pull request is scored.
func TestConfidenceWhen(t *testing.T) {
	authEnv(t)
	f := mustLoad(t, "confidence:\n  when:\n    - { name: renovate, expr: 'pr.headRef.startsWith(\"renovate/\")' }\n"+minimal)
	c := f.Settings(&f.Accounts[0]).Confidence
	if whenLabel(c.When) != "renovate" || !AnyHolds(c.When, with(SamplePR(), "headRef", "renovate/go")) || AnyHolds(c.When, SamplePR()) {
		t.Fatalf("confidence when %q", whenLabel(c.When))
	}
	if src := f.Sources(&f.Accounts[0]); src[keyConfidenceWhen] != SourceDefaults {
		t.Fatalf("source = %q", src[keyConfidenceWhen])
	}
	all := mustLoad(t, "confidence: { when: [] }\n"+minimal)
	if c := all.Settings(&all.Accounts[0]).Confidence; len(c.When) != 0 || !AnyHolds(c.When, SamplePR()) {
		t.Fatalf("an empty when must score every pull request: %+v", c.When)
	}
	if _, err := load(t, "confidence:\n  when: [{ expr: 'pr.lines > 1' }]\n"+minimal); err == nil || !strings.Contains(err.Error(), "configfile: confidence.when[0]") {
		t.Fatalf("a diff condition = %v", err)
	}
}

// TestRepositoryConditions: a repository file's conditions must hold beside
// the admin's.
func TestRepositoryConditions(t *testing.T) {
	r := Review{ApproveWhen: []When{{Expr: "pr.bot"}}, ApproveWhenInRepo: []When{{Expr: "pr.draft"}}}
	for name, tt := range map[string]struct {
		vars map[string]any
		want bool
	}{
		"both hold":         {with(with(SamplePR(), "bot", true), "draft", true), true},
		"the admin's alone": {with(SamplePR(), "bot", true), false},
		"the file's alone":  {with(SamplePR(), "draft", true), false},
	} {
		if got := r.ApprovesFor(tt.vars); got != tt.want {
			t.Fatalf("%s: ApprovesFor = %v, want %v", name, got, tt.want)
		}
	}
	if !(Review{}).ApprovesFor(SamplePR()) || !AnyHolds(nil, SamplePR()) {
		t.Fatal("no conditions hold for every pull request")
	}
	if AnyHolds([]When{{Expr: "pr.nope == 1"}}, SamplePR()) {
		t.Fatal("a condition that fails to evaluate must not hold")
	}
}

// TestGateForms: confidence.gate takes the forms review.approve does.
func TestGateForms(t *testing.T) {
	authEnv(t)
	f := mustLoad(t, "confidence:\n  gate:\n    when:\n      - { name: renovate, expr: 'pr.headRef.startsWith(\"renovate/\")' }\n"+minimal)
	if c := f.Settings(&f.Accounts[0]).Confidence; !c.Gate || whenLabel(c.GateWhen) != "renovate" {
		t.Fatalf("gate %v when %q", c.Gate, whenLabel(c.GateWhen))
	}
	plain := mustLoad(t, "confidence: { gate: true }\n"+minimal)
	if c := plain.Settings(&plain.Accounts[0]).Confidence; !c.Gate || len(c.GateWhen) != 0 {
		t.Fatalf("gate: true must gate every pull request: %v %q", c.Gate, whenLabel(c.GateWhen))
	}
	if got := defaultValue(f.FileLayer().Defaults, keyGate); got != "when renovate" {
		t.Fatalf("the Configuration page states %q", got)
	}
	for name, tt := range map[string]struct{ gate, want string }{
		"an empty block":   {"{}", "a block must list when conditions"},
		"a diff condition": {"{ when: [{ expr: 'pr.lines > 1' }] }", "confidence.gate.when[0]: pr.lines"},
	} {
		if _, err := load(t, "confidence:\n  gate: "+tt.gate+"\n"+minimal); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Fatalf("%s: err = %v, want %q", name, err, tt.want)
		}
	}
}
