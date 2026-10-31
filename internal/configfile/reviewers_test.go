package configfile

import (
	"strings"
	"testing"
	"time"

	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/review"
)

// reviewersProvider is minimal with an openai provider p that a reviewer's
// model can name.
const reviewersProvider = "providers:\n  p:\n    type: openai\n    apiKey: { env: TEST_WEBHOOK_SECRET }\n"

func TestAgentsResolve(t *testing.T) {
	t.Setenv("TEST_PRIVATE_KEY", "tok")
	t.Setenv("TEST_WEBHOOK_SECRET", "whsec")
	f := mustLoad(t, reviewersProvider+`review:
  agents:
    - { name: security, instructions: Look for exploits., categories: [security], model: p/big, paths: ['**/auth/**'] }
    - { name: retired, categories: [tests], enabled: false }
`+minimal)
	s := f.Settings(&f.Accounts[0])
	if got := s.Reviewers["security"]; got.Model != "p/big" || got.Categories[0] != review.CategorySecurity || !got.On() {
		t.Errorf("security = %+v, want the file's", got)
	}
	if got := s.Reviewers["retired"]; got.On() || len(s.Models.Routes) != 0 {
		t.Errorf("retired = %+v, routes %+v; want a specialist that is off, and no routes", got, s.Models.Routes)
	}
}

func TestAgentsInvalid(t *testing.T) {
	t.Setenv("TEST_PRIVATE_KEY", "tok")
	t.Setenv("TEST_WEBHOOK_SECRET", "whsec")
	for _, tt := range []struct {
		name, agent, want string
	}{
		{"a name that is not one", `{ name: Sec, categories: [security] }`, "review.agents[0] (Sec): name must be"},
		{"instructions twice", `{ name: sec, categories: [security], instructions: x, file: docs/sec.md }`, "set one of instructions or file"},
		{"a file outside", `{ name: sec, categories: [security], file: ../sec.md }`, "(sec).file"},
		{"a category that is none", `{ name: sec, categories: [secrets] }`, `categories[0] "secrets" is not one of`},
		{"a category twice", `{ name: sec, categories: [security, security] }`, "listed twice"},
		{"a glob that is none", `{ name: sec, categories: [security], paths: ['a/[b'] }`, "is not a valid glob"},
		{"skill paths", `{ name: sec, categories: [security], skills: { paths: [.x] } }`, "skills.paths"},
		{"a rule that is none", `{ name: sec, categories: [security], rules: [{ id: a }] }`, "set one of rule or file"},
		{"a model of no provider", `{ name: sec, categories: [security], model: q/x }`, `review.agents[0].model references provider "q"`},
		{"a condition with neither", `{ name: sec, categories: [security], when: [{ name: x }] }`, "(sec).when[0]: expr or paths is required"},
		{"a condition name twice", `{ name: sec, categories: [security], when: [{ name: x, expr: pr.draft }, { name: x, paths: [a/**] }] }`,
			"given twice"},
		{"an effort that is none", `{ name: sec, categories: [security], effort: huge }`, "effort must be"},
		{"a timeout too long", `{ name: sec, categories: [security], agent: { timeout: 100h } }`, "agent.timeout must be at least 1s"},
		{"a timeout under a second", `{ name: sec, categories: [security], agent: { timeout: 500ms } }`, "agent.timeout must be at least 1s"},
		{"steps that are none", `{ name: sec, categories: [security], agent: { steps: 0 } }`, "agent.steps must be positive"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := load(t, reviewersProvider+"review: { agents: ["+tt.agent+"] }\n"+minimal)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to say %q", err, tt.want)
			}
		})
	}
	many := make([]string, 0, 9)
	for _, n := range "abcdefghi" {
		many = append(many, "{ name: "+string(n)+", categories: [tests] }")
	}
	if _, err := load(t, reviewersProvider+"review: { agents: ["+strings.Join(many, ", ")+"] }\n"+minimal); err == nil ||
		!strings.Contains(err.Error(), "9 agents are over the 8 allowed") {
		t.Fatalf("nine agents = %v", err)
	}
}

