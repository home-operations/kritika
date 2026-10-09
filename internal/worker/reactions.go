package worker

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/forge"
	"github.com/home-operations/kritika/internal/store"
)

// outcome is what became of the work the marks are on.
type outcome int

const (
	// unanswered says nothing: the work was superseded, skipped or
	// canceled, or is retried.
	unanswered outcome = iota
	// answered: a review was posted, or a mention answered.
	answered
	// failed: the review, or the answer, failed.
	failed
)

// marks are the bot's reactions on what it works on, a pull request under
// review or a mention it answers: forge.ReactionEyes while it works, then
// forge.ReactionDone once it has answered or forge.ReactionFailed when it
// could not. react and unreact add and remove one reaction there, and find
// returns the id of the bot's reaction of a content there, 0 for none, so
// the mark an earlier outcome left can come off.
type marks struct {
	react   func(ctx context.Context, content string) (int64, error)
	unreact func(ctx context.Context, id int64) error
	find    func(ctx context.Context, content string) (int64, error)
	logger  *slog.Logger
}

// start leaves the eyes and returns what ends them: the eyes come off,
// unless busy says another job still works on the same thing, and the
// outcome's mark goes on as the other outcome's comes off. The forge keeps
// one reaction of a kind per user, so the eyes are shared by every job on
// the thing, and a forge hands back the eyes an earlier attempt left, so a
// retry ends them too. Best effort: a forge that refuses a reaction, as
// GitHub does on an issue or a conversation comment without write access
// to issues, costs the marks and nothing else. The end lands even once the
// job's ctx has ended.
func (m marks) start(ctx context.Context) (end func(o outcome, busy bool)) {
	id, err := m.react(ctx, forge.ReactionEyes)
	if err != nil {
		m.logger.Info("not marked as being worked on", "error", err)
		return func(outcome, bool) {}
	}
	return func(o outcome, busy bool) {
		dctx, cancel := detach(ctx)
		defer cancel()
		if !busy {
			if err := m.unreact(dctx, id); err != nil {
				m.logger.Warn("left marked as being worked on", "error", err)
			}
		}
		m.settle(dctx, o)
	}
}

// settle puts the outcome's mark on and takes the other outcome's off;
// unanswered changes neither.
func (m marks) settle(ctx context.Context, o outcome) {
	var on, off string
	switch o {
	case answered:
		on, off = forge.ReactionDone, forge.ReactionFailed
	case failed:
		on, off = forge.ReactionFailed, forge.ReactionDone
	default:
		return
	}
	if _, err := m.react(ctx, on); err != nil {
		m.logger.Warn("outcome not marked", "reaction", on, "error", err)
	}
	m.remove(ctx, off)
}

// remove takes the bot's reaction of content off, when it has one.
func (m marks) remove(ctx context.Context, content string) {
	id, err := m.find(ctx, content)
	if err == nil && id != 0 {
		err = m.unreact(ctx, id)
	}
	if err != nil {
		m.logger.Warn("earlier outcome's mark not removed", "reaction", content, "error", err)
	}
}

// otherReviewRunning reports whether a review of the pull request other
// than reviewID is running: a newer head's, which the pull request's eyes
// then still belong to. Not knowing counts as none, since that review ends
// the eyes itself when it is done.
func (b *Base) otherReviewRunning(ctx context.Context, logger *slog.Logger, accountID, prID, reviewID string) bool {
	var running bool
	err := b.Store.WithAccount(ctx, accountID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM reviews WHERE pull_request_id = $1 AND id::text <> $2 AND status = 'running')`,
			prID, reviewID).Scan(&running)
	})
	if err != nil {
		logger.Warn("running reviews not read", "error", err)
		return false
	}
	return running
}

// reviewOutcome is what the marks say of a review once its job is done
// with jobErr: answered when it posted, failed when it failed, or when
// jobErr ended the last attempt and the review is left as it is, and
// unanswered otherwise, as when it was superseded, skipped or canceled, or
// its job is retried, whatever the retried attempt's review says. A
// failure of a head the pull request has moved past is unanswered too:
// the newer head's review speaks for the pull request, and may already
// have. Not knowing counts as unanswered.
func (b *Base) reviewOutcome(ctx context.Context, logger *slog.Logger, accountID, reviewID string, jobErr error, lastAttempt bool) outcome {
	if jobErr != nil && !lastAttempt {
		return unanswered
	}
	var status string
	var current bool
	err := b.Store.WithAccount(ctx, accountID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT r.status, r.head_sha = p.head_sha FROM reviews r
			JOIN pull_requests p ON p.id = r.pull_request_id WHERE r.id = $1`, reviewID).Scan(&status, &current)
	})
	if err != nil {
		logger.Warn("review status not read", "error", err)
		return unanswered
	}
	switch {
	case jobErr == nil && store.ReviewStatus(status) == store.ReviewCompleted:
		return answered
	case current && (jobErr != nil || store.ReviewStatus(status) == store.ReviewFailed):
		return failed
	}
	return unanswered
}

// pullMarks are the marks on a pull request.
func pullMarks(client forge.Client, owner, repo string, number int, logger *slog.Logger) marks {
	return marks{
		react: func(ctx context.Context, content string) (int64, error) {
			return client.ReactToPullRequest(ctx, owner, repo, number, content)
		},
		unreact: func(ctx context.Context, id int64) error {
			return client.UnreactToPullRequest(ctx, owner, repo, number, id)
		},
		find: func(ctx context.Context, content string) (int64, error) {
			return client.PullRequestReaction(ctx, owner, repo, number, content)
		},
		logger: logger,
	}
}

// commentMarks are the marks on a comment.
func commentMarks(client forge.Client, owner, repo string, comment forge.Comment, logger *slog.Logger) marks {
	return marks{
		react: func(ctx context.Context, content string) (int64, error) {
			return client.React(ctx, owner, repo, comment, content)
		},
		unreact: func(ctx context.Context, id int64) error { return client.Unreact(ctx, owner, repo, comment, id) },
		find: func(ctx context.Context, content string) (int64, error) {
			return client.Reaction(ctx, owner, repo, comment, content)
		},
		logger: logger,
	}
}
