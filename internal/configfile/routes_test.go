package configfile

import (
	"slices"
	"strings"
	"testing"
)

// routesProviders declares the providers the agents' models name.
const routesProviders = `providers:
  a: { type: anthropic, apiKey: { env: TEST_WEBHOOK_SECRET } }
  o: { type: openai, apiKey: { env: TEST_WEBHOOK_SECRET } }
`

// routes is review.agents with two agents that replace the default pass
// and a specialist between them, which routes nothing.
const routes = `review:
  effort: medium
  model: a/sonnet
  agents:
    - name: deep
      model: a/opus
      effort: high
      fallback: o/big
      when:
        - { name: migrations, paths: ["db/migrations/**"] }
        - { name: large, expr: pr.lines > 800 }
    - name: security
      model: a/opus
      categories: [security]
    - name: bots
      model: o/small
      when: [{ expr: pr.bot }]
`

func TestAgentRoutes(t *testing.T) {
	t.Setenv("TEST_PRIVATE_KEY", "tok")
	t.Setenv("TEST_WEBHOOK_SECRET", "whsec")
	f := mustLoad(t, routesProviders+routes+minimal)
	a, _ := f.Account(ForgeGitHub, "acme")
	s := f.Settings(a)
	m := s.Models
	if m.Review != "a/sonnet" || m.Effort != "medium" || m.Fallback != "" || len(m.Routes) != 2 {
		t.Fatalf("models = %+v; the agents without categories are its routes", m)
	}
	if _, ok := s.Reviewers["security"]; !ok || len(s.Reviewers) != 1 {
		t.Fatalf("specialists = %+v, want security alone", s.Reviewers)
	}
	if !slices.ContainsFunc(f.UnpricedModels(), func(u UnpricedModel) bool { return u.Setting == "review.agents[0].fallback" && u.Ref == "o/big" }) {
		t.Fatalf("UnpricedModels = %+v, want the agents' models listed", f.UnpricedModels())
	}
	bot := with(SamplePR(), "bot", true)
	for _, tt := range []struct {
		name                     string
		vars                     map[string]any
		diff                     *Diff
		review, effort, fallback ModelRef
		route                    string
	}{
		{"a migration among other files", SamplePR(), &Diff{Lines: 12, Changed: []string{"README.md", "db/migrations/0042.sql"}},
			"a/opus", "high", "o/big", "deep (migrations)"},
		{"a large change", SamplePR(), &Diff{Lines: 900, Changed: []string{"main.go"}}, "a/opus", "high", "o/big", "deep (large)"},
		{"the first agent that holds wins", bot, &Diff{Lines: 900, Changed: []string{"main.go"}}, "a/opus", "high", "o/big", "deep (large)"},
		{"an agent that sets no effort or fallback keeps the default's", bot, &Diff{Lines: 3, Changed: []string{"go.mod"}},
			"o/small", "medium", "", "bots (pr.bot)"},
		{"without the diff, paths and lines do not hold", SamplePR(), nil, "a/sonnet", "medium", "", ""},
		{"no agent holds", SamplePR(), &Diff{Lines: 3, Changed: []string{"main.go"}}, "a/sonnet", "medium", "", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := m.Pick(tt.vars, tt.diff)
			if err != nil {
				t.Fatal(err)
			}
			if got.Review != tt.review || string(got.Effort) != string(tt.effort) || got.Fallback != tt.fallback || got.Route != tt.route {
				t.Fatalf("Pick = %s/%s fallback %q route %q, want %s/%s fallback %q route %q",
					got.Review, got.Effort, got.Fallback, got.Route, tt.review, tt.effort, tt.fallback, tt.route)
			}
		})
	}
}

