package worker

import (
	"log/slog"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/jobs"
	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/review"
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
		Models: configfile.Models{Routes: []configfile.ModelRoute{{Name: "deep", Model: "p/big"}}}}
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
		{name: "the default pass and each specialist that is on and holds", eff: eff, want: []string{"default", "security"}},
		{name: "one the last review did not run reviews the whole change", eff: eff,
			prior: priorReview{id: "rev-0", reviewers: []string{"default"}}, want: []string{"default", "security(full)"}},
		{name: "a specialist asked for runs alone", eff: eff, only: "security", want: []string{"security"}},
		{name: "the default pass asked for runs alone", eff: eff, only: "default", want: nil},
		{name: "an agent that replaces the default pass asked for runs it alone", eff: eff, only: "deep", want: nil},
		{name: "a name no agent has runs every one, with a note", eff: eff, only: "please", want: []string{"default", "security"},
			note: "No agent named please is on"},
		{name: "a specialist asked for while it is off runs every one, with a note", eff: eff, only: "retired",
			want: []string{"default", "security"}, note: "No agent named retired is on"},
		{name: "none is the default pass alone", eff: Effective{}, want: nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := chooseReviewers(tt.eff, vars, tt.prior, tt.only, 1000)
			if !slices.Equal(names(got), tt.want) || !strings.Contains(got.note, tt.note) || (tt.note == "") != (got.note == "") {
				t.Fatalf("agents = %q, note %q; want %q, note %q", names(got), got.note, tt.want, tt.note)
			}
		})
	}
	got := chooseReviewers(eff, vars, priorReview{}, "security", 1000)
	sec := got.specs[0]
	if sec.Paths != nil || len(sec.Rules) != 0 || sec.Limits == nil || sec.Limits.MaxSteps != 5 || sec.Skills == nil {
		t.Errorf("security asked for = %+v, want its paths lifted, its draft rule judged out, its steps and skills", sec)
	}
}

// TestJudgeReviewers: a specialist's conditions are judged over the whole
// pull request, the diff read once when one needs it, and its models are
// its own, or the default pass's as the agents picked them.
func TestJudgeReviewers(t *testing.T) {
	high := model.EffortHigh
	off := false
	agents := []configfile.Reviewer{
		{Name: "security", Categories: []review.Category{review.CategorySecurity}, Model: "p/big", Effort: &high,
			When: []configfile.Filter{{Name: "auth", Paths: []string{"auth/**"}}}},
		{Name: "large", Categories: []review.Category{review.CategoryTests}, When: []configfile.Filter{{Expr: "pr.lines > 100"}}},
		{Name: "docs", Categories: []review.Category{review.CategoryTests}, When: []configfile.Filter{{Paths: []string{"**/*.md"}}}},
		{Name: "retired", Categories: []review.Category{review.CategoryTests}, Enabled: &off},
	}
	if err := configfile.CheckAgents(agents); err != nil {
		t.Fatal(err)
	}
	reviewers, _ := configfile.SplitAgents(agents)
	eff := Effective{Reviewers: reviewers, Models: configfile.Models{Review: "p/small", Fallback: "p/backup", Effort: model.EffortLow}}
	reads := 0
	diff := "diff --git a/auth/a.go b/auth/a.go\n--- a/auth/a.go\n+++ b/auth/a.go\n@@ -1 +1 @@\n-a\n+b\n"
	judgeReviewers(slog.New(slog.DiscardHandler), &eff, map[string]any{}, func() (string, error) { reads++; return diff, nil })
	if reads != 1 {
		t.Errorf("the diff was read %d times, want once", reads)
	}
	if want := map[string]bool{"security": true, "large": false, "docs": false}; !maps.Equal(eff.Held, want) {
		t.Errorf("held = %v, want %v", eff.Held, want)
	}
	if sec := eff.Reviewers["security"]; sec.Model != "p/big" || *sec.Effort != model.EffortHigh || sec.Fallback != "p/backup" {
		t.Errorf("security = %+v, want its own model and effort, the default pass's fallback", sec)
	}
	if docs := eff.Reviewers["docs"]; docs.Model != "p/small" || *docs.Effort != model.EffortLow {
		t.Errorf("docs = %+v, want the default pass's model", docs)
	}
	if reviewers["docs"].Model != "" {
		t.Error("judging changed the configuration's agents")
	}
}

