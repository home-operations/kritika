package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/repoconfig"
)

type ReviewPullRequest struct {
	ID, RepositoryID, Repository    string
	Number                          int
	HeadSHA, BaseRef, Title, Author string
	AuthorIsBot                     bool
	State                           string
	Merged                          bool
}

func FindReviewPullRequest(ctx context.Context, tx pgx.Tx, repositoryID string, number int) (ReviewPullRequest, error) {
	var pr ReviewPullRequest
	err := tx.QueryRow(ctx, `SELECT p.id, p.repository_id, r.name, p.number, p.head_sha, p.base_ref, p.title, p.author,
		p.author_is_bot, p.state, p.merged
		FROM pull_requests p JOIN repositories r ON r.id = p.repository_id
		WHERE p.repository_id = $1 AND p.number = $2`, repositoryID, number).
		Scan(&pr.ID, &pr.RepositoryID, &pr.Repository, &pr.Number, &pr.HeadSHA, &pr.BaseRef, &pr.Title, &pr.Author,
			&pr.AuthorIsBot, &pr.State, &pr.Merged)
	if errors.Is(err, pgx.ErrNoRows) {
		return pr, ErrNotFound
	}
	if err != nil {
		return pr, fmt.Errorf("store: read review pull request: %w", err)
	}
	return pr, nil
}

func PullRequestHead(ctx context.Context, tx pgx.Tx, prID string) (string, error) {
	var head string
	if err := tx.QueryRow(ctx, `SELECT head_sha FROM pull_requests WHERE id = $1`, prID).Scan(&head); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("store: read pull request head: %w", err)
	}
	return head, nil
}

func PullRequestBody(ctx context.Context, tx pgx.Tx, prID string) (string, error) {
	var body string
	if err := tx.QueryRow(ctx, `SELECT body FROM pull_requests WHERE id = $1`, prID).Scan(&body); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("store: read pull request body: %w", err)
	}
	return body, nil
}

// LockPullRequestLabels holds the row through the caller's transaction so
// the label check can be atomic with job completion.
func LockPullRequestLabels(ctx context.Context, tx pgx.Tx, prID string) (json.RawMessage, error) {
	var labels json.RawMessage
	if err := tx.QueryRow(ctx, `SELECT labels FROM pull_requests WHERE id = $1 FOR UPDATE`, prID).Scan(&labels); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("store: lock pull request labels: %w", err)
	}
	return labels, nil
}

func ReadFilterPullRequest(ctx context.Context, tx pgx.Tx, prID string) (repoconfig.PullRequest, error) {
	var pr repoconfig.PullRequest
	var openedAt *time.Time
	err := tx.QueryRow(ctx, `SELECT number, title, author, state, merged, draft, fork, head_ref, head_sha, base_ref, url, body,
		opened_at, labels FROM pull_requests WHERE id = $1`, prID).
		Scan(&pr.Number, &pr.Title, &pr.Author, &pr.State, &pr.Merged, &pr.Draft, &pr.Fork, &pr.HeadRef, &pr.HeadSHA, &pr.BaseRef,
			&pr.URL, &pr.Body, &openedAt, &pr.Labels)
	if errors.Is(err, pgx.ErrNoRows) {
		return repoconfig.PullRequest{}, ErrNotFound
	}
	if err != nil {
		return repoconfig.PullRequest{}, fmt.Errorf("store: read pull request for the filter: %w", err)
	}
	if openedAt != nil {
		pr.CreatedAt = *openedAt
	}
	return pr, nil
}

// RunnerHeartbeatAge uses the clock that stamped the heartbeat. The boolean
// distinguishes a run that has not sent a heartbeat from one just received.
func RunnerHeartbeatAge(ctx context.Context, tx pgx.Tx, runID string) (time.Duration, bool, error) {
	var seconds *float64
	if err := tx.QueryRow(ctx, `SELECT extract(epoch FROM now() - heartbeat_at)::float8 FROM runner_runs WHERE id = $1`, runID).
		Scan(&seconds); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, false, ErrNotFound
		}
		return 0, false, fmt.Errorf("store: read runner heartbeat: %w", err)
	}
	if seconds == nil {
		return 0, false, nil
	}
	return time.Duration(*seconds * float64(time.Second)), true, nil
}

func RunnerPhase(ctx context.Context, tx pgx.Tx, runID string) (string, error) {
	var phase string
	if err := tx.QueryRow(ctx, `SELECT phase FROM runner_runs WHERE id = $1`, runID).Scan(&phase); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("store: read runner phase: %w", err)
	}
	return phase, nil
}

// StickyCommentID returns zero when no sticky comment has been recorded.
func StickyCommentID(ctx context.Context, tx pgx.Tx, prID string) (int64, error) {
	var id int64
	err := tx.QueryRow(ctx, `SELECT forge_comment_id FROM sticky_comments WHERE pull_request_id = $1`, prID).Scan(&id)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("store: read sticky comment: %w", err)
	}
	return id, nil
}
