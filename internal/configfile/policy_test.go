package configfile

import (
	"slices"
	"testing"
)

// TestPoliciesNameSettings checks every row of the table names a setting
// each scope it lists has, and no scope it leaves out.
func TestPoliciesNameSettings(t *testing.T) {
	specs := map[Scope]any{ScopeDefaults: &Defaults{}, ScopeAccount: &Account{}, ScopeRepository: &Repository{}}
	for _, p := range Policies {
		for scope, spec := range specs {
			_, ok := SpecValue(spec, p.Key)
			if want := slices.Contains(p.Scopes, scope); ok != want {
				t.Errorf("%s at %s: found %v, want %v", p.Key, scope, ok, want)
			}
		}
	}
}

func TestSpecValue(t *testing.T) {
	steps := 5
	r := &Repository{Name: "a/b", Agent: Agent{Steps: &steps}}
	if v, ok := SpecValue(r, "agent.steps"); !ok || *v.(*int) != 5 {
		t.Fatalf("agent.steps = %v, %v", v, ok)
	}
	if v, ok := SpecValue(&Account{}, "limits.concurrency"); !ok || v.(*int) != nil {
		t.Fatalf("limits.concurrency = %v, %v", v, ok)
	}
	if v, ok := SpecValue(&Account{}, "enabled"); !ok || v.(*bool) != nil {
		t.Fatalf("enabled = %v, %v", v, ok)
	}
}

func TestSources(t *testing.T) {
	t.Setenv("TEST_PRIVATE_KEY", "tok")
	t.Setenv("TEST_WEBHOOK_SECRET", "whsec")
	f := mustLoad(t, "trigger: { settle: 2m }\nagent: { steps: 9 }\n"+
		acme("  acme/*: { trigger: { exclude: [{ name: forks, expr: pr.fork }] } }\n  acme/x: { trigger: { settle: 0s, include: [{ expr: \"true\" }] } }\n"))
	s := f.Sources(&f.Accounts[0], "acme/x")
	for key, want := range map[string]Source{
		"trigger.settle": SourceAccount, "trigger.exclude": SourceAccount, "agent.steps": SourceDefaults, "enabled": SourceDefault, "trigger.include": SourceAccount,
		"agent.tokens": SourceDefault, "review.model": SourceDefault, "trigger.ignore": SourceDefault,
	} {
		if s[key] != want {
			t.Errorf("%s from %s, want %s", key, s[key], want)
		}
	}
	if s := f.Sources(&Account{Forge: ForgeGitHub, Name: "other"}, "other/x"); s["trigger.settle"] != SourceDefaults || s["trigger.exclude"] != SourceDefault {
		t.Fatalf("an account without an entry = %v", s)
	}
}
