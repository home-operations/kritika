package worker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/contextpack"
	"github.com/home-operations/kritika/internal/forge"
	"github.com/home-operations/kritika/internal/jobs"
	"github.com/home-operations/kritika/internal/repoconfig"
	"github.com/home-operations/kritika/internal/review"
	"github.com/home-operations/kritika/internal/store"
	"github.com/home-operations/kritika/internal/udiff"
)

// Effective is a repository's settings once its .kritika.yaml is applied.
// Settings.Review names the files the runner reads from the merge base and
// keeps in the review's context pack.
type Effective struct {
	repoconfig.Merged
	// Found is whether the repository has a .kritika.yaml.
	Found bool
	// Held are the specialists whose when conditions judgeReviewers found
	// to hold for the pull request, by name.
	Held map[string]bool
}

// readRepoConfig reads .kritika.yaml at ref through the forge: nil when the
// repository has none, and nil with a note when it is too large to use.
func readRepoConfig(ctx context.Context, client forge.Client, owner, repo, ref string) ([]byte, []string, error) {
	doc, err := client.FileAt(ctx, owner, repo, ref, repoconfig.FileName)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, nil, nil
	case errors.Is(err, forge.ErrFileTooLarge) || err == nil && len(doc) > repoconfig.MaxFileBytes:
		return nil, []string{repoconfig.TooLarge(repoconfig.FileName)}, nil
	case err != nil:
		return nil, nil, err
	}
	return doc, nil, nil
}

// effective applies doc, the merge-base .kritika.yaml or nil when there is
// none, onto the admin's settings (see repoconfig.Merge). The notes say
// which of the file's values were dropped, or why the whole file was
// ignored.
func effective(settings configfile.Settings, doc []byte) (Effective, []string) {
	m, err := repoconfig.Merge(doc, settings)
	notes := m.Dropped
	if err != nil {
		notes = append(notes, fmt.Sprintf("%s was ignored: %v", repoconfig.FileName, err))
	}
	return Effective{Merged: m, Found: doc != nil}, notes
}

// narrowTo turns confidence scoring and its gate off for a pull request,
// with the filter variables vars, that their conditions do not hold for.
// One confidence.when's admin conditions leave out is reviewed as a
// repository with no confidence model is; one only the repository file's
// leave out is not approved either. One confidence.gate's leave out shows
// its score and passes.
func (e *Effective) narrowTo(vars map[string]any) {
	c := &e.Confidence
	if !configfile.AnyHolds(c.GateWhen, vars) {
		c.Gate = false
	}
	switch {
	case c.Model == "":
	case !configfile.AnyHolds(c.When, vars):
		c.Model, c.Fallback = "", ""
	case !configfile.AnyHolds(c.WhenInRepo, vars):
		c.Model, c.Fallback, c.RepoUnscored = "", "", true
	}
}

// pickRoute has eff's model routes pick the review's model for the pull
// request with the filter variables vars, reading the forge's diff when
// one of their conditions needs it. A diff the forge will not give leaves
// those conditions not holding, and the others are judged without it.
func pickRoute(logger *slog.Logger, eff *Effective, vars map[string]any, forgeDiff func() (string, error)) {
	if len(eff.Models.Routes) == 0 {
		return
	}
	var d *configfile.Diff
	if eff.Models.NeedsDiff() {
		d = pullDiff(logger, eff, forgeDiff)
	}
	picked, err := eff.Models.Pick(vars, d)
	if err != nil {
		logger.Warn("model route condition failed to evaluate", "error", err)
	}
	if picked.Route != "" {
		logger.Info("model route picked", "route", picked.Route, "model", picked.Review)
	}
	eff.Models = picked
}

// pickModels picks the review's model and judges its specialists: the
// agent asked picks it when asked names one that replaces the default pass,
// and otherwise the first of those that holds does (pickRoute); then the
// specialists are judged (judgeReviewers).
func pickModels(logger *slog.Logger, eff *Effective, vars map[string]any, forgeDiff func() (string, error), asked string) {
	if i := slices.IndexFunc(eff.Models.Routes, func(r configfile.ModelRoute) bool { return r.Name == asked }); i >= 0 {
		eff.Models.Routes = []configfile.ModelRoute{eff.Models.Routes[i]}
		eff.Models.Routes[0].When = nil
	}
	pickRoute(logger, eff, vars, forgeDiff)
	judgeReviewers(logger, eff, vars, forgeDiff)
}

// pullDiff is the pull request's diff as a condition judges it, nil when
// the forge will not give it: conditions on paths or lines then do not
// hold.
func pullDiff(logger *slog.Logger, eff *Effective, forgeDiff func() (string, error)) *configfile.Diff {
	diff, err := forgeDiff()
	if err != nil {
		logger.Warn("forge diff not read; conditions on paths or lines do not hold", "error", err)
		return nil
	}
	return &configfile.Diff{Lines: contextpack.ChangedLines(diff, eff.Ignore), Changed: changedPaths(diff)}
}

// judgeReviewers judges, over the whole pull request, whether the when
// conditions of each of eff's specialists that is on hold, reading the
// forge's diff when a condition needs it, and gives each its models: its
// own, or the default pass's as the agents picked them.
func judgeReviewers(logger *slog.Logger, eff *Effective, vars map[string]any, forgeDiff func() (string, error)) {
	var d *configfile.Diff
	if slices.ContainsFunc(slices.Collect(maps.Values(eff.Reviewers)), configfile.Reviewer.NeedsDiff) {
		d = pullDiff(logger, eff, forgeDiff)
	}
	reviewers := maps.Clone(eff.Reviewers)
	eff.Held = map[string]bool{}
	for name, r := range reviewers {
		if !r.On() {
			continue
		}
		held, err := r.Holds(vars, d)
		if err != nil {
			logger.Warn("agent condition failed to evaluate", "agent", name, "error", err)
		}
		eff.Held[name] = held
		models := r.Models(eff.Models)
		r.Model, r.Fallback, r.Effort = models.Review, models.Fallback, &models.Effort
		reviewers[name] = r
	}
	eff.Reviewers = reviewers
}

