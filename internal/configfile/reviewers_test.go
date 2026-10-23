package configfile

import (
	"strings"
	"testing"
	"time"

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
	if got := s.Reviewers["security"]; got.Model != "p/big" || got.Categories[0] != review.CategorySecurity || !got.On() {
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
