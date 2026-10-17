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
	pack, decision, err := w.recordPreparation(ctx, job, pr.id, reviewID, runID, prior.id, judged, eff.Confidence)
	if err != nil {
		return prepared{}, "", err
	}
	if decision.end.Status == store.ReviewSuperseded {
		logger.Info("review superseded", "patch_id", review.ShortSHA(pack.PatchID))
		return prepared{}, decision.end.Status, decision.jobError()
	}
	if decision.end.Status == store.ReviewCanceled {
		// A review a newer one replaced leaves the head's status to that
		// one; a cancel someone asked for reports itself.
		if decision.description == "" {
			logger.Info("review canceled before preparation: a newer review replaced it")
			return prepared{}, decision.end.Status, nil
		}
		sctx, cancel := detach(ctx)
		defer cancel()
		w.reportEnd(sctx, afterRunEnded(args, pr, reviewID, client, logger), decision.description)
		return prepared{}, decision.end.Status, nil
	}
	for stage, n := range pack.StageCounts {
		w.Metrics.ContextChunks(account.Key(), stage, n)
	}
	if decision.end.Status == store.ReviewSkipped {
		logger.Info("review skipped", "reason", pack.SkipReason, "patch_id", review.ShortSHA(pack.PatchID))
		// The skip is committed, and its job with it when the filter decided
		// it: reporting it must survive a job ending now, and a review of
		// the head someone asks for meanwhile owns the head from its start.
		sctx, cancel := detach(ctx)
		defer cancel()
		state, desc := skipVerdict(pack.carried, eff.Confidence.Gate, skipDescription(pack.SkipReason, pack.SkipDetail))
		w.ownedWrite(sctx, afterRunEnded(args, pr, reviewID, client, logger), func(ctx context.Context) error {
			owner, repo := pr.ownerRepo()
			if err := client.SetStatus(ctx, owner, repo, args.HeadSHA, state, "kritika: "+desc); err != nil {
				logger.Warn("commit status not set", "error", err)
			}
			carryApproval(ctx, logger, client, pr, eff.Settings, pack.carried)
			return nil
		})
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

// afterRunEnded is the review afterRun ends, as ownedWrite takes it.
func afterRunEnded(args jobs.ReviewArgs, pr *pullRequest, reviewID string, client forge.Client, logger *slog.Logger) endedReview {
	owner, repo := pr.ownerRepo()
	return endedReview{
		accountID: args.AccountID, reviewID: reviewID, prID: pr.id, headSHA: args.HeadSHA,
		owner: owner, repo: repo, client: client, logger: logger,
	}
}

type preparationRecord struct {
	store.ContextPackRecord
	carried *review.Confidence
}

// recordPreparation gathers the facts for one outcome decision and persists
// it atomically. A filtered skip is final only when labels remain unchanged
// under the row lock shared with ingest and the River job completes in the
// same transaction. Changed labels leave the job unfinished for a snooze.
func (w *Review) recordPreparation(
	ctx context.Context, job *river.Job[jobs.ReviewArgs], prID, reviewID, runID, priorID string, judged json.RawMessage,
	confidence configfile.Confidence,
) (pack preparationRecord, decision reviewDecision, err error) {
	err = w.Store.WithAccount(ctx, job.Args.AccountID, func(tx pgx.Tx) error {
		head, labels, err := store.LockPullRequestPreparation(ctx, tx, prID)
		if err != nil {
			return fmt.Errorf("worker: read preparation head and labels: %w", err)
		}
		status, canceled, replaced, err := store.LockReviewPreparation(ctx, tx, reviewID)
		if err != nil {
			return err
		}
		if replaced {
			decision = reviewDecision{end: store.ReviewEnd{Status: store.ReviewCanceled, OnlyUnfinished: true}, disposition: reviewFinish}
			return nil
		}
		if status != store.ReviewRunning {
			return fmt.Errorf("worker: review is %s before preparation", status)
		}
		if canceled {
			decision = decideInterruptedReview(river.ErrJobCancelledRemotely, nil)
			_, err = store.EndReview(ctx, tx, reviewID, decision.end)
			return err
		}
		superseded := head != job.Args.HeadSHA
		if !superseded {
			pack.ContextPackRecord, err = store.ReadContextPack(ctx, tx, runID)
			if err != nil {
				return fmt.Errorf("worker: read preparation context pack: %w", err)
			}
		}
		filtered := pack.SkipReason == string(repoconfig.SkipFiltered)
		decision = decidePreparedReview(superseded, pack.SkipReason, filtered && !bytes.Equal(labels, judged))
		if decision.disposition == reviewContinue {
			return store.MarkReviewPrepared(ctx, tx, reviewID, pack.PatchID, pack.Scope, pack.ScopeReason, priorID)
		}
		if pack.SkipReason == runner.SkipUnchangedPatch {
			pack.carried, _, err = carriedConfidence(ctx, tx, prID, reviewID, confidence)
			if err != nil {
				return err
			}
		}
		decision.end.PatchID = pack.PatchID
		if _, err := store.EndReview(ctx, tx, reviewID, decision.end); err != nil {
			return err
		}
		if filtered && decision.disposition == reviewFinish {
			return completeJob(ctx, tx, job)
		}
		return nil
	})
	return pack, decision, err
}