// TestAgentAlways: an agent without categories or conditions always
// replaces the default pass, which is how an admin takes it over; one
// that is off replaces nothing.
func TestAgentAlways(t *testing.T) {
	t.Setenv("TEST_PRIVATE_KEY", "tok")
	t.Setenv("TEST_WEBHOOK_SECRET", "whsec")
	f := mustLoad(t, routesProviders+`review:
  model: a/sonnet
  agents:
    - { name: off, model: o/small, enabled: false }
    - { name: always, model: a/opus }
`+minimal)
	a, _ := f.Account(ForgeGitHub, "acme")
	got, err := f.Settings(a).Models.Pick(SamplePR(), nil)
	if err != nil || got.Review != "a/opus" || got.Route != "always" {
		t.Fatalf("Pick = %s by %q, %v; want the agent without conditions", got.Review, got.Route, err)
	}
}

// TestPickConditions: a condition that fails to evaluate does not hold,
// and one read back from JSON, with no program, is compiled before it is
// judged rather than holding on no globs.
func TestPickConditions(t *testing.T) {
	failing := Filter{Expr: `pr.labels[0].name == "x"`}
	if err := failing.Compile(); err != nil {
		t.Fatal(err)
	}
	m := Models{Review: "a/sonnet", Routes: []ModelRoute{{Model: "a/opus", When: []Filter{failing, {Expr: "pr.draft"}}}}}
	unlabeled := with(SamplePR(), "labels", []any{})
	if got, err := m.Pick(unlabeled, nil); err == nil || got.Review != "a/sonnet" {
		t.Fatalf("Pick = %s, %v; want the default and the evaluation error", got.Review, err)
	}
	if got, err := m.Pick(with(unlabeled, "draft", true), nil); err == nil || got.Review != "a/opus" || got.Route != "pr.draft" {
		t.Fatalf("Pick = %s by %q, %v; want the next condition to pick the route", got.Review, got.Route, err)
	}
	if got, _ := m.Pick(SamplePR(), nil); got.Review != "a/sonnet" {
		t.Fatalf("Pick = %s; an uncompiled pr.draft must not hold for a pull request that is no draft", got.Review)
	}
}

func TestAgentRejects(t *testing.T) {
	t.Setenv("TEST_PRIVATE_KEY", "tok")
	t.Setenv("TEST_WEBHOOK_SECRET", "whsec")
	agents := func(entries string) string {
		return routesProviders + "review:\n  model: a/sonnet\n  agents:\n" + entries + minimal
	}
	for _, tt := range []struct {
		name, doc, want string
	}{
		{"a review.model list", routesProviders + "review: { model: [{ model: a/opus }] }\n" + minimal, "cannot construct !!seq into configfile.ModelRef"},
		{"an agent without a name", agents("    - { model: a/opus }\n"), "review.agents[0] (): name must be"},
		{"the default pass's name", agents("    - { name: default, model: a/opus }\n"), `"default" is the default pass`},
		{"a name twice", agents("    - { name: a, model: a/opus }\n    - { name: a, model: a/opus }\n"), "review.agents[1] (a): name is given twice"},
		{"a replacing agent without a model", agents("    - { name: deep, when: [{ expr: pr.bot }] }\n"), "needs a model"},
		{"a replacing agent with a specialist's key", agents("    - { name: deep, model: a/opus, instructions: Be thorough. }\n"),
			"takes only name, enabled, model, fallback, effort and when"},
		{"an unknown key", agents("    - { name: deep, model: a/opus, wen: x }\n"), "field wen not found"},
		{"a condition with neither expr nor paths", agents("    - { name: deep, model: a/opus, when: [{ name: x }] }\n"),
			"review.agents[0] (deep).when[0]: expr or paths is required"},
		{"a condition that fails the smoke test", agents("    - { name: deep, model: a/opus, when: [{ expr: 'pr.labels[5].name == \"x\"' }] }\n"),
			"review.agents[0] (deep).when[0]: smoke test"},
		{"a model of no provider", agents("    - { name: deep, model: nope/x }\n"), `review.agents[0].model references provider "nope"`},
		{"a fallback of no provider", agents("    - { name: deep, model: a/opus, fallback: nope/x }\n"), `review.agents[0].fallback references provider "nope"`},
		{"an effort that is no level", agents("    - { name: deep, model: a/opus, effort: loud }\n"), "review.agents[0] (deep).effort must be"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := load(t, tt.doc)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}
