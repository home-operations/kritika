package worker

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/repoconfig"
	"github.com/home-operations/kritika/internal/review"
	"github.com/home-operations/kritika/internal/runner"
	"github.com/home-operations/kritika/internal/store"
)

func TestChooseReviewers(t *testing.T) {
	off, steps := false, 5
	configured := map[string]configfile.Reviewer{
		"security": {Paths: []string{"auth/**"}, Rules: []configfile.Rule{{ID: "r", Rule: "R.", When: []configfile.When{{Expr: "pr.draft"}}}},
			Agent: configfile.ReviewerAgent{Steps: &steps}},
		"renovate": {When: []configfile.When{{Expr: `pr.headRef.startsWith("renovate/")`}}},
		"retired":  {Enabled: &off},
	}
	eff := Effective{
		Reviewers: configured, Agent: configfile.AgentSettings{MaxSteps: 30, MaxTokens: 1000, Timeout: time.Minute},
		Skills: configfile.Skills{Paths: []string{".agents/skills"}}}
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
			note: "No reviewer is named please"},
		{name: "none is the default reviewer alone", eff: Effective{}, want: nil},
		{name: "the default one off", eff: Effective{Merged: repoconfig.Merged{Settings: configfile.Settings{
			Reviewers: map[string]configfile.Reviewer{"default": {Enabled: &off}, "docs": {}}}}}, want: []string{"docs"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := chooseReviewers(tt.eff, vars, tt.prior, tt.only, 1000)
			if !slices.Equal(names(got), tt.want) || !strings.Contains(got.note, tt.note) || (tt.note == "") != (got.note == "") {
				t.Fatalf("reviewers = %q, note %q; want %q, note %q", names(got), got.note, tt.want, tt.note)
			}
		})
	}
	got := chooseReviewers(eff, vars, priorReview{}, "security", 1000)
	sec := got.specs[0]
	if sec.Paths != nil || len(sec.Rules) != 0 || sec.Limits == nil || sec.Limits.MaxSteps != 5 || sec.Skills == nil {
		t.Errorf("security asked for = %+v, want its paths lifted, its draft rule judged out, its steps and skills", sec)
	}
}

func TestReviewerModels(t *testing.T) {
	low := configfile.Models{Review: "p/small", Fallback: "p/backup", Effort: model.EffortLow}
	high := model.EffortHigh
	got := reviewerModels(configfile.Reviewer{Model: "p/big", Effort: &high}, low)
	if got.Review != "p/big" || got.Fallback != "p/backup" || got.Effort != model.EffortHigh {
		t.Errorf("models = %+v, want the reviewer's model and effort over the repository's fallback", got)
	}
	if got := reviewerModels(configfile.Reviewer{}, low); !reflect.DeepEqual(got, low) {
		t.Errorf("models = %+v, want the repository's", got)
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
		"@kritika review":                  "",
		"@kritika review security":         "security",
		"@Kritika Review Security please":  "security",
		"@kritika review\nsecurity":        "",
		"@kritika review gha-security now": "gha-security",
		"@kritika review?":                 "",
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
