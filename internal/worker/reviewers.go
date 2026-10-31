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
	"github.com/home-operations/kritika/internal/jobs"
	"github.com/home-operations/kritika/internal/repoconfig"
	"github.com/home-operations/kritika/internal/review"
	"github.com/home-operations/kritika/internal/runspec"
	"github.com/home-operations/kritika/internal/store"
)

// reviewers is the agents that a review runs, as its job document carries
// them. It also holds the note its summary states when only named no agent
// that is on.
type reviewers struct {
	specs []runspec.Reviewer
	note  string
}

// notes are what the summary states of the agents: the note, if any.
func (r reviewers) notes() []string {
	if r.note == "" {
		return nil
	}
	return []string{r.note}
}

// count is how many agents the review runs, which its parts, grant and
// deadline are sized for: one for the default pass alone.
func (r reviewers) count() int { return max(1, len(r.specs)) }

// chooseReviewers is the agents that a review of a pull request with the
// filter variables vars runs. settings is the repository's. The agents are
// the default pass, on the model that the agents that replace it picked. Then
// come, by name, each specialist that is on and whose when conditions
// judgeReviewers found to hold.
//
// only, when it names a specialist, is the one that a review someone asked
// for runs alone, whatever its paths and conditions. When it names the
// default pass or an agent that replaces it, that pass runs alone, on the
// agent's model (see pickModels). When it names no agent that is on, the
// review runs them all, with a note.
//
// An agent that the last review, prior, did not run is marked to review the
// whole change. specs is nil when the default pass runs alone and the last
// review ran it, as a review always does.
func chooseReviewers(settings Effective, vars map[string]any, prior priorReview, only string, maxTokens int64) reviewers {
	var out reviewers
	names := slices.Sorted(maps.Keys(settings.Reviewers))
	runDefault := true
	switch _, specialist := settings.Reviewers[only]; {
	case only == "":
	case !runsAlone(settings.Settings, only):
		out.note = fmt.Sprintf("No agent named %s is on, so every agent that applies ran", only)
		only = ""
	case specialist:
		names, runDefault = []string{only}, false
	default:
		names = nil
	}
	full := func(name string) bool { return prior.id != "" && !slices.Contains(prior.reviewers, name) }
	if runDefault {
		out.specs = append(out.specs, runspec.Reviewer{Name: review.DefaultReviewer, Full: full(review.DefaultReviewer)})
	}
	for _, name := range names {
		r := settings.Reviewers[name]
		if !r.On() || only == "" && !settings.Held[name] {
			continue
		}
		spec := runspec.Reviewer{
			Name: name, Instructions: r.Instructions, File: r.File, Categories: r.Categories, Paths: r.Paths,
			Rules: repoconfig.RulesFor(r.Rules, vars), Full: full(name), Limits: reviewerLimits(r, settings.Agent, maxTokens),
		}
		if only != "" {
			spec.Paths = nil
		}
		if sk := settings.Skills; len(sk.Paths) > 0 {
			scope := configfile.WithSkills(sk, r.Skills).Scope
			spec.Skills = &runspec.Skills{Paths: sk.Paths, Scope: scope, Off: repoconfig.SkillsOff(scope, vars)}
		}
		out.specs = append(out.specs, spec)
	}
	if len(out.specs) == 1 && out.specs[0].Name == review.DefaultReviewer && !out.specs[0].Full {
		out.specs = nil
	}
	return out
}

// reviewerLimits are the bounds that r's agent runs within, where they are
// lower than the repository's agent configuration and maxTokens, its budget a
// part. It is nil where r sets none.
func reviewerLimits(r configfile.Reviewer, agent configfile.AgentSettings, maxTokens int64) *runspec.ReviewerLimits {
	if r.Agent == (configfile.ReviewerAgent{}) {
		return nil
	}
	agent.MaxTokens = maxTokens
	b := r.Bounds(agent)
	return &runspec.ReviewerLimits{MaxSteps: b.MaxSteps, MaxTokens: b.MaxTokens, TimeoutSeconds: int(b.Timeout / time.Second)}
}

// reviewerGrants are the models of the reviewers other than the default one
// that the run can run, by name: each one's own, or the repository's where it
// sets none, pinned as pinModels pins the review's.
func (w *Review) reviewerGrants(
	ctx context.Context, logger *slog.Logger, file *configfile.File, account *configfile.Account, settings configfile.Settings,
	specs []runspec.Reviewer,
) map[string]store.ReviewerGrant {
	var out map[string]store.ReviewerGrant
	for _, spec := range specs {
		if spec.Name == review.DefaultReviewer {
			continue
		}
		models := settings.Reviewers[spec.Name].Models(settings.Models)
		models = pinModels(ctx, logger, w.Steppers, file, account, models)
		if out == nil {
			out = map[string]store.ReviewerGrant{}
		}
		out[spec.Name] = store.ReviewerGrant{Model: string(models.Review), Fallback: string(models.Fallback), Effort: string(models.Effort)}
	}
	return out
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

// reviewerCategories are the categories that the reviewers other than the
// default one can report, by name, those that name any.
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

// readPull reads the pull request prID once for a review started by trigger,
// so the reviewers it runs and its prompt judge the same row. It returns the
// pull request with its filter variables.
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

// asked is the reviewer that a re-run someone asked for names, or the word
// they wrote that names none. chooseReviewers states that it ran every
// reviewer in place of that word. asked is "" for a review of every reviewer.
func asked(args jobs.ReviewArgs) string { return cmp.Or(args.Reviewer, args.Requested) }

// mayRun is eff's configuration with its specialists cut to those that a
// review asked for as args can run, whose models it holds slots on. The
// result is the one asked for alone, when one that runs alone is asked for.
// It is none when the default pass or an agent that replaces it is asked for.
// Otherwise it is each that is on and whose when conditions judgeReviewers
// found to hold.
func mayRun(eff Effective, args jobs.ReviewArgs) configfile.Settings {
	s := eff.Settings
	asked := args.Reviewer
	if !runsAlone(s, asked) {
		// Asked for an agent that is no longer on, the review runs every
		// one that applies, as chooseReviewers falls back to.
		asked = ""
	}
	s.Reviewers = maps.Clone(s.Reviewers)
	maps.DeleteFunc(s.Reviewers, func(name string, r configfile.Reviewer) bool {
		if asked != "" {
			return name != asked
		}
		return !r.On() || !eff.Held[name]
	})
	return s
}

// runsAlone reports whether name is an agent that a review asked for runs
// alone: the default pass, an agent on that replaces it, or a specialist that
// is on. settings is the repository's as it is now.
func runsAlone(settings configfile.Settings, name string) bool {
	r, specialist := settings.Reviewers[name]
	return name == review.DefaultReviewer || specialist && r.On() ||
		slices.ContainsFunc(settings.Models.Routes, func(o configfile.ModelRoute) bool { return o.Name == name })
}
