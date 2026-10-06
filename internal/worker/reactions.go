package worker

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/forge"
)

// marks are the bot's reactions on what it works on, a pull request under
// review or a mention it answers: forge.ReactionEyes while it works, then
// forge.ReactionDone once it has answered. react and unreact add and remove
// one reaction there.
type marks struct {
	react   func(ctx context.Context, content string) (int64, error)
	unreact func(ctx context.Context, id int64) error
	logger  *slog.Logger
}

// start leaves the eyes and returns what ends them: the eyes come off,
// unless busy says another job still works on the same thing, and with
// answered the thumbs up goes on. The forge keeps one reaction of a kind
// per user, so the eyes are shared by every job on the thing, and a forge
// hands back the eyes an earlier attempt left, so a retry ends them too.
// Best effort: a forge that refuses a reaction, as GitHub does on an issue
// or a conversation comment without write access to issues, costs the
// marks and nothing else. The end lands even once the job's ctx has ended.
func (m marks) start(ctx context.Context) (end func(answered, busy bool)) {
	id, err := m.react(ctx, forge.ReactionEyes)
	if err != nil {
		m.logger.Info("not marked as being worked on", "error", err)
		return func(bool, bool) {}
	}
	return func(answered, busy bool) {
		dctx, cancel := detach(ctx)
		defer cancel()
		if !busy {
			if err := m.unreact(dctx, id); err != nil {
				m.logger.Warn("left marked as being worked on", "error", err)
			}
		}
		if !answered {
			return
		}
		if _, err := m.react(dctx, forge.ReactionDone); err != nil {
			m.logger.Warn("not marked as answered", "error", err)
		}
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

// pullMarks are the marks on a pull request.
func pullMarks(client forge.Client, owner, repo string, number int, logger *slog.Logger) marks {
	return marks{
		react: func(ctx context.Context, content string) (int64, error) {
			return client.ReactToPullRequest(ctx, owner, repo, number, content)
		},
		unreact: func(ctx context.Context, id int64) error {
			return client.UnreactToPullRequest(ctx, owner, repo, number, id)
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
		logger:  logger,
	}
}
