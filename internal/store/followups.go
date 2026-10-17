package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

type FollowupState struct {
	Status         FollowupStatus
	Reason         string
	ReplyCommentID *int64
}

// FindFollowup returns ErrNotFound when the comment has no recorded outcome.
func FindFollowup(ctx context.Context, tx pgx.Tx, prID string, commentID int64, inline bool) (FollowupState, error) {
	var state FollowupState
	err := tx.QueryRow(ctx, `SELECT status, reason, reply_comment_id FROM followups
		WHERE pull_request_id = $1 AND comment_id = $2 AND inline = $3`,
		prID, commentID, inline).Scan(&state.Status, &state.Reason, &state.ReplyCommentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return state, ErrNotFound
	}
	if err != nil {
		return state, fmt.Errorf("store: read follow-up: %w", err)
	}
	return state, nil
}

// RecentFollowupCounts counts answers and limit notices in the last hour,
// using the same database clock that timestamps their rows. It holds the
// pull request's follow-up lock until tx ends, so mentions handled at once
// count in turn, each after what the one before it recorded in its tx.
func RecentFollowupCounts(ctx context.Context, tx pgx.Tx, prID string) (answered, notices int, err error) {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('kritika-followups:' || $1, 0))`, prID); err != nil {
		return 0, 0, fmt.Errorf("store: lock follow-ups: %w", err)
	}
	err = tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status = 'answered'), count(*) FILTER (WHERE status = 'limited')
		FROM followups WHERE pull_request_id = $1 AND created_at > now() - interval '1 hour'`, prID).Scan(&answered, &notices)
	if err != nil {
		return 0, 0, fmt.Errorf("store: count follow-ups: %w", err)
	}
	return answered, notices, nil
}

type FollowupResult struct {
	AccountID, PullRequestID string
	CommentID                int64
	Author                   string
	Inline                   bool
	Path                     string
	Line                     int
	Status                   FollowupStatus
	Reason                   string
	ReplyCommentID           int64
	Model                    string
}

// RecordFollowup keeps an existing reply when an update has no new reply ID.
// The original creation time remains the basis for the hourly limit, but a
// mention limited on a retry counts from then, as RecentFollowupCounts'
// notices must for the limit notice to be posted once an hour.
// A failed attempt cannot overwrite an acknowledgment whose commit succeeded
// even if its caller received an error from Commit.
func RecordFollowup(ctx context.Context, tx pgx.Tx, r FollowupResult) error {
	_, err := tx.Exec(ctx, `INSERT INTO followups
		(account_id, pull_request_id, comment_id, author, inline, path, line, status, reason, reply_comment_id, model)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, left($9, 500), nullif($10::bigint, 0), $11)
		ON CONFLICT (pull_request_id, comment_id, inline) DO UPDATE SET status = excluded.status, reason = excluded.reason,
			reply_comment_id = coalesce(excluded.reply_comment_id, followups.reply_comment_id), model = excluded.model,
			created_at = CASE WHEN excluded.status = 'limited' AND followups.status <> 'limited' THEN now() ELSE followups.created_at END
		WHERE excluded.status <> 'failed' OR
			(followups.status NOT IN ('answered', 'limited') AND followups.reply_comment_id IS NULL)`,
		r.AccountID, r.PullRequestID, r.CommentID, r.Author, r.Inline, r.Path, r.Line,
		r.Status, r.Reason, r.ReplyCommentID, r.Model)
	if err != nil {
		return fmt.Errorf("store: record follow-up: %w", err)
	}
	return nil
}
