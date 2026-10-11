package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Publication is the checkpoint a review job resumes without model work.
// Terminal checkpoints remain readable so a retry after completion is a no-op.
type Publication struct {
	ReviewID, PullRequestID, HeadSHA string
	Payload                          json.RawMessage
	Status                           ReviewStatus
}

// SavePublication keeps the first finalized answer of a prepared review.
func SavePublication(ctx context.Context, tx pgx.Tx, reviewID string, payload json.RawMessage) error {
	tag, err := tx.Exec(ctx, `UPDATE reviews SET publication = $2
		WHERE id = $1 AND status = 'prepared' AND publication IS NULL`, reviewID, payload)
	if err != nil {
		return fmt.Errorf("store: save publication: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return errors.New("store: review is not ready for a publication checkpoint")
	}
	return nil
}

// ReadPublication returns the checkpoint of jobID, or ErrNotFound before
// the job has finalized its answer.
func ReadPublication(ctx context.Context, tx pgx.Tx, jobID int64) (Publication, error) {
	var p Publication
	err := tx.QueryRow(ctx, `SELECT id, pull_request_id, head_sha, publication, status FROM reviews
		WHERE river_job_id = $1 AND publication IS NOT NULL ORDER BY created_at DESC LIMIT 1`, jobID).
		Scan(&p.ReviewID, &p.PullRequestID, &p.HeadSHA, &p.Payload, &p.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrNotFound
	}
	if err != nil {
		return p, fmt.Errorf("store: read publication: %w", err)
	}
	return p, nil
}

// LockPublication serializes write-back for a pull request. It does not
// lock its row, so ingest and cancellation remain able to update it.
func LockPublication(ctx context.Context, tx pgx.Tx, pullRequestID string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "kritika:publish:"+pullRequestID)
	if err != nil {
		return fmt.Errorf("store: lock publication: %w", err)
	}
	return nil
}

// PublicationCurrent refuses an old head or an answer overtaken by a newer
// completed review, including another review of the same head.
func PublicationCurrent(ctx context.Context, tx pgx.Tx, reviewID string) (bool, error) {
	var current bool
	err := tx.QueryRow(ctx, `SELECT r.status = 'prepared' AND r.head_sha = p.head_sha
		AND NOT EXISTS (SELECT 1 FROM reviews newer WHERE newer.pull_request_id = r.pull_request_id
			AND newer.status = 'completed' AND newer.created_at > r.created_at)
		FROM reviews r JOIN pull_requests p ON p.id = r.pull_request_id WHERE r.id = $1`, reviewID).Scan(&current)
	if err != nil {
		return false, fmt.Errorf("store: check publication head: %w", err)
	}
	return current, nil
}

// CompletePublication records delivery, its automatic-review count and its
// terminal status in one transaction. Repeating it changes nothing.
func CompletePublication(ctx context.Context, tx pgx.Tx, result ReviewResult, automatic, pause bool) (bool, error) {
	var status ReviewStatus
	if err := tx.QueryRow(ctx, `SELECT status FROM reviews WHERE id = $1 FOR UPDATE`, result.ReviewID).Scan(&status); err != nil {
		return false, fmt.Errorf("store: lock published review: %w", err)
	}
	if status != ReviewPrepared {
		return false, nil
	}
	if err := RecordReviewResult(ctx, tx, result); err != nil {
		return false, err
	}
	if automatic {
		if _, err := CountAutoReview(ctx, tx, result.PullRequestID, pause); err != nil {
			return false, err
		}
	}
	return EndReview(ctx, tx, result.ReviewID, ReviewEnd{Status: ReviewCompleted, OnlyUnfinished: true})
}
