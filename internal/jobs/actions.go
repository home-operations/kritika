package jobs

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

// ErrNoHead is returned when the pull request has no head to review: the
// number does not exist, or, for any review but a re-run someone asks
// for, it is closed.
var ErrNoHead = errors.New("jobs: pull request has no head to re-review")

// ErrNotCancelable is returned by RequestCancel when the review is not in a
// state a cancel can reach: it has already finished, it never recorded the
// River job it started as, or reviewID/by is not a well-formed UUID.
var ErrNotCancelable = errors.New("jobs: review is not in a cancelable state")

// ErrRerunQueued is returned by EnqueueRerun when the pull request's
// current head is already being reviewed, or a review job for it is queued:
// a second run would pay the model twice and publish its findings twice.
var ErrRerunQueued = errors.New("jobs: a review of this head is already queued or running")

// ErrRerunOther is ErrRerunQueued for a re-run whose reviewer, or every
// reviewer when it names none, is not the one the review in flight runs:
// that review does not stand in for it, and starting it would cancel that
// review, so it is refused for the one asking to try again.
var ErrRerunOther = fmt.Errorf("%w for other reviewers", ErrRerunQueued)

// ErrRepositoryNotFound is returned by EnqueueReindex when repositoryID does
// not exist in accountID.
var ErrRepositoryNotFound = errors.New("jobs: repository not found")

// ErrReindexQueued is returned by EnqueueReindex when a forced reindex of
// the repository is already queued or running; no new job was inserted.
var ErrReindexQueued = errors.New("jobs: reindex already queued")

// EnqueueRerun re-queues a review of number's current head, the way a human
// asks kritika to look again, a merged or closed pull request's too: a look
// back at what was missed, which the worker reviews with no commit status
// and no approval. It gives the job a fresh, random Request value
// so it inserts even when a review of the same head already completed,
// bypassing the push-triggered dedup that keys on
// account+repository+number+head alone; while a review of that head is
// running or prepared, or a review job for it has yet to finish, it is
// ErrRerunQueued instead. Row-level security on pull_requests and reviews
// already scopes every query to the transaction's account; the explicit
// account_id predicates are defense in depth.
func EnqueueRerun(
	ctx context.Context, tx pgx.Tx, c *river.Client[pgx.Tx], accountID, repositoryID string, number int,
) (int64, error) {
	return enqueueFresh(ctx, tx, c, accountID, repositoryID, number, TriggerManual, "", "", "")
}

// EnqueueReviewerRerun is EnqueueRerun for a re-run of reviewer alone, the
// one whose name someone wrote after "@<bot> review", or of every reviewer
// with requested, a word written there that names none, and with focus,
// what the request asks the review to look hardest at.
func EnqueueReviewerRerun(
	ctx context.Context, tx pgx.Tx, c *river.Client[pgx.Tx], accountID, repositoryID string, number int,
	reviewer, requested, focus string,
) (int64, error) {
	return enqueueFresh(ctx, tx, c, accountID, repositoryID, number, TriggerManual, reviewer, requested, focus)
}

// EnqueueLabelChange queues a review of number's current head for trigger,
// a label added or removed (see LabelChange). The head's earlier job, the
// one that ended in the skip the label may lift, still holds the key a
// push dedupes on, so the job gets a fresh Request as a re-run does, and is
// ErrRerunQueued under the same conditions.
func EnqueueLabelChange(
	ctx context.Context, tx pgx.Tx, c *river.Client[pgx.Tx], accountID, repositoryID string, number int, trigger string,
) (int64, error) {
	return enqueueFresh(ctx, tx, c, accountID, repositoryID, number, trigger, "", "", "")
}

