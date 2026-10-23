package runner

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/home-operations/kritika/internal/agent"
	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/gitfetch"
	"github.com/home-operations/kritika/internal/repoconfig"
	"github.com/home-operations/kritika/internal/review"
)

func TestPlanReviewers(t *testing.T) {
	res := &gitfetch.Result{
		Diff:    sizedDiff("auth/login.go", 2<<10, "web/page.go", 2<<10, "vendor/x.go", 1<<10),
		Changed: []string{"auth/login.go", "web/page.go", "vendor/x.go"}, DeltaDiff: sizedDiff("web/page.go", 1<<10),
	}
	security := Reviewer{Name: "security", Paths: []string{"auth/**"}}
	def := Reviewer{Name: review.DefaultReviewer}
	type share struct {
		reviewer string
		paths    []string
		scope    review.Scope
	}
	for _, tt := range []struct {
		name      string
		reviewers []Reviewer
		scope     review.Scope
		want      []share
		full      bool
	}{
		{name: "no reviewers is the review as it always was"},
		{name: "the default reviewer alone too", reviewers: []Reviewer{def}},
		{name: "the default reviewer the last review did not run reviews the whole change", reviewers: []Reviewer{{Name: def.Name, Full: true}},
			scope: review.ScopeIncremental, full: true},
		{
			name:      "each reviewer that applies gets its files, the default one every one but the ignored",
			reviewers: []Reviewer{def, security, {Name: "docs", Paths: []string{"**/*.md"}}}, scope: review.ScopeFull,
			want: []share{
				{review.DefaultReviewer, []string{"auth/login.go", "web/page.go"}, review.ScopeFull},
				{"security", []string{"auth/login.go"}, review.ScopeFull},
			},
		},
		{name: "an agent alone that does not apply runs the default pass as always", reviewers: []Reviewer{{Name: "docs", Paths: []string{"**/*.md"}}}},
		{
			name:      "on a re-review, a file reverted since the last one is still the reviewer's",
			reviewers: []Reviewer{def, {Name: "web", Paths: []string{"web/**"}}, {Name: "gone", Paths: []string{"old/**"}}},
			scope:     review.ScopeIncremental,
			want: []share{
				{review.DefaultReviewer, []string{"auth/login.go", "web/page.go", "old/x.go"}, review.ScopeIncremental},
				{"web", []string{"web/page.go"}, review.ScopeIncremental},
				{"gone", []string{"old/x.go"}, review.ScopeIncremental},
			},
		},
		{
			name: "a reviewer alone that applies runs in shares", reviewers: []Reviewer{security}, scope: review.ScopeFull,
			want: []share{{"security", []string{"auth/login.go"}, review.ScopeFull}},
		},
		{
			name:      "on a re-review, a reviewer none of whose files changed since still runs, judged over the whole change",
			reviewers: []Reviewer{def, security}, scope: review.ScopeIncremental,
			want: []share{
				{review.DefaultReviewer, []string{"auth/login.go", "web/page.go", "old/x.go"}, review.ScopeIncremental},
				{"security", []string{"auth/login.go"}, review.ScopeIncremental},
			},
		},
		{
			name:      "on a re-review, a reviewer the last review did not run reviews its files whole",
			reviewers: []Reviewer{def, {Name: "security", Paths: security.Paths, Full: true}}, scope: review.ScopeIncremental,
			want: []share{
				{review.DefaultReviewer, []string{"auth/login.go", "web/page.go", "old/x.go"}, review.ScopeIncremental},
				{"security", []string{"auth/login.go"}, review.ScopeFull},
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := reviewSpec()
			s.Prompt.Reviewers = tt.reviewers
			deltaPaths := []string{"web/page.go", "old/x.go"}
			plan := planReviewers(s, res, tt.scope, deltaPaths)
			if plan.full != tt.full {
				t.Fatalf("plan = %+v, want full %v", plan, tt.full)
			}
			var got []share
			for _, sh := range plan.shares {
				got = append(got, share{sh.name(), sh.paths, sh.scope})
			}
			if len(got) != len(tt.want) {
				t.Fatalf("shares = %+v, want %+v", got, tt.want)
			}
			for i := range got {
				if got[i].reviewer != tt.want[i].reviewer || !slices.Equal(got[i].paths, tt.want[i].paths) || got[i].scope != tt.want[i].scope {
					t.Errorf("share %d = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// TestPlanReviewersSplit: each reviewer is cut into its share of the parts
// the run is granted.
func TestPlanReviewersSplit(t *testing.T) {
	s := reviewSpec()
	s.Agent.Parts = 4
	s.Prompt.Reviewers = []Reviewer{{Name: review.DefaultReviewer}, {Name: "security"}}
	res := &gitfetch.Result{Diff: sizedDiff("a/x.go", 50<<10, "b/y.go", 50<<10), Changed: []string{"a/x.go", "b/y.go"}}
	plan := planReviewers(s, res, review.ScopeFull, nil)
	if len(plan.shares) != 4 {
		t.Fatalf("shares = %d, want two parts for each of two reviewers", len(plan.shares))
	}
	for i, sh := range plan.shares {
		if sh.count != 2 || sh.index != i%2+1 {
			t.Errorf("share %d is part %d of %d of %s", i, sh.index, sh.count, sh.name())
		}
	}
	if notes := reviewerNotes(plan.shares); len(notes) != 2 || !strings.Contains(notes[0], "The default pass reviewed in 2 parts") || !strings.Contains(notes[1], "The security agent reviewed in 2 parts") {
		t.Errorf("notes = %q", notes)
	}
}

// TestPartPromptsReviewers: a reviewer's part is given its role, its rules
// rather than the default reviewer's, the last review's findings it
// raised, a schema of its categories, and a check that refuses a finding
// outside them or its files.
func TestPartPromptsReviewers(t *testing.T) {
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"indexed":false}`))
	}))
	t.Cleanup(gw.Close)
	s := reviewSpec()
	s.Model.GatewayURL, s.PriorHead = gw.URL, shaB
	s.Prompt.Rules = []configfile.Rule{{ID: "house-rule", Rule: "Mind the house."}}
	s.Prompt.Prior = []review.Finding{
		{Path: "auth/login.go", Line: 1, Severity: review.SeverityP1, Title: "default's earlier finding"},
		{Path: "auth/login.go", Line: 2, Severity: review.SeverityP1, Title: "security's earlier finding", Agents: []string{"security"}},
	}
	s.Prompt.Reviewers = []Reviewer{
		{Name: review.DefaultReviewer},
		{Name: "security", File: "docs/security.md", Categories: []review.Category{review.CategorySecurity}, Paths: []string{"auth/**"},
			Rules: []configfile.Rule{{ID: "no-secrets", Rule: "No secrets."}}, Limits: &ReviewerLimits{MaxSteps: 5}},
	}
	head := tree(t, map[string]string{"auth/login.go": "package auth\n", "web/page.go": "package web\n"})
	res := &gitfetch.Result{Diff: sizedDiff("auth/login.go", 1<<10, "web/page.go", 1<<10), Changed: []string{"auth/login.go", "web/page.go"}}
	plan := planReviewers(s, res, review.ScopeFull, nil)
	var tools agentTools
	files := repoconfig.Files{"docs/security.md": "Trace every input to a sink."}
	parts, _, rules, err := partPrompts(t.Context(), s, Secrets{GatewayToken: "t"}, head, head, res, files, nil, plan.shares, &tools,
		slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 2 || !slices.Equal(rules, []string{"house-rule", "no-secrets"}) {
		t.Fatalf("%d parts with rules %q, want two with each reviewer's", len(parts), rules)
	}
	def, sec := parts[0], parts[1]
	if def.reviewer != review.DefaultReviewer || strings.Contains(def.prompt.system, "## Your role") ||
		!strings.Contains(def.prompt.system, "house-rule") || !strings.Contains(def.prompt.user, "you review it as the default reviewer") ||
		!strings.Contains(def.prompt.user, "default's earlier finding") || strings.Contains(def.prompt.user, "security's earlier finding") {
		t.Errorf("the default reviewer's prompt:\n%s\n%s", def.prompt.system, def.prompt.user)
	}
	if sec.reviewer != "security" || !strings.Contains(sec.prompt.system, "you are the security reviewer") ||
		!strings.Contains(sec.prompt.system, "Trace every input to a sink.") || strings.Contains(sec.prompt.system, "house-rule") ||
		!strings.Contains(sec.prompt.system, "no-secrets") || !strings.Contains(sec.prompt.user, "security's earlier finding") ||
		strings.Contains(sec.prompt.user, "default's earlier finding") {
		t.Errorf("the security reviewer's prompt:\n%s\n%s", sec.prompt.system, sec.prompt.user)
	}
	if !strings.Contains(string(sec.prompt.submit.InputSchema), `"enum":["security"]`) {
		t.Errorf("the security reviewer's schema is not its categories': %s", sec.prompt.submit.InputSchema)
	}
	if sec.agent.MaxSteps != 5 || def.agent.MaxSteps != s.Agent.MaxSteps {
		t.Errorf("steps = %d and %d, want the reviewer's own and the run's", sec.agent.MaxSteps, def.agent.MaxSteps)
	}
	finding := func(path, category string) json.RawMessage {
		return json.RawMessage(`{"summary":{"headline":"H","take":"T.","praise":[]},"findings":[{"path":"` + path + `","line":1,` +
			`"severity":"p2","category":"` + category + `","title":"t","explanation":"e"}]}`)
	}
	if err := sec.prompt.validate(finding("auth/login.go", "security")); err != nil {
		t.Errorf("the security reviewer refused its own finding: %v", err)
	}
	if err := sec.prompt.validate(finding("auth/login.go", "tests")); err == nil {
		t.Error("the security reviewer took a tests finding")
	}
	if err := sec.prompt.validate(finding("web/page.go", "security")); err == nil {
		t.Error("the security reviewer took a finding on a file it does not review")
	}
	if err := def.prompt.validate(finding("web/page.go", "tests")); err != nil {
		t.Errorf("the default reviewer refused a finding: %v", err)
	}
}

// TestMergePartsReviewers: a reviewer's part's findings name it, whatever
// they said, and the part's record names it too.
func TestMergePartsReviewers(t *testing.T) {
	parts := []reviewPart{{paths: []string{"a.go"}, reviewer: review.DefaultReviewer}, {paths: []string{"a.go"}, reviewer: "security", full: true}}
	sub := json.RawMessage(`{"summary":{"headline":"H","take":"T.","praise":[]},"findings":[{"path":"a.go","line":1,"severity":"p2",` +
		`"category":"security","title":"t","explanation":"e","agents":["ghost"]}]}`)
	results := []agent.Result{{Stop: agent.StopSubmitted, Submitted: sub}, {Stop: agent.StopSubmitted, Submitted: sub}}
	merged, records, err := mergeParts(parts, results, Secrets{})
	if err != nil {
		t.Fatal(err)
	}
	var res review.Result
	if err := json.Unmarshal(merged.Submitted, &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Findings) != 2 || !slices.Equal(res.Findings[0].Agents, []string{"default"}) ||
		!slices.Equal(res.Findings[1].Agents, []string{"security"}) {
		t.Errorf("findings = %+v, want each naming its part's reviewer", res.Findings)
	}
	if records[1].Reviewer != "security" || !records[1].Full || records[0].Full {
		t.Errorf("records = %+v", records)
	}
}

func TestSpecReviewersValidate(t *testing.T) {
	s := reviewSpec()
	s.Prompt.Reviewers = []Reviewer{{Name: "security"}, {Name: "security"}}
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "listed twice") {
		t.Errorf("a reviewer listed twice = %v", err)
	}
	s.Prompt.Reviewers = []Reviewer{{Name: ""}}
	if err := s.Validate(); err == nil {
		t.Error("an unnamed reviewer was accepted")
	}
}

func TestFollowUpRole(t *testing.T) {
	s := followUpSpec()
	if followUpRole(s, nil) != nil {
		t.Error("a follow-up with no reviewer has a role")
	}
	s.Prompt.Reviewers = []Reviewer{{Name: "security", Instructions: "Exploits.", Categories: []review.Category{review.CategorySecurity}}}
	role := followUpRole(s, nil)
	if role == nil || role.Name != "security" || role.Instructions != "Exploits." || role.Categories != nil {
		t.Errorf("role = %+v, want the reviewer's, with no categories to report", role)
	}
	if followUpReviewer(reviewSpec()) != "" {
		t.Error("a review's run names a follow-up reviewer")
	}
}

func TestDefaultReviewPriorOwnership(t *testing.T) {
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"indexed":false}`))
	}))
	t.Cleanup(gw.Close)
	head := tree(t, map[string]string{"main.go": "package main\n"})
	for _, scope := range []review.Scope{review.ScopeFull, review.ScopeIncremental} {
		for _, split := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/split=%t", scope, split), func(t *testing.T) {
				s := reviewSpec()
				s.Model.GatewayURL, s.PriorHead = gw.URL, shaB
				s.Prompt.Prior = []review.Finding{
					{Path: "main.go", Line: 1, Severity: review.SeverityP1, Title: "default finding"},
					{Path: "main.go", Line: 1, Severity: review.SeverityP1, Title: "lens finding", Agents: []string{"security"}},
					{Path: "main.go", Line: 1, Severity: review.SeverityP1, Title: "shared finding", Agents: []string{"security", review.DefaultReviewer}},
				}
				res := &gitfetch.Result{Diff: agentDiff, DeltaDiff: agentDiff, Changed: []string{"main.go"}}
				var tools agentTools
				logger := slog.New(slog.DiscardHandler)
				var prompt agentPrompt
				if split {
					parts, _, _, err := partPrompts(t.Context(), s, Secrets{}, head, head, res, nil, nil,
						splitShares([][]string{{"main.go"}}, scope), &tools, logger)
					if err != nil {
						t.Fatal(err)
					}
					prompt = parts[0].prompt
				} else {
					prompt, _ = wholePrompt(t.Context(), s, Secrets{}, head, nil, res,
						newPromptInputs(s, nil, nil, res.Changed), scope, nil, &tools, logger)
				}
				if strings.Contains(prompt.user, "lens finding") || !strings.Contains(prompt.user, "default finding") ||
					!strings.Contains(prompt.user, "shared finding") {
					t.Fatalf("default prompt must recheck only findings it raised:\n%s", prompt.user)
				}
				if len(s.Prompt.Prior) != 3 {
					t.Fatal("building the prompt changed the prior findings used for publication")
				}
			})
		}
	}
}