// TestPickModelsAsked: an agent that replaces the default pass, asked for
// by name, picks the review's model whatever its conditions; asked for no
// such agent, the first that holds does.
func TestPickModelsAsked(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	fresh := func() Effective {
		return Effective{Models: configfile.Models{Review: "p/small", Routes: []configfile.ModelRoute{
			{Name: "deep", Model: "p/big", When: []configfile.Filter{{Expr: "pr.draft"}}},
			{Name: "light", Model: "p/tiny", When: []configfile.Filter{{Expr: "pr.bot"}}},
		}}}
	}
	vars := map[string]any{"draft": false, "bot": false}
	noDiff := func() (string, error) { return "", nil }
	for asked, want := range map[string]configfile.ModelRef{"": "p/small", "light": "p/tiny", "security": "p/small"} {
		eff := fresh()
		pickModels(logger, &eff, vars, noDiff, asked)
		if eff.Models.Review != want {
			t.Errorf("asked %q: model = %s, want %s", asked, eff.Models.Review, want)
		}
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

// TestRequestedNoReviewer: "@<bot> review <word>" that names no agent
// that is on queues a review of every agent that keeps the word, so the
// review states why the one asked for did not run.
func TestRequestedNoReviewer(t *testing.T) {
	off := false
	settings := configfile.Settings{
		Reviewers: map[string]configfile.Reviewer{"security": {}, "retired": {Enabled: &off}},
		Models:    configfile.Models{Routes: []configfile.ModelRoute{{Name: "deep", Model: "p/big"}}},
	}
	for _, tt := range []struct{ name, reviewer, requested string }{
		{"security", "security", ""}, {"default", "default", ""}, {"deep", "deep", ""}, {"please", "", "please"},
		// A specialist that is off is a word that names none: the request
		// is one of every agent, as its review runs.
		{"retired", "", "retired"},
	} {
		if r, q := splitRequested(tt.name, settings); r != tt.reviewer || q != tt.requested {
			t.Errorf("splitRequested(%q) = %q, %q; want %q, %q", tt.name, r, q, tt.reviewer, tt.requested)
		}
	}
	args := jobs.ReviewArgs{Requested: "please"}
	eff := Effective{Reviewers: settings.Reviewers, Held: map[string]bool{"security": true}}
	got := chooseReviewers(eff, map[string]any{}, priorReview{}, asked(args), 1000)
	if len(got.specs) != 2 || !slices.Contains(got.notes(), "No agent named please is on, so every agent that applies ran") {
		t.Errorf("agents = %+v, want every one with the note", got)
	}
}

// TestMayRun: a review holds slots on the models of the specialists it
// may run alone: those whose conditions held, or the one asked for, and
// none when the default pass or an agent that replaces it is asked for.
func TestMayRun(t *testing.T) {
	off := false
	eff := Effective{
		Reviewers: map[string]configfile.Reviewer{
			"security": {Model: "p/big"},
			"docs":     {Model: "p/doc"},
			"retired":  {Enabled: &off, Model: "p/old"},
		},
		Models: configfile.Models{Review: "p/small", Routes: []configfile.ModelRoute{{Name: "deep", Model: "p/huge"}}},
		Held:   map[string]bool{"security": true, "docs": false, "retired": true},
	}
	for _, tt := range []struct {
		args jobs.ReviewArgs
		want []string
	}{
		{jobs.ReviewArgs{}, []string{"p/big"}},
		{jobs.ReviewArgs{Reviewer: "docs"}, []string{"p/doc"}},
		{jobs.ReviewArgs{Reviewer: "default"}, nil},
		{jobs.ReviewArgs{Reviewer: "deep"}, nil},
		{jobs.ReviewArgs{Requested: "please"}, []string{"p/big"}},
	} {
		if got := reviewerModelKeys(mayRun(eff, tt.args)); !slices.Equal(got, tt.want) {
			t.Errorf("slots for %+v = %q, want %q", tt.args, got, tt.want)
		}
	}
	if len(eff.Reviewers) != 3 {
		t.Error("mayRun changed the review's agents")
	}
}

// TestMayRunStaleRequest: a request queued for an agent that is no longer
// on runs every agent that applies, and holds slots for them all, as
// chooseReviewers falls back to.
func TestMayRunStaleRequest(t *testing.T) {
	off := false
	eff := Effective{
		Reviewers: map[string]configfile.Reviewer{
			"retired":  {Enabled: &off, Model: "p/old"},
			"security": {Model: "p/big"},
		},
		Models: configfile.Models{Review: "p/small"},
		Held:   map[string]bool{"security": true},
	}
	for _, asked := range []string{"retired", "removed"} {
		args := jobs.ReviewArgs{Reviewer: asked}
		if got := reviewerModelKeys(mayRun(eff, args)); !slices.Equal(got, []string{"p/big"}) {
			t.Errorf("slots for a stale request of %s = %q, want security's", asked, got)
		}
		got := chooseReviewers(eff, map[string]any{}, priorReview{}, asked, 1000)
		if len(got.specs) != 2 || got.note == "" {
			t.Errorf("agents for a stale request of %s = %+v, want every one with a note", asked, got)
		}
	}
}
