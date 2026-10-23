package configfile

import (
	"strings"
	"testing"
	"time"

	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/review"
)

// reviewersProvider is minimal with an openai provider p a reviewer's model
// may name.
const reviewersProvider = "providers:\n  p:\n    type: openai\n    apiKey: { env: TEST_WEBHOOK_SECRET }\n"

func TestReviewersResolve(t *testing.T) {
	t.Setenv("TEST_PRIVATE_KEY", "tok")
	t.Setenv("TEST_WEBHOOK_SECRET", "whsec")
	f := mustLoad(t, reviewersProvider+`reviewers:
  security: { instructions: Look for exploits., categories: [security], model: p/big, paths: ['**/auth/**'] }
  default: { enabled: false }
`+minimal)
	s := f.Settings(&f.Accounts[0])
	if got := s.Reviewers["security"]; got.Model.Default != "p/big" || got.Categories[0] != review.CategorySecurity || !got.On() {
		t.Errorf("security = %+v, want the file's", got)
	}
	if DefaultOn(s.Reviewers) {
		t.Error("the default reviewer is on, though the file turned it off")
	}
	if !DefaultOn(mustLoad(t, minimal).Settings(&f.Accounts[0]).Reviewers) || !DefaultOn(nil) {
		t.Error("the default reviewer is off where nothing turned it off")
	}
}

func TestReviewersInvalid(t *testing.T) {
	t.Setenv("TEST_PRIVATE_KEY", "tok")
	t.Setenv("TEST_WEBHOOK_SECRET", "whsec")
	for _, tt := range []struct {
		name, reviewers, want string
	}{
		{"a name that is not one", `{ Sec: { instructions: x } }`, "reviewers.Sec: a name must be"},
		{"the default reviewer retuned", `{ default: { instructions: x } }`, "only enabled may be set"},
		{"instructions twice", `{ sec: { instructions: x, file: docs/sec.md } }`, "set one of instructions or file"},
		{"a file outside", `{ sec: { file: ../sec.md } }`, "reviewers.sec.file"},
		{"a category that is none", `{ sec: { categories: [secrets] } }`, `categories[0] "secrets" is not one of`},
		{"a category twice", `{ sec: { categories: [security, security] } }`, "listed twice"},
		{"a glob that is none", `{ sec: { paths: ['a/[b'] } }`, "is not a valid glob"},
		{"skill paths", `{ sec: { skills: { paths: [.x] } } }`, "skills.paths"},
		{"a rule that is none", `{ sec: { rules: [{ id: a }] } }`, "set one of rule or file"},
		{"a model of no provider", `{ sec: { model: q/x } }`, "reviewers.sec.model references provider \"q\""},
		{"a route's model of no provider", `{ sec: { model: [{ model: q/x, when: [{ paths: [a/**] }] }, { model: p/a }] } }`,
			"reviewers.sec.model[0].model references provider \"q\""},
		{"a route without conditions", `{ sec: { model: [{ model: p/b }, { model: p/a }] } }`, "[0]: when is required"},
		{"a route effort that is none", `{ sec: { model: [{ model: p/b, effort: huge, when: [{ expr: pr.draft }] }, { model: p/a }] } }`,
			"reviewers.sec.model[0].effort must be"},
		{"a condition with neither", `{ sec: { when: [{ name: x }] } }`, "reviewers.sec.when[0]: expr or paths is required"},
		{"a condition name twice", `{ sec: { when: [{ name: x, expr: pr.draft }, { name: x, paths: [a/**] }] } }`, "given twice"},
		{"an effort that is none", `{ sec: { effort: huge } }`, "effort must be"},
		{"a timeout too long", `{ sec: { agent: { timeout: 100h } } }`, "agent.timeout must be at least 1s"},
		{"a timeout under a second", `{ sec: { agent: { timeout: 500ms } } }`, "agent.timeout must be at least 1s"},
		{"steps that are none", `{ sec: { agent: { steps: 0 } } }`, "agent.steps must be positive"},
		{"too many", `{ a: {}, b: {}, c: {}, d: {}, e: {}, f: {}, g: {}, h: {}, i: {} }`, "9 reviewers besides the default one"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := load(t, reviewersProvider+"reviewers: "+tt.reviewers+"\n"+minimal)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to say %q", err, tt.want)
			}
		})
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
	reviewers := map[string]Reviewer{
		"any":   {},
		"auth":  {When: []Filter{{Name: "auth", Paths: []string{"auth/**"}}}},
		"large": {When: []Filter{{Expr: "pr.lines > 100"}}},
		"bot":   {When: []Filter{{Expr: "pr.bot"}, {Paths: []string{"**/*.md"}}}},
	}
	if err := CheckReviewers(reviewers); err != nil {
		t.Fatal(err)
	}
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
	got := Reviewer{Model: &ReviewModel{Default: "p/big", Routes: []ModelRoute{{Model: "p/huge"}}}, Effort: &high}.Models(picked)
	if got.Review != "p/big" || got.Fallback != "p/backup" || got.Effort != high || got.Route != "" || len(got.Routes) != 1 {
		t.Errorf("models = %+v, want the reviewer's model, routes and effort over the default reviewer's fallback", got)
	}
	got = Reviewer{}.Models(picked)
	if got.Review != "p/small" || got.Route != "deep (migrations)" || got.Routes != nil {
		t.Errorf("models = %+v, want the default reviewer's as picked, with nothing left to pick", got)
	}
}
