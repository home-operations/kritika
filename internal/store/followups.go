package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/home-operations/kritika/internal/tuning"
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
// A failed attempt cannot overwrite an acknowledgment whose commit succeeded
// even if its caller received an error from Commit.
func RecordFollowup(ctx context.Context, tx pgx.Tx, r FollowupResult) error {
	_, err := tx.Exec(ctx, `INSERT INTO followups
		(account_id, pull_request_id, comment_id, author, inline, path, line, status, reason, reply_comment_id, model)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, left($9, $12), nullif($10::bigint, 0), $11)
		ON CONFLICT (pull_request_id, comment_id, inline) DO UPDATE SET status = excluded.status, reason = excluded.reason,
			reply_comment_id = coalesce(excluded.reply_comment_id, followups.reply_comment_id), model = excluded.model
		WHERE excluded.status <> 'failed' OR (followups.status <> 'answered' AND followups.reply_comment_id IS NULL)`,
		r.AccountID, r.PullRequestID, r.CommentID, r.Author, r.Inline, r.Path, r.Line,
		r.Status, r.Reason, r.ReplyCommentID, r.Model, tuning.Get().Store.FollowupReasonChars)
	if err != nil {
		return fmt.Errorf("store: record follow-up: %w", err)
	}
	return nil
}