func enqueueFresh(
	ctx context.Context, tx pgx.Tx, c *river.Client[pgx.Tx], accountID, repositoryID string, number int,
	trigger, reviewer, requested, focus string,
) (int64, error) {
	var headSHA string
	err := tx.QueryRow(ctx, `SELECT head_sha FROM pull_requests
		WHERE account_id = $1 AND repository_id = $2 AND number = $3 AND (state = 'open' OR $4)`,
		accountID, repositoryID, number, trigger == TriggerManual).Scan(&headSHA)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNoHead
	}
	if err != nil {
		return 0, fmt.Errorf("jobs: look up pull request head: %w", err)
	}
	// Two re-runs of one pull request at once would each see the other's
	// job not yet committed; the lock makes the second wait and see it.
	key := "kritika:rerun:" + accountID + ":" + repositoryID + ":" + strconv.Itoa(number)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, key); err != nil {
		return 0, fmt.Errorf("jobs: lock pull request: %w", err)
	}
	// A push's review still waiting its settle is not one to stand in for
	// a review someone asks for now: the request takes its place.
	if trigger == TriggerManual {
		if err := cancelWaiting(ctx, tx, c, accountID, repositoryID, number, headSHA); err != nil {
			return 0, err
		}
	}
	// same is whether a live job of the head runs the reviewers this one
	// would, with the focus it would, which then stands in for it; a
	// review row with no live job is taken to.
	var busy, same bool
	err = tx.QueryRow(ctx, `WITH live AS (
			SELECT coalesce(args->>'reviewer', '') AS reviewer, coalesce(args->>'focus', '') AS focus FROM river_job
			WHERE kind IN ($5, 'review_prepare', 'review_publish') AND state IN (`+LiveStatesSQL()+`)
				AND args->>'account_id' = $1::text AND args->>'repository_id' = $2::text AND (args->>'number')::int = $3
				AND args->>'head_sha' = $4)
		SELECT EXISTS (
			SELECT 1 FROM reviews r JOIN pull_requests p ON p.id = r.pull_request_id
			WHERE p.account_id = $1::text::uuid AND p.repository_id = $2::text::uuid AND p.number = $3 AND r.head_sha = $4
				AND r.status IN ('running', 'prepared'))
			OR EXISTS (SELECT 1 FROM live),
			NOT EXISTS (SELECT 1 FROM live) OR EXISTS (SELECT 1 FROM live WHERE reviewer = $6 AND focus = $7)`,
		accountID, repositoryID, number, headSHA, ReviewArgs{}.Kind(), reviewer, focus).Scan(&busy, &same)
	if err != nil {
		return 0, fmt.Errorf("jobs: look up running reviews: %w", err)
	}
	switch {
	case busy && same:
		return 0, ErrRerunQueued
	case busy:
		return 0, ErrRerunOther
	}
	res, err := c.InsertTx(ctx, tx, ReviewArgs{
		AccountID: accountID, RepositoryID: repositoryID, Number: number, HeadSHA: headSHA,
		Trigger: trigger, Request: uuid.NewString(), Reviewer: reviewer, Requested: requested, Focus: focus,
	}, nil)
	if err != nil {
		return 0, fmt.Errorf("jobs: enqueue rerun: %w", err)
	}
	return res.Job.ID, nil
}

// cancelWaiting cancels the review jobs of the head that a push queued
// and that are scheduled for later, waiting their trigger.settle. A job
// River holds, about to start, is left to it: the live check then finds
// it and the request stands behind it as it would behind any review in
// flight. A review someone asked for that waits for a model slot is not
// a push's, and is not touched.
func cancelWaiting(
	ctx context.Context, tx pgx.Tx, c *river.Client[pgx.Tx], accountID, repositoryID string, number int, headSHA string,
) error {
	rows, err := tx.Query(ctx, `SELECT id FROM river_job
		WHERE kind = $5 AND state = 'scheduled' AND scheduled_at > now() AND args->>'trigger' = $6
			AND args->>'account_id' = $1::text AND args->>'repository_id' = $2::text AND (args->>'number')::int = $3
			AND args->>'head_sha' = $4
		FOR UPDATE SKIP LOCKED`,
		accountID, repositoryID, number, headSHA, ReviewArgs{}.Kind(), TriggerSynchronize)
	if err != nil {
		return fmt.Errorf("jobs: look up waiting reviews: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return fmt.Errorf("jobs: look up waiting reviews: %w", err)
	}
	for _, id := range ids {
		if _, err := c.JobCancelTx(ctx, tx, id); err != nil {
			return fmt.Errorf("jobs: cancel waiting review: %w", err)
		}
	}
	return nil
}

// HeadQueued reports whether a review job for the pull request's head has
// yet to finish, whatever Request it carries.
func HeadQueued(ctx context.Context, tx pgx.Tx, accountID, repositoryID string, number int, headSHA string) (bool, error) {
	var queued bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM river_job
		WHERE kind IN ($5, 'review_prepare', 'review_publish') AND state IN (`+LiveStatesSQL()+`)
			AND args->>'account_id' = $1::text AND args->>'repository_id' = $2::text AND (args->>'number')::int = $3
			AND args->>'head_sha' = $4)`,
		accountID, repositoryID, number, headSHA, ReviewArgs{}.Kind()).Scan(&queued)
	if err != nil {
		return false, fmt.Errorf("jobs: look up queued reviews: %w", err)
	}
	return queued, nil
}

// PullQueued reports whether a review job for any head of the pull request
// has yet to finish.
func PullQueued(ctx context.Context, tx pgx.Tx, accountID, repositoryID string, number int) (bool, error) {
	var queued bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM river_job
		WHERE kind IN ($4, 'review_prepare', 'review_publish') AND state IN (`+LiveStatesSQL()+`)
			AND args->>'account_id' = $1::text AND args->>'repository_id' = $2::text AND (args->>'number')::int = $3)`,
		accountID, repositoryID, number, ReviewArgs{}.Kind()).Scan(&queued)
	if err != nil {
		return false, fmt.Errorf("jobs: look up queued reviews: %w", err)
	}
	return queued, nil
}

