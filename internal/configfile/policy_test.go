package configfile

import "testing"

// TestPoliciesNameSettings checks every key of the table names a setting
// the file's root has.
func TestPoliciesNameSettings(t *testing.T) {
	for _, key := range Policies {
		if _, ok := SpecValue(&Defaults{}, key); !ok {
			t.Errorf("%s: the root has no such setting", key)
		}
	}
}

func TestSpecValue(t *testing.T) {
	steps := 5
	r := &Overrides{Agent: Agent{Steps: &steps}}
	if v, ok := SpecValue(r, "agent.steps"); !ok || *v.(*int) != 5 {
		t.Fatalf("agent.steps = %v, %v", v, ok)
	}
	if v, ok := SpecValue(&Defaults{}, "limits.concurrency"); !ok || v.(*int) != nil {
		t.Fatalf("limits.concurrency = %v, %v", v, ok)
	}
	if v, ok := SpecValue(&Defaults{}, "agent.nope"); ok {
		t.Fatalf("agent.nope = %v, %v; want no such key", v, ok)
	}
}

// TestSources: a setting the root writes comes from the defaults, one it
// leaves out from kritika's own, and limits an account's entry writes from
// the account.
func TestSources(t *testing.T) {
	t.Setenv("TEST_PRIVATE_KEY", "tok")
	t.Setenv("TEST_WEBHOOK_SECRET", "whsec")
	f := mustLoad(t, "trigger: { limit: 2, exclude: [{ name: forks, expr: pr.fork }] }\nagent: { steps: 9 }\ncomments: { dashboard: false }\n"+minimal)
	s := f.Sources(&f.Accounts[0])
	for key, want := range map[string]Source{
		"trigger.limit": SourceDefaults, "trigger.exclude": SourceDefaults, "agent.steps": SourceDefaults, "comments": SourceDefaults, "comments.dashboard": SourceDefaults,
		"enabled": SourceDefault, "trigger.include": SourceDefault, "agent.tokens": SourceDefault, "agent.prompt": SourceDefault, "review.model": SourceDefault,
		"ignore": SourceDefault, "limits": SourceDefault,
	} {
		if s[key] != want {
			t.Errorf("%s from %s, want %s", key, s[key], want)
		}
	}
	if s := mustLoad(t, minimal).Sources(&Account{}); s["trigger.limit"] != SourceDefault || s["trigger.exclude"] != SourceDefault {
		t.Fatalf("a file writing nothing = %v", s)
	}
	root := mustLoad(t, "limits: { concurrency: 2 }\n"+minimal)
	if got := root.Sources(&root.Accounts[0])["limits"]; got != SourceDefaults {
		t.Fatalf("limits the root writes from %s, want %s", got, SourceDefaults)
	}
	own := mustLoad(t, "limits: { concurrency: 2 }\n"+acmeAccount("    limits: { concurrency: 7 }\n"))
	if got := own.Sources(&own.Accounts[0])["limits"]; got != SourceAccount {
		t.Fatalf("limits the account writes from %s, want %s", got, SourceAccount)
	}
}
