package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/forge"
	"github.com/home-operations/kritika/internal/jobs"
	"github.com/home-operations/kritika/internal/store"
)

// pullRequest is what the worker reads back before starting.
type pullRequest struct {
	id, repositoryID string
	repository       string
	number           int
	headSHA, baseRef string
	title, author    string
	authorIsBot      bool
	// closed is why it takes no more reviews, "" while it is open.
	closed string
}

// dedupesBotPatch says whether an unchanged patch from a bot author skips
// the review. A manual re-run bypasses the skip: the human asked for it, so
// an identical bot patch is reviewed again rather than deduped away.
func (pr *pullRequest) dedupesBotPatch(trigger string) bool {
	return pr.authorIsBot && trigger != jobs.TriggerManual
}

// ownerRepo splits the repository's full name as the forge client takes it.
func (pr *pullRequest) ownerRepo() (owner, repo string) {
	owner, repo, _ = strings.Cut(pr.repository, "/")
	return owner, repo
}

// loadPullRequest reads a job's pull request. One the store does not know
// cancels the job: it will not appear by retrying.
func loadPullRequest(ctx context.Context, st *store.Store, accountID, repositoryID string, number int) (*pullRequest, error) {
	var row store.ReviewPullRequest
	err := st.WithAccount(ctx, accountID, func(tx pgx.Tx) error {
		var err error
		row, err = store.FindReviewPullRequest(ctx, tx, repositoryID, number)
		return err
	})
	if errors.Is(err, store.ErrNotFound) {
		return nil, river.JobCancel(fmt.Errorf("worker: pull request %d of %s is unknown", number, repositoryID))
	}
	if err != nil {
		return nil, fmt.Errorf("worker: load pull request: %w", err)
	}
	return &pullRequest{id: row.ID, repositoryID: row.RepositoryID, repository: row.Repository, number: row.Number,
		headSHA: row.HeadSHA, baseRef: row.BaseRef, title: row.Title, author: row.Author,
		authorIsBot: row.AuthorIsBot, closed: closedReason(row)}, nil
}

// closedReason is why the pull request row takes no more reviews, "" while
// it is open.
func closedReason(row store.ReviewPullRequest) string {
	switch {
	case row.Merged:
		return "the pull request was merged"
	case row.State != "open":
		return "the pull request is closed"
	}
	return ""
}

// refreshPullRequest asks the forge whether pr, which the store holds open,
// is still open. If it is not, refreshPullRequest records it as closed and
// returns the reason that it takes no more reviews, as loadPullRequest does.
// Otherwise it returns "". A job trusts the row from the event that queued it
// only until the job first contacts the forge. Anything can happen in
// between: for example, a replica can be down when the closed event arrives.
// Otherwise, the row is the only place from which the job reads the state.
//
// The forge is not asked for a pull request that the store already holds
// closed. A pull request that the forge does not know is left for the poll to
// sort out. One that cannot be reached fails the attempt, as the job's own
// forge calls do. The reason is the row's once the close is recorded: a
// reopening that the row already knows of, newer than what the forge
// answered, keeps it open.
func (b *Base) refreshPullRequest(
	ctx context.Context, client forge.Client, accountID string, pr *pullRequest, logger *slog.Logger,
) (string, error) {
	if pr.closed != "" {
		return pr.closed, nil
	}
	owner, repo := pr.ownerRepo()
	fresh, err := client.PullRequest(ctx, owner, repo, pr.number)
	if errors.Is(err, forge.ErrNotFound) {
		logger.Info("pull request not refreshed: the forge does not know it")
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("worker: refresh pull request: %w", err)
	}
	if fresh.State != "closed" {
		return "", nil
	}
	var row store.ReviewPullRequest
	err = b.Store.WithAccount(ctx, accountID, func(tx pgx.Tx) error {
		if err := store.ClosePullRequest(ctx, tx, pr.repositoryID, pr.number, fresh.Merged, fresh.ClosedAt, fresh.UpdatedAt); err != nil {
			return err
		}
		var err error
		row, err = store.FindReviewPullRequest(ctx, tx, pr.repositoryID, pr.number)
		return err
	})
	if err != nil {
		return "", fmt.Errorf("worker: refresh pull request: %w", err)
	}
	closed := closedReason(row)
	if closed != "" {
		logger.Info("pull request closed since its job was queued", "merged", row.Merged)
	}
	return closed, nil
}

// skipClosed ends e's review, of a pull request merged or closed since the
// event that queued it, as skipped. An automatic review has nothing left to
// do. No status is set on a head that merges no more, unless an earlier
// attempt of the job set one (see closedUnderJob). client is the forge client
// that the caller has, nil before one is built. The end is written under the
// pull request's publication lock, which a review's start holds while it sets
// its own pending status. skipClosed makes sure inside that lock that the job
// owns the status: a newer review that starts meanwhile, on a pull request
// reopened since, keeps the status it set.
func (w *Review) skipClosed(
	ctx context.Context, e earlyEnd, jobID int64, client forge.Client, file *configfile.File, account *configfile.Account,
) error {
	e.logger.Info("review skipped before start", "reason", e.pr.closed)
	return lockedWrite(ctx, w.Store.WithPublicationLock, e.pr.id, func(ctx context.Context) error {
		if err := w.closedUnderJob(ctx, &e, jobID, client, file, account); err != nil {
			return err
		}
		return w.end(ctx, e, store.ReviewSkipped, e.pr.closed)
	})
}

// closedUnderJob gives e the client to report its end on the head when an
// earlier attempt of the job started a review there. That is a review of an
// open pull request, whose pending status a replica that died left on the
// head. The pull request closed under that review before the retry. The
// retry's skip is then what replaces the status. The eyes that the earlier
// attempt left come off with it, unless another review of the pull request
// runs. A job whose earlier attempts started nothing sets no status on a head
// that merges no more.
func (w *Review) closedUnderJob(
	ctx context.Context, e *earlyEnd, jobID int64, client forge.Client, file *configfile.File, account *configfile.Account,
) error {
	var owns bool
	err := w.Store.WithAccount(ctx, e.args.AccountID, func(tx pgx.Tx) error {
		var err error
		owns, err = store.JobOwnsStatus(ctx, tx, jobID)
		return err
	})
	if err != nil || !owns {
		return err
	}
	if client == nil {
		if client, err = w.client(ctx, file, account, e.pr.repository); err != nil {
			return err
		}
	}
	e.client = client
	e.owner, e.repo = e.pr.ownerRepo()
	if !w.otherReviewRunning(ctx, e.logger, e.args.AccountID, e.pr.id, "") {
		pullMarks(client, e.owner, e.repo, e.pr.number, e.logger).remove(ctx, forge.ReactionEyes)
	}
	return nil
}