// changedPaths are the paths a unified diff changes, each named as the
// runner names it: head-side, base-side once deleted.
func changedPaths(diff string) []string {
	files := udiff.Parse(diff)
	out := make([]string, len(files))
	for i, f := range files {
		out[i] = f.Path()
	}
	return out
}

// repoFiles are the paths the runner reads from the merge base: the files
// the settings and their reviewers name, and .kritika.yaml itself, which
// the review's context pack keeps a copy of.
func (e *Effective) repoFiles() []string {
	paths := e.Review.Referenced()
	for _, p := range configfile.ReviewerFiles(e.Reviewers) {
		if !slices.Contains(paths, p) {
			paths = append(paths, p)
		}
	}
	if e.Found && !slices.Contains(paths, repoconfig.FileName) {
		paths = append(paths, repoconfig.FileName)
	}
	return paths
}

// templates are the repository's comment templates, read out of files,
// what the runner read from the merge base; a named file it could not
// read leaves the built-in template in use, as the pack's notes say.
func (e *Effective) templates(files repoconfig.Files) review.Templates {
	return review.Templates{Summary: files[e.Review.Templates.Summary], Inline: files[e.Review.Templates.Inline]}
}

// skipByRepo ends a review the merge-base .kritika.yaml disables or filters
// out before a runner is spent on it, with a success status saying why. It
// returns the filter variables it judged a review that goes on by, and
// reports whether it ended the review, with the error of recording that.
//
// A label change that reaches ingest while this job is still to finish
// queues nothing, the job being the head's review to come; one that lands
// after the job read the labels would then go unjudged. So a skip is final
// only once the labels are seen unchanged in the transaction that
// completes the job, under the pull request's row lock that ingest's own
// write waits on; labels that moved are judged again.
func (w *Review) skipByRepo(
	ctx context.Context, e earlyEnd, job *river.Job[jobs.ReviewArgs], eff *Effective,
) (map[string]any, bool, error) {
	for {
		var judged repoconfig.PullRequest
		if err := w.Store.WithAccount(ctx, e.args.AccountID, func(tx pgx.Tx) error {
			var err error
			judged, err = store.ReadFilterPullRequest(ctx, tx, e.pr.id)
			return err
		}); err != nil {
			return nil, true, err
		}
		judged.Event = e.args.Trigger
		vars, err := judged.Vars()
		if err != nil {
			return nil, true, err
		}
		// With no changed paths yet, only enabled and the filter can skip.
		reason, by, err := eff.Check(vars, nil)
		if err != nil {
			e.logger.Warn("repository filter failed to evaluate", "filter", by.Label(), "error", err)
		}
		if reason == "" {
			eff.narrowTo(vars)
			return vars, false, nil
		}
		e.filter = ""
		if by != nil {
			e.filter = by.Name
		}
		if err := w.recordSkip(ctx, e, string(reason)); err != nil {
			return nil, true, err
		}
		moved := false
		if err := w.Store.WithAccount(ctx, e.args.AccountID, func(tx pgx.Tx) error {
			labels, err := store.LockPullRequestLabels(ctx, tx, e.pr.id)
			if err != nil {
				return fmt.Errorf("worker: read the pull request's labels: %w", err)
			}
			if moved = !bytes.Equal(labels, judged.Labels); moved {
				return nil
			}
			return completeJob(ctx, tx, job)
		}); err != nil {
			return nil, true, err
		}
		if !moved {
			return nil, true, nil
		}
	}
}

// recordSkip records the skip for reason and says so on the head commit.
// A skip the head's latest review already is, the same job judging again,
// a retry, or a label change that left the head where it was, is not
// recorded a second time; only its status is said again. A re-run someone
// asked for is recorded all the same.
func (w *Review) recordSkip(ctx context.Context, e earlyEnd, reason string) error {
	if e.args.Trigger != jobs.TriggerManual {
		var repeat bool
		if err := w.Store.WithAccount(ctx, e.args.AccountID, func(tx pgx.Tx) error {
			var err error
			repeat, err = store.HeadSkipped(ctx, tx, e.pr.id, e.args.HeadSHA, reason)
			return err
		}); err != nil {
			return err
		}
		if repeat {
			e.logger.Info("review still skipped", "reason", reason)
			// The skip stands, but the condition that decides it may be
			// another one now, and the status names it.
			e.skip = reason
			state, desc := e.status(store.ReviewSkipped, "")
			if err := e.client.SetStatus(ctx, e.owner, e.repo, e.args.HeadSHA, state, desc); err != nil {
				e.logger.Warn("commit status not set", "error", err)
			}
			return nil
		}
	}
	e.logger.Info("review skipped before its runner", "reason", reason)
	e.skip = reason
	return w.end(ctx, e, store.ReviewSkipped, "")
}

// filterVars rebuilds the filter's pr variable for a review started by
// trigger from the stored pull request row, the same keys
// webhook.PullRequest.FilterVars gives ingest.
func filterVars(ctx context.Context, tx pgx.Tx, prID, trigger string) (map[string]any, error) {
	pr, err := store.ReadFilterPullRequest(ctx, tx, prID)
	if err != nil {
		return nil, err
	}
	pr.Event = trigger
	return pr.Vars()
}
