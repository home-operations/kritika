package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/forge"
	"github.com/home-operations/kritika/internal/jobs"
	"github.com/home-operations/kritika/internal/repoconfig"
	"github.com/home-operations/kritika/internal/review"
	"github.com/home-operations/kritika/internal/runner"
	"github.com/home-operations/kritika/internal/store"
)

// prepared is what afterRun hands the publish phase: the patch id, the
// settings with the repository's .kritika.yaml applied and its templates
// read, the notes the summary states, whether the review builds on the
// last completed one, and the ids of the rules the agent was given.
type prepared struct {
	patchID   string
	eff       Effective
	templates review.Templates
	notes     []string
	scope     review.Scope
	ruleIDs   []string
}

// afterRun re-checks the head under the account transaction and reads the
// context pack: the runner decided whether the review is skipped and what
// it builds on, and read the merge-base repository files. A skipped
// review ends with a success status saying why. notes are the worker's
// own on the repository's file. It returns a patch id when the review
// should go on to publishing, and "" plus the terminal status it recorded
// otherwise.
func (w *Review) afterRun(
	ctx context.Context, job *river.Job[jobs.ReviewArgs], account *configfile.Account, pr *pullRequest, eff Effective, notes []string,
	client forge.Client, reviewID, runID string, prior priorReview, judged json.RawMessage, logger *slog.Logger,
) (prepared, store.ReviewStatus, error) {
	args := job.Args
	pack, decision, err := w.recordPreparation(ctx, job, pr.id, reviewID, runID, prior.id, judged)
	if err != nil {
		return prepared{}, "", err
	}
	if decision.end.Status == store.ReviewSuperseded {
		logger.Info("review superseded", "patch_id", review.ShortSHA(pack.PatchID))
		return prepared{}, decision.end.Status, decision.jobError()
	}
	for stage, n := range pack.StageCounts {
		w.Metrics.ContextChunks(account.Key(), stage, n)
	}
	if decision.end.Status == store.ReviewSkipped {
		logger.Info("review skipped", "reason", pack.SkipReason, "patch_id", review.ShortSHA(pack.PatchID))
		var carried *review.Confidence
		if pack.SkipReason == runner.SkipUnchangedPatch {
			err := w.Store.WithAccount(ctx, args.AccountID, func(tx pgx.Tx) error {
				var err error
				carried, _, err = carriedConfidence(ctx, tx, pr.id, reviewID, eff.Confidence)
				return err
			})
			if err != nil {
				return prepared{}, "", err
			}
		}
		owner, repo := pr.ownerRepo()
		state, desc := skipVerdict(carried, eff.Confidence.Gate, skipDescription(pack.SkipReason, pack.SkipDetail))
		if err := client.SetStatus(ctx, owner, repo, args.HeadSHA, state, "kritika: "+desc); err != nil {
			logger.Warn("commit status not set", "error", err)
		}
		carryApproval(ctx, logger, client, pr, eff.Settings, carried)
		if decision.disposition == reviewSnooze {
			logger.Info("labels moved while the runner judged the filter; the review is judged again")
		}
		return prepared{}, decision.end.Status, decision.jobError()
	}
	logger.Info("review prepared", "patch_id", review.ShortSHA(pack.PatchID), "scope", pack.Scope, "scope_reason", pack.ScopeReason)
	return prepared{
		patchID: pack.PatchID, eff: eff, templates: eff.templates(pack.Files), notes: append(slices.Clone(notes), pack.Notes...),
		scope: pack.Scope, ruleIDs: pack.RuleIDs,
	}, decision.end.Status, nil
}

// recordPreparation gathers the facts for one outcome decision and persists
// it atomically. A filtered skip is final only when labels remain unchanged
// under the row lock shared with ingest and the River job completes in the
// same transaction. Changed labels leave the job unfinished for a snooze.
func (w *Review) recordPreparation(
	ctx context.Context, job *river.Job[jobs.ReviewArgs], prID, reviewID, runID, priorID string, judged json.RawMessage,
) (pack store.ContextPackRecord, decision reviewDecision, err error) {
	err = w.Store.WithAccount(ctx, job.Args.AccountID, func(tx pgx.Tx) error {
		head, err := store.PullRequestHead(ctx, tx, prID)
		if err != nil {
			return fmt.Errorf("worker: re-read head: %w", err)
		}
		superseded := head != job.Args.HeadSHA
		labelsMoved := false
		if !superseded {
			pack, err = store.ReadContextPack(ctx, tx, runID)
			if err != nil {
				return err
			}
			if pack.SkipReason == string(repoconfig.SkipFiltered) {
				labels, err := store.LockPullRequestLabels(ctx, tx, prID)
				if err != nil {
					return err
				}
				labelsMoved = !bytes.Equal(labels, judged)
			}
		}
		decision = decidePreparedReview(superseded, pack.SkipReason, labelsMoved)
		decision.end.PatchID = pack.PatchID
		if decision.disposition == reviewContinue {
			return store.MarkReviewPrepared(ctx, tx, reviewID, pack.PatchID, pack.Scope, pack.ScopeReason, priorID)
		}
		if _, err := store.EndReview(ctx, tx, reviewID, decision.end); err != nil {
			return err
		}
		if pack.SkipReason == string(repoconfig.SkipFiltered) && decision.disposition == reviewFinish {
			_, err = river.JobCompleteTx[*riverpgxv5.Driver](ctx, tx, job)
		}
		return err
	})
	return pack, decision, err
}
