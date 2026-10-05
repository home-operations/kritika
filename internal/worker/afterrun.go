package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"time"

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
	var pack store.ContextPackRecord
	var superseded bool
	err := w.Store.WithAccount(ctx, args.AccountID, func(tx pgx.Tx) error {
		var currentHead string
		if err := tx.QueryRow(ctx, `SELECT head_sha FROM pull_requests WHERE id = $1`, pr.id).Scan(&currentHead); err != nil {
			return fmt.Errorf("worker: re-read head: %w", err)
		}
		if currentHead != args.HeadSHA {
			superseded = true
			return nil
		}
		var err error
		pack, err = store.ReadContextPack(ctx, tx, runID)
		return err
	})
	if err != nil {
		return prepared{}, "", err
	}
	if superseded {
		logger.Info("review superseded", "patch_id", review.ShortSHA(pack.PatchID))
		return prepared{}, store.ReviewSuperseded, w.finishReview(ctx, args.AccountID, reviewID, store.ReviewSuperseded, pack.PatchID, "")
	}
	for stage, n := range pack.StageCounts {
		w.Metrics.ContextChunks(account.Key(), stage, n)
	}
	if pack.SkipReason != "" {
		logger.Info("review skipped", "reason", pack.SkipReason, "patch_id", review.ShortSHA(pack.PatchID))
		end := store.ReviewEnd{Status: store.ReviewSkipped, PatchID: pack.PatchID, SkipReason: pack.SkipReason}
		moved := false
		if pack.SkipReason == string(repoconfig.SkipFiltered) {
			moved, err = w.endFiltered(ctx, job, pr.id, reviewID, end, judged)
		} else {
			_, err = w.endReview(ctx, args.AccountID, reviewID, end)
		}
		if err != nil {
			return prepared{}, "", err
		}
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
		state, desc := skipVerdict(carried, skipDescription(pack.SkipReason, pack.SkipDetail))
		if err := client.SetStatus(ctx, owner, repo, args.HeadSHA, state, "kritika: "+desc); err != nil {
			logger.Warn("commit status not set", "error", err)
		}
		carryApproval(ctx, logger, client, pr, eff.Settings, carried)
		if moved {
			logger.Info("labels moved while the runner judged the filter; the review is judged again")
			return prepared{}, store.ReviewSkipped, river.JobSnooze(time.Second)
		}
		return prepared{}, store.ReviewSkipped, nil
	}
	err = w.Store.WithAccount(ctx, args.AccountID, func(tx pgx.Tx) error {
		return store.MarkReviewPrepared(ctx, tx, reviewID, pack.PatchID, pack.Scope, pack.ScopeReason, prior.id)
	})
	if err != nil {
		return prepared{}, "", err
	}
	logger.Info("review prepared", "patch_id", review.ShortSHA(pack.PatchID), "scope", pack.Scope, "scope_reason", pack.ScopeReason)
	return prepared{
		patchID: pack.PatchID, eff: eff, templates: eff.templates(pack.Files), notes: append(slices.Clone(notes), pack.Notes...),
		scope: pack.Scope, ruleIDs: pack.RuleIDs,
	}, store.ReviewPrepared, nil
}

// endFiltered ends a review the runner skipped as filtered, and reports
// whether the pull request's labels moved since they were judged. A label
// change that reaches ingest while this job is still to finish queues
// nothing, the job being the head's review to come, so the skip is final
// only when the labels are seen unchanged in the transaction that
// completes the job, under the pull request's row lock that ingest's own
// write waits on. With labels that moved the job is left to run again.
func (w *Review) endFiltered(
	ctx context.Context, job *river.Job[jobs.ReviewArgs], prID, reviewID string, end store.ReviewEnd, judged json.RawMessage,
) (moved bool, err error) {
	err = w.Store.WithAccount(ctx, job.Args.AccountID, func(tx pgx.Tx) error {
		var labels json.RawMessage
		if err := tx.QueryRow(ctx, `SELECT labels FROM pull_requests WHERE id = $1 FOR UPDATE`, prID).Scan(&labels); err != nil {
			return fmt.Errorf("worker: read the pull request's labels: %w", err)
		}
		if _, err := store.EndReview(ctx, tx, reviewID, end); err != nil {
			return err
		}
		if moved = !bytes.Equal(labels, judged); moved {
			return nil
		}
		_, err := river.JobCompleteTx[*riverpgxv5.Driver](ctx, tx, job)
		return err
	})
	return moved, err
}
