package configfile

import (
	"strings"
	"testing"
)

// TestApproveForms: review.approve is true, false, or a block of when
// conditions, which turns approval on; a narrower scope replaces it whole.
func TestApproveForms(t *testing.T) {
	authEnv(t)
	f := mustLoad(t, "review:\n  approve:\n    when:\n      - { name: bumps, expr: pr.bot }\n"+
		acme("  acme/*: { review: { approve: true } }\n  acme/x: { review: { approve: { when: [{ expr: 'pr.draft' }] } } }\n  acme/off: { review: { approve: false } }\n"))
	a := &f.Accounts[0]
	for repo, want := range map[string]struct {
		on   bool
		when string
	}{
		"acme/y":   {true, ""},
		"acme/x":   {true, "pr.draft"},
		"acme/off": {false, ""},
	} {
		r := f.Settings(a, repo).Review
		if r.Approve != want.on || whenLabel(r.ApproveWhen) != want.when || len(r.ApproveWhenInRepo) != 0 {
			t.Fatalf("%s: approve %v when %q, want %v and %q", repo, r.Approve, whenLabel(r.ApproveWhen), want.on, want.when)
		}
	}
	root := mustLoad(t, "review:\n  approve:\n    when:\n      - { name: bumps, expr: pr.bot }\n"+minimal)
	if r := root.Settings(&root.Accounts[0], "acme/y").Review; !r.Approve || whenLabel(r.ApproveWhen) != "bumps" {
		t.Fatalf("root approve = %v when %q", r.Approve, whenLabel(r.ApproveWhen))
	}
	if got := root.FileLayer().Defaults; !strings.Contains(defaultValue(got, keyApprove), "when bumps") {
		t.Fatalf("the Configuration page states %q", defaultValue(got, keyApprove))
	}
	bot := with(SamplePR(), "bot", true)
	if r := root.Settings(&root.Accounts[0], "acme/y").Review; !r.ApprovesFor(bot) || r.ApprovesFor(SamplePR()) {
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
		"an empty block":       {"{}", "an approve block must list when conditions"},
		"an empty list":        {"{ when: [] }", "an approve block must list when conditions"},
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
// conditions do approval; a narrower scope's, even empty, replaces it.
func TestConfidenceWhen(t *testing.T) {
	authEnv(t)
	f := mustLoad(t, "confidence:\n  when:\n    - { name: renovate, expr: 'pr.headRef.startsWith(\"renovate/\")' }\n"+
		acme("  acme/all: { confidence: { when: [] } }\n"))
	a := &f.Accounts[0]
	c := f.Settings(a, "acme/y").Confidence
	if whenLabel(c.When) != "renovate" || !AnyHolds(c.When, with(SamplePR(), "headRef", "renovate/go")) || AnyHolds(c.When, SamplePR()) {
		t.Fatalf("confidence when %q", whenLabel(c.When))
	}
	if all := f.Settings(a, "acme/all").Confidence; len(all.When) != 0 || !AnyHolds(all.When, SamplePR()) {
		t.Fatalf("an empty when must score every pull request: %+v", all.When)
	}
	if src := f.Sources(a, "acme/all"); src[keyConfidenceWhen] != SourceAccount {
		t.Fatalf("source = %q", src[keyConfidenceWhen])
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
