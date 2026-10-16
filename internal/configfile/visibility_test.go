package configfile

import (
	"strings"
	"testing"
)

// TestVisibilityEntries: a visibility entry lays over the file's root for
// the repositories GitHub reports with that visibility, under the account's
// entries, and a repository whose visibility is unknown gets none.
func TestVisibilityEntries(t *testing.T) {
	t.Setenv("TEST_PRIVATE_KEY", "tok")
	t.Setenv("TEST_WEBHOOK_SECRET", "whsec")
	f := mustLoad(t, "review: { cost: true }\ntrigger: { limit: 5 }\n"+
		"visibility:\n  public: { review: { cost: false, approve: true }, trigger: { limit: 2 }, rules: [{ id: pub, rule: x }] }\n"+
		"  internal: { trigger: { limit: 3 } }\n"+
		acme("  acme/*: { review: { approve: false } }\n  acme/x: { trigger: { limit: 1 } }\n"))
	a := &f.Accounts[0]
	for _, tt := range []struct {
		name, repo string
		traits     RepoTraits
		cost       bool
		limit      int
	}{
		{"a public repository", "acme/y", RepoTraits{Visibility: "public"}, false, 2},
		{"a public repository with an entry", "acme/x", RepoTraits{Visibility: "public"}, false, 1},
		{"an internal repository", "acme/y", RepoTraits{Visibility: "internal"}, true, 3},
		{"a private repository", "acme/y", RepoTraits{Visibility: "private"}, true, 5},
		{"a repository of unknown visibility", "acme/y", RepoTraits{}, true, 5},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := f.Settings(a, tt.repo, tt.traits)
			if s.Review.Cost != tt.cost || s.MaxAutoReviews != tt.limit || s.Review.Approve {
				t.Fatalf("cost=%v limit=%d approve=%v, want cost=%v limit=%d and the account's approve",
					s.Review.Cost, s.MaxAutoReviews, s.Review.Approve, tt.cost, tt.limit)
			}
		})
	}
	public := RepoTraits{Visibility: "public"}
	src := f.Sources(a, "acme/y", public)
	if src["review.cost"] != SourceVisibility || src["trigger.limit"] != SourceVisibility || src["review.approve"] != SourceAccount {
		t.Fatalf("sources of a public repository = %v", src)
	}
	if src := f.Sources(a, "acme/y", RepoTraits{}); src["review.cost"] != SourceDefaults || src["trigger.limit"] != SourceDefaults {
		t.Fatalf("sources of a repository of unknown visibility = %v", src)
	}
	if scopes := f.RuleScopes(a, "acme/y", public); scopes["pub"] != ScopeVisibility {
		t.Fatalf("rule scopes of a public repository = %v", scopes)
	}
	if scopes := f.RuleScopes(a, "acme/y", RepoTraits{}); scopes["pub"] != "" {
		t.Fatalf("rule scopes of a repository of unknown visibility = %v", scopes)
	}
	if rules := f.Settings(a, "acme/y", public).Review.Rules; len(rules) != 1 || rules[0].ID != "pub" {
		t.Fatalf("rules of a public repository = %+v", rules)
	}
}

func TestVisibilityEnabled(t *testing.T) {
	t.Setenv("TEST_PRIVATE_KEY", "tok")
	t.Setenv("TEST_WEBHOOK_SECRET", "whsec")
	f := mustLoad(t, "visibility: { public: { enabled: false } }\n"+acme("  acme/x: {}\n"))
	a := &f.Accounts[0]
	if f.Runs(a, "acme/x", RepoTraits{Visibility: "public"}) || !f.Runs(a, "acme/x", RepoTraits{Visibility: "private"}) || !f.Runs(a, "acme/x", RepoTraits{}) {
		t.Fatal("a visibility entry's enabled applies to the repositories of that visibility alone")
	}
}

func TestVisibilityRejectsEntries(t *testing.T) {
	t.Setenv("TEST_PRIVATE_KEY", "tok")
	t.Setenv("TEST_WEBHOOK_SECRET", "whsec")
	for _, tt := range []struct{ name, yaml, want string }{
		{"an unknown visibility", "visibility: { secret: {} }\n", "visibility.secret: an entry is keyed public, private, internal"},
		{"a model of an undeclared provider", "visibility: { public: { review: { model: nope/x } } }\n", `visibility.public.review.model references provider "nope"`},
		{"a broken trigger", "visibility: { public: { trigger: { exclude: [{ name: a, expr: 'pr.' }] } } }\n", "visibility.public.trigger."},
		{"limits", "visibility: { public: { limits: { concurrency: 1 } } }\n", "field limits not found"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := load(t, tt.yaml+minimal)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}
