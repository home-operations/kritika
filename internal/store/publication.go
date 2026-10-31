package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Publication is the checkpoint a review job resumes without another runner.
// Terminal checkpoints keep an empty marker so a retry after completion is a no-op.
type Publication struct {
	JobID                            int64
	CancelRequested                  bool
	ReviewID, PullRequestID, HeadSHA string
	RepositoryID, Repository, Closed string
	Number                           int
	AuthorIsBot                      bool
	Payload                          json.RawMessage
	Status                           ReviewStatus
}

// SavePublication keeps the first answer checkpoint of a prepared review.
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

// ReplacePublication advances the checkpoint only if this attempt still owns
// the saved input. It cannot overwrite another publisher's finalized answer.
func ReplacePublication(ctx context.Context, tx pgx.Tx, reviewID string, previous, payload json.RawMessage) error {
	tag, err := tx.Exec(ctx, `UPDATE reviews SET publication = $3
		WHERE id = $1 AND status = 'prepared' AND publication = $2::jsonb`, reviewID, previous, payload)
	if err != nil {
		return fmt.Errorf("store: replace publication: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return errors.New("store: publication checkpoint changed")
	}
	return nil
}

// ReadPublication returns the checkpoint or prepared cancellation of jobID,
// or ErrNotFound before either is recorded. Closed is set only for a review
// that began after its pull request was merged or closed, a look back that
// sets no commit status: one the pull request closed under keeps the
// pending status it set, which its outcome has to replace. The job is
// matched through a semi-join rather than an OR of the two columns, which
// the planner cannot turn into index lookups.
func ReadPublication(ctx context.Context, tx pgx.Tx, jobID int64) (Publication, error) {
	var p Publication
	err := tx.QueryRow(ctx, `SELECT r.river_job_id, r.id, r.pull_request_id, r.head_sha, r.publication, r.status,
		r.cancel_requested_at IS NOT NULL, p.repository_id, repo.name, p.number, p.author_is_bot,
		CASE WHEN p.state = 'open' OR r.created_at < p.closed_at THEN ''
			WHEN p.merged THEN 'the pull request was merged' ELSE 'the pull request is closed' END
		FROM reviews r JOIN pull_requests p ON p.id = r.pull_request_id JOIN repositories repo ON repo.id = p.repository_id
		WHERE r.id IN (SELECT id FROM reviews WHERE river_job_id = $1
			UNION SELECT review_id FROM runner_runs WHERE river_job_id = $1 AND review_id IS NOT NULL)
		AND (r.publication IS NOT NULL OR (r.status = 'prepared' AND r.cancel_requested_at IS NOT NULL))
		ORDER BY r.created_at DESC LIMIT 1`, jobID).
		Scan(&p.JobID, &p.ReviewID, &p.PullRequestID, &p.HeadSHA, &p.Payload, &p.Status, &p.CancelRequested,
			&p.RepositoryID, &p.Repository, &p.Number, &p.AuthorIsBot, &p.Closed)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrNotFound
	}
	if err != nil {
		return p, fmt.Errorf("store: read publication: %w", err)
	}
	return p, nil
}

// AdvancePublication transfers a prepared review to the next durable stage.
func AdvancePublication(ctx context.Context, tx pgx.Tx, reviewID string, previous, next int64) error {
	tag, err := tx.Exec(ctx, `UPDATE reviews SET river_job_id = $3
		WHERE id = $1 AND river_job_id = $2 AND status = 'prepared' AND publication IS NOT NULL`, reviewID, previous, next)
	if err != nil {
		return fmt.Errorf("store: advance publication: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return errors.New("store: publication ownership changed")
	}
	return nil
}

// ErrPublicationBusy means another publisher owns this pull request.
var ErrPublicationBusy = errors.New("store: publication is busy")

// WithPublicationLock holds a session lock on a dedicated connection. Forge
// calls must not hold an application-pool connection or an open transaction:
// heartbeats and cancellation need that pool while delivery is in progress.
func (s *Store) WithPublicationLock(ctx context.Context, pullRequestID string, fn func(context.Context) error) (err error) {
	attemptCtx, cancelAttempt := context.WithTimeout(ctx, lockTimeout)
	defer cancelAttempt()
	cfg := s.app.Config().ConnConfig
	cfg.RuntimeParams["application_name"] = "kritika-publication"
	conn, err := pgx.ConnectConfig(attemptCtx, cfg)
	if err != nil {
		return fmt.Errorf("store: publication connection: %w", err)
	}
	defer func() {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := conn.Close(cctx); err != nil {
			s.logger.Warn("publication connection not closed cleanly", "error", err)
		}
	}()
	var locked bool
	err = conn.QueryRow(attemptCtx, `SELECT pg_try_advisory_lock(hashtextextended($1, 0))`, "kritika:publish:"+pullRequestID).Scan(&locked)
	if err != nil {
		return fmt.Errorf("store: lock publication: %w", err)
	}
	if !locked {
		return ErrPublicationBusy
	}
	cancelAttempt()
	lockCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	done := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(lockTimeout)
		defer ticker.Stop()
		for {
			select {
			case <-lockCtx.Done():
				done <- nil
				return
			case <-ticker.C:
				pingCtx, cancelPing := context.WithTimeout(lockCtx, lockTimeout)
				err := conn.Ping(pingCtx)
				cancelPing()
				if err != nil {
					if lockCtx.Err() != nil {
						done <- nil
						return
					}
					err = fmt.Errorf("store: publication lock connection lost: %w", err)
					cancel(err)
					done <- err
					return
				}
			}
		}
	}()
	defer func() {
		cancel(nil)
		err = errors.Join(err, <-done)
	}()
	return fn(lockCtx)
}

// PublicationCurrent refuses an old head or an answer overtaken by a newer
// completed review. This includes another review of the same head.
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