// RequestCancel asks the worker running reviewID to stop. It only applies
// when the review is still running or queued (prepared) and recorded the
// River job it started as, and that job has not ended; otherwise there is
// nothing a cancel can reach.
// The row update and the JobCancelTx call share tx, so a rollback undoes
// both together.
func RequestCancel(ctx context.Context, tx pgx.Tx, c *river.Client[pgx.Tx], reviewID string) error {
	if u, err := uuid.Parse(reviewID); err != nil || u.String() != reviewID {
		return ErrNotCancelable
	}
	var jobID int64
	var prepared bool
	err := tx.QueryRow(ctx, `UPDATE reviews SET cancel_requested_at = now()
		WHERE id = $1 AND status IN ('running', 'prepared') AND river_job_id IS NOT NULL
		RETURNING river_job_id, status = 'prepared'`, reviewID).Scan(&jobID, &prepared)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotCancelable
	}
	if err != nil {
		return fmt.Errorf("jobs: request cancel: %w", err)
	}
	// A job that already ended can no longer end its review: JobCancelTx
	// would return it unchanged, and nothing would ever act on the request.
	var state string
	err = tx.QueryRow(ctx, `SELECT state FROM river_job WHERE id = $1 FOR UPDATE`, jobID).Scan(&state)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotCancelable
	}
	if err != nil {
		return fmt.Errorf("jobs: read job state: %w", err)
	}
	switch rivertype.JobState(state) {
	case rivertype.JobStateCancelled, rivertype.JobStateCompleted, rivertype.JobStateDiscarded:
		return ErrNotCancelable
	}
	// Prepared reviews finish cancellation in the worker. Marking their River
	// job canceled could discard a delivery error before that worker sees it.
	if prepared {
		if rivertype.JobState(state) == rivertype.JobStateRunning {
			return nil
		}
		if _, err := c.JobRetryTx(ctx, tx, jobID); err != nil {
			return fmt.Errorf("jobs: wake publication cancellation: %w", err)
		}
		return nil
	}
	if _, err := c.JobCancelTx(ctx, tx, jobID); err != nil {
		if errors.Is(err, river.ErrNotFound) {
			return ErrNotCancelable
		}
		return fmt.Errorf("jobs: cancel job: %w", err)
	}
	return nil
}

// EnqueueReindex forces a full reindex of a repository even when an active
// generation already covers its current commit. Returns ErrRepositoryNotFound
// if repositoryID does not exist in accountID, and ErrReindexQueued if a
// forced reindex of the repository is already queued or running: an
// onboarding or push job does not stand in for one.
func EnqueueReindex(ctx context.Context, tx pgx.Tx, c *river.Client[pgx.Tx], accountID, repositoryID string) (int64, error) {
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM repositories WHERE account_id = $1 AND id = $2)`,
		accountID, repositoryID).Scan(&exists); err != nil {
		return 0, fmt.Errorf("jobs: look up repository: %w", err)
	}
	if !exists {
		return 0, ErrRepositoryNotFound
	}
	res, err := c.InsertTx(ctx, tx, IndexArgs{
		AccountID: accountID, RepositoryID: repositoryID, CommitSHA: "", Trigger: TriggerReindex, Full: true,
	}, nil)
	if err != nil {
		return 0, fmt.Errorf("jobs: enqueue reindex: %w", err)
	}
	if res.UniqueSkippedAsDuplicate {
		return 0, ErrReindexQueued
	}
	return res.Job.ID, nil
}
