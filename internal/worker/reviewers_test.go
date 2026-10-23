package worker

import (
	"log/slog"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/review"
	"github.com/home-operations/kritika/internal/runner"
	"github.com/home-operations/kritika/internal/store"
)

func TestChooseReviewers(t *testing.T) {
	off, steps := false, 5
	configured := map[string]configfile.Reviewer{
		"security": {Paths: []string{"auth/**"}, Rules: []configfile.Rule{{ID: "r", Rule: "R.", When: []configfile.When{{Expr: "pr.draft"}}}},
			Agent: configfile.ReviewerAgent{Steps: &steps}},
		"renovate": {When: []configfile.Filter{{Expr: `pr.headRef.startsWith("renovate/")`}}},
		"retired":  {Enabled: &off},
	}
	// judgeReviewers found renovate's condition not to hold.
	eff := Effective{
		Reviewers: configured, Agent: configfile.AgentSettings{MaxSteps: 30, MaxTokens: 1000, Timeout: time.Minute},
		Skills: configfile.Skills{Paths: []string{".agents/skills"}}, Held: map[string]bool{"security": true, "renovate": false},
		Routes: map[string]string{"security": "deep (auth)"}}
	vars := map[string]any{"headRef": "feature/x", "draft": false}
	names := func(r reviewers) []string {
		out := make([]string, 0, len(r.specs))
		for _, s := range r.specs {
			name := s.Name
			if s.Full {
				name += "(full)"
			}
			out = append(out, name)
		}
		return out
	}
	for _, tt := range []struct {
		name  string
		eff   Effective
		prior priorReview
		only  string
		want  []string
		note  string
	}{
		{name: "the default reviewer and each that is on and holds", eff: eff, want: []string{"default", "security"}},
		{name: "one the last review did not run reviews the whole change", eff: eff,
			prior: priorReview{id: "rev-0", reviewers: []string{"default"}}, want: []string{"default", "security(full)"}},
		{name: "one asked for runs alone", eff: eff, only: "security", want: []string{"security"}},
		{name: "the default one asked for runs alone", eff: eff, only: "default", want: nil},
		{name: "a name no reviewer has runs every one, with a note", eff: eff, only: "please", want: []string{"default", "security"},
			note: "No reviewer named please is on"},
		{name: "none is the default reviewer alone", eff: Effective{}, want: nil},
		{name: "with the default one off and no other that holds, none runs", eff: Effective{Reviewers: map[string]configfile.Reviewer{"default": {Enabled: &off}, "retired": {Enabled: &off}}},
			want: nil},
		{name: "the default one asked for while it is off runs every one, with a note", eff: Effective{Reviewers: map[string]configfile.Reviewer{"default": {Enabled: &off}, "docs": {}}, Held: map[string]bool{"docs": true}},
			only: "default", want: []string{"docs"}, note: "No reviewer named default is on"},
		{name: "a reviewer asked for while it is off runs every one, with a note", eff: eff, only: "retired",
			want: []string{"default", "security"}, note: "No reviewer named retired is on"},
		{name: "the default one off", eff: Effective{
			Reviewers: map[string]configfile.Reviewer{"default": {Enabled: &off}, "docs": {}}, Held: map[string]bool{"docs": true}}, want: []string{"docs"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := chooseReviewers(tt.eff, vars, tt.prior, tt.only, 1000)
			if !slices.Equal(names(got), tt.want) || !strings.Contains(got.note, tt.note) || (tt.note == "") != (got.note == "") {
				t.Fatalf("reviewers = %q, note %q; want %q, note %q", names(got), got.note, tt.want, tt.note)
			}
		})
	}
	offOnly := Effective{Reviewers: map[string]configfile.Reviewer{"default": {Enabled: &off}}}
	if got := chooseReviewers(offOnly, vars, priorReview{}, "", 1000); got.specs != nil || !got.defaultOff {
		t.Errorf("the default reviewer off and no other = %+v, want no specs and the default marked off for the runner to skip", got)
	}
	if got := chooseReviewers(eff, vars, priorReview{}, "", 1000); got.defaultOff ||
		!slices.Contains(got.notes(), "The security reviewer reviewed on its model route deep (auth)") {
		t.Errorf("reviewers = %+v, want the default on and security's route noted", got)
	}
	got := chooseReviewers(eff, vars, priorReview{}, "security", 1000)
	sec := got.specs[0]
	if sec.Paths != nil || len(sec.Rules) != 0 || sec.Limits == nil || sec.Limits.MaxSteps != 5 || sec.Skills == nil {
		t.Errorf("security asked for = %+v, want its paths lifted, its draft rule judged out, its steps and skills", sec)
	}
}

