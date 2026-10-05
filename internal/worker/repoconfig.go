package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
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
	"github.com/home-operations/kritika/internal/store"
)

// Effective is a repository's settings once its .kritika.yaml is applied.
// Settings.Review names the files the runner reads from the merge base and
// keeps in the review's context pack.
type Effective struct {
	repoconfig.Merged
	// Found is whether the repository has a .kritika.yaml.
	Found bool
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

// repoFiles are the paths the runner reads from the merge base: the files
// the settings name, and .kritika.yaml itself, which the review's context
// pack keeps a copy of.
func (e *Effective) repoFiles() []string {
	paths := e.Review.Referenced()
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

// settleLeft is how much longer a review job started by trigger, enqueued
// at created, waits before it runs: a new head waits until settle has
// passed since it arrived, so a burst of pushes is reviewed once, at its
// last head.
func settleLeft(trigger string, settle time.Duration, created, now time.Time) time.Duration {
	if !jobs.Settles(trigger) {
		return 0
	}
	return created.Add(settle).Sub(now)
}

// skipByRepo ends a review the merge-base .kritika.yaml disables or filters
// out before a runner is spent on it, with a success status saying why. It
// reports whether it ended the review, with the error of recording that.
//
// A label change that reaches ingest while this job is still to finish
// queues nothing, the job being the head's review to come; one that lands
// after the job read the labels would then go unjudged. So a skip is final
// only once the labels are seen unchanged in the transaction that
// completes the job, under the pull request's row lock that ingest's own
// write waits on; labels that moved are judged again.
func (w *Review) skipByRepo(ctx context.Context, e earlyEnd, job *river.Job[jobs.ReviewArgs], eff *Effective) (bool, error) {
	for {
		var judged repoconfig.PullRequest
		if err := w.Store.WithAccount(ctx, e.args.AccountID, func(tx pgx.Tx) error {
			var err error
			judged, err = loadFilterPR(ctx, tx, e.pr.id)
			return err
		}); err != nil {
			return true, err
		}
		judged.Event = e.args.Trigger
		vars, err := judged.Vars()
		if err != nil {
			return true, err
		}
		// With no changed paths yet, only enabled and the filter can skip.
		reason, by, err := eff.Check(vars, nil)
		if err != nil {
			e.logger.Warn("repository filter failed to evaluate", "filter", by.Label(), "error", err)
		}
		if reason == "" {
			return false, nil
		}
		e.filter = ""
		if by != nil {
			e.filter = by.Name
		}
		if err := w.recordSkip(ctx, e, string(reason)); err != nil {
			return true, err
		}
		moved := false
		if err := w.Store.WithAccount(ctx, e.args.AccountID, func(tx pgx.Tx) error {
			var labels json.RawMessage
			if err := tx.QueryRow(ctx, `SELECT labels FROM pull_requests WHERE id = $1 FOR UPDATE`, e.pr.id).Scan(&labels); err != nil {
				return fmt.Errorf("worker: read the pull request's labels: %w", err)
			}
			if moved = !bytes.Equal(labels, judged.Labels); moved {
				return nil
			}
			_, err := river.JobCompleteTx[*riverpgxv5.Driver](ctx, tx, job)
			return err
		}); err != nil {
			return true, err
		}
		if !moved {
			return true, nil
		}
	}
}

// recordSkip records the skip for reason and says so on the head commit,
// unless it is the skip the head's latest review already is: the same
// job judging again, a retry, or a label change that left the head where
// it was. A re-run someone asked for is recorded all the same.
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
	pr, err := loadFilterPR(ctx, tx, prID)
	if err != nil {
		return nil, err
	}
	pr.Event = trigger
	return pr.Vars()
}

// loadFilterPR reads what the repository filter sees of a pull request.
func loadFilterPR(ctx context.Context, tx pgx.Tx, prID string) (repoconfig.PullRequest, error) {
	var (
		pr       repoconfig.PullRequest
		openedAt *time.Time
	)
	err := tx.QueryRow(ctx, `SELECT number, title, author, state, merged, draft, fork, head_ref, head_sha, base_ref, url, body,
		opened_at, labels FROM pull_requests WHERE id = $1`, prID).
		Scan(&pr.Number, &pr.Title, &pr.Author, &pr.State, &pr.Merged, &pr.Draft, &pr.Fork, &pr.HeadRef, &pr.HeadSHA, &pr.BaseRef,
			&pr.URL, &pr.Body, &openedAt, &pr.Labels)
	if err != nil {
		return repoconfig.PullRequest{}, fmt.Errorf("worker: read pull request for the filter: %w", err)
	}
	if openedAt != nil {
		pr.CreatedAt = *openedAt
	}
	return pr, nil
}