func TestReviewerBounds(t *testing.T) {
	steps, tokens, timeout := 50, int64(10), time.Hour
	r := Reviewer{Agent: ReviewerAgent{Steps: &steps, Tokens: &tokens, Timeout: &timeout}}
	got := r.Bounds(AgentSettings{MaxSteps: 30, MaxTokens: 100, Timeout: time.Minute})
	if got.MaxSteps != 30 || got.MaxTokens != 10 || got.Timeout != time.Minute {
		t.Fatalf("bounds = %+v, want each the lower of the two", got)
	}
}

func TestReviewerFiles(t *testing.T) {
	got := ReviewerFiles(map[string]Reviewer{
		"b": {File: "docs/b.md", Rules: []Rule{{ID: "x", File: "docs/rule.md"}, {ID: "y", Rule: "Y"}}},
		"a": {File: "docs/rule.md"},
	})
	if strings.Join(got, ",") != "docs/rule.md,docs/b.md" {
		t.Fatalf("files = %q, want docs/rule.md then docs/b.md, each once, in the order of the reviewers' names", got)
	}
}

// TestReviewerHolds: a reviewer's conditions take the shape of a model
// route's, judged over the whole pull request: its expressions, its paths
// and pr.lines, the last two only once the diff is read.
func TestReviewerHolds(t *testing.T) {
	agents := []Reviewer{
		{Name: "any", Categories: []review.Category{review.CategoryTests}},
		{Name: "auth", Categories: []review.Category{review.CategorySecurity}, When: []Filter{{Name: "auth", Paths: []string{"auth/**"}}}},
		{Name: "large", Categories: []review.Category{review.CategoryTests}, When: []Filter{{Expr: "pr.lines > 100"}}},
		{Name: "bot", Categories: []review.Category{review.CategoryTests}, When: []Filter{{Expr: "pr.bot"}, {Paths: []string{"**/*.md"}}}},
	}
	if err := CheckAgents(agents); err != nil {
		t.Fatal(err)
	}
	reviewers, _ := SplitAgents(agents)
	vars := SamplePR()
	vars["bot"] = false
	d := &Diff{Lines: 150, Changed: []string{"auth/login.go"}}
	for _, tt := range []struct {
		name      string
		d         *Diff
		want      bool
		needsDiff bool
	}{
		{"any", nil, true, false}, {"auth", d, true, true}, {"auth", nil, false, true}, {"auth", &Diff{Changed: []string{"web/a.go"}}, false, true},
		{"large", d, true, true}, {"large", &Diff{Lines: 10}, false, true}, {"bot", d, false, true},
	} {
		r := reviewers[tt.name]
		if got, err := r.Holds(vars, tt.d); err != nil || got != tt.want {
			t.Errorf("%s over %+v = %v, %v; want %v", tt.name, tt.d, got, err, tt.want)
		}
		if r.NeedsDiff() != tt.needsDiff {
			t.Errorf("%s needs the diff = %v", tt.name, !tt.needsDiff)
		}
	}
}

func TestReviewerModels(t *testing.T) {
	high := model.EffortHigh
	picked := Models{Review: "p/small", Fallback: "p/backup", Effort: model.EffortLow, Route: "deep (migrations)",
		Routes: []ModelRoute{{Model: "p/x"}}}
	got := Reviewer{Model: "p/big", Effort: &high}.Models(picked)
	if got.Review != "p/big" || got.Fallback != "p/backup" || got.Effort != high || got.Route != "" || got.Routes != nil {
		t.Errorf("models = %+v, want the agent's model and effort over the default pass's fallback", got)
	}
	got = Reviewer{}.Models(picked)
	if got.Review != "p/small" || got.Route != "deep (migrations)" || got.Routes != nil {
		t.Errorf("models = %+v, want the default pass's as picked, with nothing left to pick", got)
	}
}