// TestRoleWithinBudget: a reviewer's instructions are part of the system
// prompt the user message is budgeted against, so long ones leave less of
// the diff in it rather than passing the opening prompt's bound.
func TestRoleWithinBudget(t *testing.T) {
	s := reviewSpec()
	s.Agent.MaxPromptTokens = 12_000
	files, changed := make([]any, 0, 80), make([]string, 0, 40)
	for i := range 40 {
		name := fmt.Sprintf("f%02d.go", i)
		files, changed = append(files, name, 2<<10), append(changed, name)
	}
	pack := packView{Diff: sizedDiff(files...), Changed: changed}
	in := newPromptInputs(s, nil, nil, pack.Changed)
	plain := newAgentPrompt(s, in, pack, nil, false, false)
	in.role = &review.Reviewer{Name: "security", Instructions: strings.Repeat("Trace every input. ", 1000)}
	roled := newAgentPrompt(s, in, pack, nil, false, false)
	if !strings.Contains(roled.system, "Trace every input.") {
		t.Fatal("the role is not in the system prompt")
	}
	if len(roled.user) >= len(plain.user) || len(roled.omitted) <= len(plain.omitted) {
		t.Errorf("with the role, user %d leaving out %d files; without, %d leaving out %d: the role must displace the diff",
			len(roled.user), len(roled.omitted), len(plain.user), len(plain.omitted))
	}
}