// TestJudgeReviewers: a reviewer's conditions and its model routes are
// judged over the whole pull request, the diff read once when one needs it,
// and its model is then the one it picked, or the default reviewer's.
func TestJudgeReviewers(t *testing.T) {
	high := model.EffortHigh
	off := false
	routes := &configfile.ReviewModel{Default: "p/mid", Routes: []configfile.ModelRoute{
		{Name: "deep", Model: "p/big", Effort: &high, When: []configfile.Filter{{Name: "migrations", Paths: []string{"db/**"}}}},
	}}
	reviewers := map[string]configfile.Reviewer{
		"security": {Model: routes, When: []configfile.Filter{{Name: "auth", Paths: []string{"auth/**"}}}},
		"large":    {When: []configfile.Filter{{Expr: "pr.lines > 100"}}},
		"docs":     {When: []configfile.Filter{{Paths: []string{"**/*.md"}}}},
		"retired":  {Enabled: &off},
	}
	if err := configfile.CheckReviewers(reviewers); err != nil {
		t.Fatal(err)
	}
	eff := Effective{Reviewers: reviewers, Models: configfile.Models{Review: "p/small", Fallback: "p/backup", Effort: model.EffortLow}}
	reads := 0
	diff := "diff --git a/auth/a.go b/auth/a.go\n--- a/auth/a.go\n+++ b/auth/a.go\n@@ -1 +1 @@\n-a\n+b\n" +
		"diff --git a/db/1.sql b/db/1.sql\n--- a/db/1.sql\n+++ b/db/1.sql\n@@ -1 +1 @@\n-a\n+b\n"
	judgeReviewers(slog.New(slog.DiscardHandler), &eff, map[string]any{}, func() (string, error) { reads++; return diff, nil })
	if reads != 1 {
		t.Errorf("the diff was read %d times, want once", reads)
	}
	if want := map[string]bool{"security": true, "large": false, "docs": false}; !maps.Equal(eff.Held, want) {
		t.Errorf("held = %v, want %v", eff.Held, want)
	}
	sec := eff.Reviewers["security"]
	if sec.Model.Default != "p/big" || *sec.Effort != model.EffortHigh || sec.Fallback != "p/backup" || eff.Routes["security"] != "deep (migrations)" {
		t.Errorf("security = %+v %v, route %q; want its deep route", sec.Model, *sec.Effort, eff.Routes["security"])
	}
	if docs := eff.Reviewers["docs"]; docs.Model.Default != "p/small" || *docs.Effort != model.EffortLow || eff.Routes["docs"] != "" {
		t.Errorf("docs = %+v, want the default reviewer's model", docs.Model)
	}
	if reviewers["security"].Model.Default != "p/mid" {
		t.Error("judging changed the configuration's reviewers")
	}
}

func TestRanReviewers(t *testing.T) {
	if got := ranReviewers(nil); !slices.Equal(got, []string{review.DefaultReviewer}) {
		t.Errorf("a whole run ran %q", got)
	}
	parts := []store.AgentPart{{}, {Reviewer: "security", Full: true}, {Reviewer: "security"}, {Reviewer: "default"}}
	if got := ranReviewers(parts); !slices.Equal(got, []string{"default", "security"}) {
		t.Errorf("ran %q", got)
	}
	if got := fullReviewers(parts); !slices.Equal(got, []string{"security"}) {
		t.Errorf("full %q", got)
	}
}

func TestRequestedReviewer(t *testing.T) {
	for body, want := range map[string]string{
		"@kritika review":                                "",
		"@kritika review security":                       "security",
		"@Kritika Review Security please":                "security",
		"@kritika review\nsecurity":                      "",
		"@kritika review gha-security now":               "gha-security",
		"@kritika review?":                               "",
		"@kritika review\n@kritika review security":      "",
		"@kritika review docs\n@kritika review security": "docs",
	} {
		if got := requestedReviewer(body, "kritika"); got != want {
			t.Errorf("requestedReviewer(%q) = %q, want %q", body, got, want)
		}
	}
}

func TestSkipDescriptionNoReviewer(t *testing.T) {
	if got := skipDescription(runner.SkipNoReviewer, ""); got != "no reviewer applies to the change" {
		t.Errorf("description = %q", got)
	}
}
