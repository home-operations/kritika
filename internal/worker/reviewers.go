package worker

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/repoconfig"
	"github.com/home-operations/kritika/internal/review"
	"github.com/home-operations/kritika/internal/runner"
	"github.com/home-operations/kritika/internal/store"
)

// reviewers is the reviewers a review runs, as its job document carries
// them, whether the default reviewer is off, and the note its summary
// states when only named no reviewer that is on.
type reviewers struct {
	specs      []runner.Reviewer
	defaultOff bool
	note       string
}

// notes are what the summary states of the reviewers: the note, if any.
func (r reviewers) notes() []string {
	if r.note == "" {
		return nil
	}
	return []string{r.note}
}

// count is how many reviewers the review runs, which its parts, grant
// and deadline are sized for: one for the default reviewer alone.
func (r reviewers) count() int { return max(1, len(r.specs)) }

// chooseReviewers is the reviewers a review of a pull request with the filter
// variables vars runs, settings being the repository's: the default one
// unless it is off, then each other one that is on and one of whose when
// conditions holds, by name. only, when it names a reviewer, is the one a
// review someone asked for runs alone, whatever its paths and conditions;
// one that names none that is on runs them all, with a note. A reviewer the last
// review, prior, did not run is marked to review the whole change. specs
// is nil when the default reviewer runs alone and the last review ran it,
// as a review always has, or, with defaultOff, when no reviewer is left to
// run, which the runner skips.
func chooseReviewers(settings Effective, vars map[string]any, prior priorReview, only string, maxTokens int64) reviewers {
	var out reviewers
	names := slices.Sorted(maps.Keys(settings.Reviewers))
	runDefault := configfile.DefaultOn(settings.Reviewers)
	if only != "" {
		r, named := settings.Reviewers[only]
		switch {
		case only == review.DefaultReviewer && runDefault:
			names = nil
		case only != review.DefaultReviewer && named && r.On():
			names, runDefault = []string{only}, false
		default:
			out.note = fmt.Sprintf("No reviewer named %s is on, so every reviewer that applies ran", only)
			only = ""
		}
	}
	out.defaultOff = !runDefault
	full := func(name string) bool { return prior.id != "" && !slices.Contains(prior.reviewers, name) }
	if runDefault {
		out.specs = append(out.specs, runner.Reviewer{Name: review.DefaultReviewer, Full: full(review.DefaultReviewer)})
	}
	for _, name := range names {
		r := settings.Reviewers[name]
		if name == review.DefaultReviewer || !r.On() || only == "" && !configfile.AnyHolds(r.When, vars) {
			continue
		}
		spec := runner.Reviewer{
			Name: name, Instructions: r.Instructions, File: r.File, Categories: r.Categories, Paths: r.Paths,
			Rules: repoconfig.RulesFor(r.Rules, vars), Full: full(name), Limits: reviewerLimits(r, settings.Agent, maxTokens),
		}
		if only != "" {
			spec.Paths = nil
		}
		if sk := settings.Skills; len(sk.Paths) > 0 {
			scope := configfile.WithSkills(sk, r.Skills).Scope
			spec.Skills = &runner.Skills{Paths: sk.Paths, Scope: scope, Off: repoconfig.SkillsOff(scope, vars)}
		}
		out.specs = append(out.specs, spec)
	}
	if len(out.specs) == 1 && out.specs[0].Name == review.DefaultReviewer && !out.specs[0].Full {
		out.specs = nil
	}
	return out
}

// reviewerLimits are the bounds r's agent runs within, where they are
// lower than the repository's agent settings and maxTokens, its budget a
// part; nil where r sets none.
func reviewerLimits(r configfile.Reviewer, agent configfile.AgentSettings, maxTokens int64) *runner.ReviewerLimits {
	if r.Agent == (configfile.ReviewerAgent{}) {
		return nil
	}
	agent.MaxTokens = maxTokens
	b := r.Bounds(agent)
	return &runner.ReviewerLimits{MaxSteps: b.MaxSteps, MaxTokens: b.MaxTokens, TimeoutSeconds: int(b.Timeout / time.Second)}
}

// reviewerGrants are the models of the reviewers other than the default
// one the run may run, by name: each one's own, or the repository's where
// it sets none, pinned as pinModels pins the review's.
func (w *Review) reviewerGrants(
	ctx context.Context, logger *slog.Logger, file *configfile.File, account *configfile.Account, settings configfile.Settings,
	specs []runner.Reviewer,
) map[string]store.ReviewerGrant {
	var out map[string]store.ReviewerGrant
	for _, spec := range specs {
		if spec.Name == review.DefaultReviewer {
			continue
		}
		models := reviewerModels(settings.Reviewers[spec.Name], settings.Models)
		models = pinModels(ctx, logger, w.Steppers, file, account, models)
		if out == nil {
			out = map[string]store.ReviewerGrant{}
		}
		out[spec.Name] = store.ReviewerGrant{Model: string(models.Review), Fallback: string(models.Fallback), Effort: string(models.Effort)}
	}
	return out
}

// reviewerModels are r's models: its own, the repository's, defaults,
// where it sets none.
func reviewerModels(r configfile.Reviewer, defaults configfile.Models) configfile.Models {
	models := configfile.Models{Review: cmp.Or(r.Model, defaults.Review), Fallback: cmp.Or(r.Fallback, defaults.Fallback),
		Effort: defaults.Effort}
	if r.Effort != nil {
		models.Effort = *r.Effort
	}
	return models
}

// ranReviewers are the reviewers whose parts a review's agent run lists,
// in order, the default reviewer for a run not split by reviewer.
func ranReviewers(parts []store.AgentPart) []string {
	var out []string
	for _, part := range parts {
		if name := cmp.Or(part.Reviewer, review.DefaultReviewer); !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	if len(out) == 0 {
		return []string{review.DefaultReviewer}
	}
	return out
}

// fullReviewers are the reviewers whose parts reviewed the whole change of
// a review that built on the last one: an incremental review holds none
// of their findings back.
func fullReviewers(parts []store.AgentPart) []string {
	var out []string
	for _, part := range parts {
		if name := cmp.Or(part.Reviewer, review.DefaultReviewer); part.Full && !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	return out
}

// reviewerCategories are the categories the reviewers other than the
// default one may report, by name, those that name any.
func reviewerCategories(reviewers map[string]configfile.Reviewer) map[string][]review.Category {
	var out map[string][]review.Category
	for name, r := range reviewers {
		if len(r.Categories) == 0 || name == review.DefaultReviewer {
			continue
		}
		if out == nil {
			out = map[string][]review.Category{}
		}
		out[name] = r.Categories
	}
	return out
}

// readPull reads the pull request prID once for a review started by
// trigger, so the reviewers it runs and its prompt judge the same row, and
// returns it with its filter variables.
func (w *Review) readPull(ctx context.Context, accountID, prID, trigger string) (repoconfig.PullRequest, map[string]any, error) {
	var pull repoconfig.PullRequest
	if err := w.Store.WithAccount(ctx, accountID, func(tx pgx.Tx) error {
		var err error
		pull, err = store.ReadFilterPullRequest(ctx, tx, prID)
		return err
	}); err != nil {
		return repoconfig.PullRequest{}, nil, err
	}
	judged := pull
	judged.Event = trigger
	vars, err := judged.Vars()
	return pull, vars, err
}
