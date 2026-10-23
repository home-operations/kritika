package configfile

import (
	"slices"
	"strings"
	"testing"
)

// routesProviders declares the providers the routes' models name.
const routesProviders = `providers:
  a: { type: anthropic, apiKey: { env: TEST_WEBHOOK_SECRET } }
  o: { type: openai, apiKey: { env: TEST_WEBHOOK_SECRET } }
`

// routes is a review.model list of two routes and the default.
const routes = `review:
  effort: medium
  model:
    - name: deep
      model: a/opus
      effort: high
      fallback: o/big
      when:
        - { name: migrations, paths: ["db/migrations/**"] }
        - { name: large, expr: pr.lines > 800 }
    - model: o/small
      when: [{ expr: pr.bot }]
    - model: a/sonnet
`

func TestReviewModelRoutes(t *testing.T) {
	t.Setenv("TEST_PRIVATE_KEY", "tok")
	t.Setenv("TEST_WEBHOOK_SECRET", "whsec")
	f := mustLoad(t, routesProviders+routes+minimal)
	a, _ := f.Account(ForgeGitHub, "acme")
	m := f.Settings(a).Models
	if m.Review != "a/sonnet" || m.Effort != "medium" || m.Fallback != "" || len(m.Routes) != 2 {
		t.Fatalf("models = %+v; the last entry is the default", m)
	}
	if got := f.FileLayer().Review.Value; got != "a/sonnet, routes deep, o/small" {
		t.Fatalf("FileLayer review = %q", got)
	}
	if !slices.ContainsFunc(f.UnpricedModels(), func(u UnpricedModel) bool { return u.Setting == "review.model[0].fallback" && u.Ref == "o/big" }) {
		t.Fatalf("UnpricedModels = %+v, want the routes' models listed", f.UnpricedModels())
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
		{"the first route that holds wins", bot, &Diff{Lines: 900, Changed: []string{"main.go"}}, "a/opus", "high", "o/big", "deep (large)"},
		{"a route that sets no effort or fallback keeps the default's", bot, &Diff{Lines: 3, Changed: []string{"go.mod"}},
			"o/small", "medium", "", "pr.bot"},
		{"without the diff, paths and lines do not hold", SamplePR(), nil, "a/sonnet", "medium", "", ""},
		{"no route holds", SamplePR(), &Diff{Lines: 3, Changed: []string{"main.go"}}, "a/sonnet", "medium", "", ""},
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

func TestReviewModelRejects(t *testing.T) {
	t.Setenv("TEST_PRIVATE_KEY", "tok")
	t.Setenv("TEST_WEBHOOK_SECRET", "whsec")
	model := func(entries string) string { return routesProviders + "review:\n  model:\n" + entries + minimal }
	for _, tt := range []struct {
		name, doc, want string
	}{
		{"an empty list", routesProviders + "review: { model: [] }\n" + minimal, "a list must end with the default model"},
		{"a last entry with conditions", model("    - { model: a/opus, when: [{ expr: pr.bot }] }\n"), "[0]: the last entry is the default model"},
		{"a last entry with an effort", model("    - { model: a/opus, effort: high }\n"), "[0]: the last entry is the default model"},
		{"a route without conditions", model("    - { model: a/opus }\n    - { model: a/sonnet }\n"), "[0]: when is required"},
		{"an entry without a model", model("    - { when: [{ expr: pr.bot }] }\n    - { model: a/sonnet }\n"), "[0]: model is required"},
		{"an unknown key", model("    - { model: a/opus, when: [{ expr: pr.bot }], wen: x }\n    - { model: a/sonnet }\n"), "field wen not found"},
		{"a condition with neither expr nor paths", model("    - { model: a/opus, when: [{ name: x }] }\n    - { model: a/sonnet }\n"),
			"review.model[0].when[0]: expr or paths is required"},
		{"a condition that fails the smoke test", model("    - { model: a/opus, when: [{ expr: 'pr.labels[5].name == \"x\"' }] }\n    - { model: a/sonnet }\n"),
			"review.model[0].when[0]: smoke test"},
		{"a route's model of no provider", model("    - { model: nope/x, when: [{ expr: pr.bot }] }\n    - { model: a/sonnet }\n"),
			`review.model[0].model references provider "nope"`},
		{"a route's fallback of no provider", model("    - { model: a/opus, fallback: nope/x, when: [{ expr: pr.bot }] }\n    - { model: a/sonnet }\n"),
			`review.model[0].fallback references provider "nope"`},
		{"a route's effort that is no level", model("    - { model: a/opus, effort: loud, when: [{ expr: pr.bot }] }\n    - { model: a/sonnet }\n"),
			"review.model[0].effort must be"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := load(t, tt.doc)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}
