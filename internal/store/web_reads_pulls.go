package store

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/review"
)

// Label is one pull request label as pull_requests.labels records it.
type Label struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}

// SeverityCounts counts a review's findings by severity.
type SeverityCounts struct {
	P0, P1, P2 int
}

// ReviewBrief is the newest review of a pull request as the pull request
// list shows it.
type ReviewBrief struct {
	ID        string
	Status    ReviewStatus
	Scope     review.Scope
	Findings  SeverityCounts
	CreatedAt time.Time
}

// PullRow is one pull request with its newest review.
type PullRow struct {
	ID           string
	RepositoryID string
	Repository   string
	Number       int
	Title        string
	Author       string
	State        string
	Draft        bool
	// Fork is whether the head is in another repository.
	Fork   bool
	Merged bool
	// Paused is whether its automatic reviews are paused.
	Paused     bool
	HeadSHA    string
	HeadRef    string
	BaseRef    string
	URL        string
	OpenedAt   *time.Time
	UpdatedAt  time.Time
	Labels     []Label
	LastReview *ReviewBrief
	// Reviews counts the reviews that completed: not those skipped, capped,
	// superseded, failed or canceled.
	Reviews int
	// CostUSD is what every review of the pull request spent, whatever
	// became of it.
	CostUSD float64
}

// PullFilter narrows ListPulls. Zero fields match everything; Author is
// the author's login in any case, and Query matches a title or author
// substring, or a number.
type PullFilter struct {
	RepositoryID string
	State        PullState
	Outcome      ReviewStatus
	Author       string
	Query        string
	Is           PullIs
}

// The last review, lr, is the newest that was not skipped: a skipped one
// said nothing, so what the one before it found still stands.
const pullColumns = `p.id, p.repository_id, r.name, p.number, p.title, p.author, p.state, p.draft, p.fork, p.merged, p.paused,
	p.head_sha, p.head_ref, p.base_ref, p.url, p.opened_at, p.updated_at, p.labels,
	lr.id, lr.status, lr.scope, lr.created_at, lr.p0, lr.p1, lr.p2,
	(SELECT count(*) FROM reviews WHERE pull_request_id = p.id AND status = 'completed'),
	(SELECT coalesce(sum(u.cost_usd), 0)::float8 FROM usage u JOIN reviews v ON v.id = u.review_id WHERE v.pull_request_id = p.id)
	FROM pull_requests p JOIN repositories r ON r.id = p.repository_id
	LEFT JOIN LATERAL (SELECT v.id, v.status, v.scope, v.created_at,
		count(f.id) FILTER (WHERE f.severity = 'p0') AS p0,
		count(f.id) FILTER (WHERE f.severity = 'p1') AS p1,
		count(f.id) FILTER (WHERE f.severity = 'p2') AS p2
		FROM reviews v LEFT JOIN findings f ON f.review_id = v.id
		WHERE v.id = (SELECT id FROM reviews WHERE pull_request_id = p.id AND status <> 'skipped'
			ORDER BY created_at DESC, id DESC LIMIT 1)
		GROUP BY v.id) lr ON true`

func scanPull(row pgx.CollectableRow) (PullRow, error) {
	var p PullRow
	var labels []byte
	var id, status, scope *string
	var at *time.Time
	var p0, p1, p2 *int
	if err := row.Scan(&p.ID, &p.RepositoryID, &p.Repository, &p.Number, &p.Title, &p.Author, &p.State, &p.Draft, &p.Fork, &p.Merged,
		&p.Paused, &p.HeadSHA, &p.HeadRef, &p.BaseRef, &p.URL, &p.OpenedAt, &p.UpdatedAt, &labels,
		&id, &status, &scope, &at, &p0, &p1, &p2, &p.Reviews, &p.CostUSD); err != nil {
		return p, err
	}
	p.Labels = []Label{}
	if len(labels) > 0 {
		if err := json.Unmarshal(labels, &p.Labels); err != nil {
			return p, fmt.Errorf("decode labels: %w", err)
		}
	}
	if id != nil {
		p.LastReview = &ReviewBrief{
			ID: *id, Status: ReviewStatus(*status), Scope: review.Scope(*scope), CreatedAt: *at,
			Findings: SeverityCounts{P0: *p0, P1: *p1, P2: *p2},
		}
	}
	return p, nil
}

// ErrFilter is a list filter with a value outside its type.
var ErrFilter = errors.New("store: invalid filter")

// ListPulls returns a page of pull requests, most recently updated first.
func ListPulls(ctx context.Context, tx pgx.Tx, f PullFilter, p Page) ([]PullRow, *Cursor, error) {
	if err := p.check(); err != nil {
		return nil, nil, err
	}
	f.State = cmp.Or(f.State, PullAll)
	if !f.State.Valid() || (f.Outcome != "" && !f.Outcome.Valid()) || (f.Is != "" && !f.Is.Valid()) {
		return nil, nil, ErrFilter
	}
	number := -1
	if n, err := strconv.ParseInt(strings.TrimPrefix(f.Query, "#"), 10, 32); err == nil {
		number = int(n)
	}
	like := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(f.Query) + "%"
	rows, err := tx.Query(ctx, `SELECT `+pullColumns+`
		WHERE ($1::uuid IS NULL OR p.repository_id = $1)
			AND ($2 = 'all' OR p.state = $2)
			AND ($3 = '' OR lr.status = $3)
			AND ($4 = '' OR p.title ILIKE $5 OR p.author ILIKE $5 OR p.number = $6)
			AND ($7 OR (p.updated_at, p.id) < ($8, $9::uuid))
			AND ($11 = '' OR lower(p.author) = lower($11))
			AND ($12 = '' OR CASE $12 WHEN 'paused' THEN p.paused ELSE coalesce(lr.p0, 0) > 0 END)
		ORDER BY p.updated_at DESC, p.id DESC LIMIT $10`,
		uuidParam(f.RepositoryID), string(f.State), string(f.Outcome), f.Query, like, number,
		p.After.First(), p.After.T, p.afterID(), p.Limit+1, f.Author, string(f.Is))
	if err != nil {
		return nil, nil, fmt.Errorf("store: list pull requests: %w", err)
	}
	out, err := pgx.CollectRows(rows, scanPull)
	if err != nil {
		return nil, nil, fmt.Errorf("store: list pull requests: %w", err)
	}
	items, next := paged(out, p.Limit, func(r PullRow) Cursor { return Cursor{T: r.UpdatedAt, ID: r.ID} })
	return items, next, nil
}

// Attention counts the account's open pull requests that want a look, by
// why: the newest review that was not skipped failed, hit a cap or found
// a P0 finding, or automatic reviews are paused. One pull request
// may count under several.
type Attention struct {
	Failed, Capped, P0, Paused int
}

// ReadAttention counts the open pull requests that want a look.
func ReadAttention(ctx context.Context, tx pgx.Tx) (Attention, error) {
	var a Attention
	err := tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE lr.status = 'failed'), count(*) FILTER (WHERE lr.status = 'capped'),
			count(*) FILTER (WHERE lr.p0 > 0), count(*) FILTER (WHERE p.paused)
		FROM pull_requests p LEFT JOIN LATERAL (SELECT v.status,
			(SELECT count(*) FROM findings f WHERE f.review_id = v.id AND f.severity = 'p0') AS p0
			FROM reviews v WHERE v.pull_request_id = p.id AND v.status <> 'skipped'
			ORDER BY v.created_at DESC, v.id DESC LIMIT 1) lr ON true
		WHERE p.state = 'open'`).Scan(&a.Failed, &a.Capped, &a.P0, &a.Paused)
	if err != nil {
		return a, fmt.Errorf("store: read attention: %w", err)
	}
	return a, nil
}

// FindPull returns the pull request numbered number of a repository.
func FindPull(ctx context.Context, tx pgx.Tx, repositoryID string, number int) (PullRow, error) {
	rows, err := tx.Query(ctx, `SELECT `+pullColumns+` WHERE p.repository_id = $1 AND p.number = $2`, repositoryID, number)
	if err != nil {
		return PullRow{}, fmt.Errorf("store: find pull request: %w", err)
	}
	p, err := pgx.CollectExactlyOneRow(rows, scanPull)
	if errors.Is(err, pgx.ErrNoRows) {
		return PullRow{}, ErrNotFound
	}
	if err != nil {
		return PullRow{}, fmt.Errorf("store: find pull request: %w", err)
	}
	return p, nil
}

// ReviewRow is one reviews row with what its usage rows charged it.
type ReviewRow struct {
	ID            string
	PullRequestID string
	Repository    string
	Number        int
	Title         string
	URL           string
	// PullState and PullMerged are the pull request's: a merged or closed
	// one takes no more reviews.
	PullState     string
	PullMerged    bool
	Status        ReviewStatus
	Trigger       string
	Scope         review.Scope
	ScopeReason   string
	Model         string
	HeadSHA       string
	MergeBaseSHA  string
	PatchID       string
	PriorReviewID *string
	// NewestReviewID is the pull request's newest review that was not
	// skipped, nil when none is newer than this.
	NewestReviewID *string
	// SkipReason is why a skipped review was: the repository's own reason
	// (a repoconfig.SkipReason), or the runner's (runner.Skip*).
	SkipReason        string
	Error             string
	CreatedAt         time.Time
	FinishedAt        *time.Time
	CancelRequestedAt *time.Time
	// Summary is the reviews.summary JSON, nil when the review has none.
	Summary *review.Summary
	// Confidence is the reviews.confidence JSON, nil when the review was
	// not scored.
	Confidence   *review.Confidence
	CostUSD      float64
	InputTokens  int64
	OutputTokens int64
	Calls        int64
	PlanCalls    int64
}

const reviewColumns = `v.id, v.pull_request_id, r.name, p.number, p.title, p.url, v.status, v.trigger, v.scope, v.scope_reason,
	v.model, v.head_sha, v.merge_base_sha, v.patch_id, v.prior_review_id, v.skip_reason, v.error, v.created_at, v.finished_at,
	v.cancel_requested_at, v.summary, v.confidence, coalesce(u.cost, 0), coalesce(u.input, 0), coalesce(u.output, 0),
	coalesce(u.calls, 0), coalesce(u.plan_calls, 0),
	(SELECT n.id FROM reviews n WHERE n.pull_request_id = v.pull_request_id AND n.status <> 'skipped'
		AND (n.created_at, n.id) > (v.created_at, v.id)
		ORDER BY n.created_at DESC, n.id DESC LIMIT 1), p.state, p.merged
	FROM reviews v JOIN pull_requests p ON p.id = v.pull_request_id JOIN repositories r ON r.id = p.repository_id
	LEFT JOIN LATERAL (SELECT sum(cost_usd)::float8 AS cost, sum(input_tokens) AS input, sum(output_tokens) AS output,
		count(*) AS calls, count(*) FILTER (WHERE chatgpt_plan) AS plan_calls
		FROM usage WHERE review_id = v.id) u ON true`

func scanReview(row pgx.CollectableRow) (ReviewRow, error) {
	var v ReviewRow
	var status, scope string
	var summary, confidence []byte
	if err := row.Scan(&v.ID, &v.PullRequestID, &v.Repository, &v.Number, &v.Title, &v.URL, &status, &v.Trigger, &scope, &v.ScopeReason,
		&v.Model, &v.HeadSHA, &v.MergeBaseSHA, &v.PatchID, &v.PriorReviewID, &v.SkipReason, &v.Error, &v.CreatedAt, &v.FinishedAt,
		&v.CancelRequestedAt, &summary, &confidence, &v.CostUSD, &v.InputTokens, &v.OutputTokens, &v.Calls, &v.PlanCalls, &v.NewestReviewID,
		&v.PullState, &v.PullMerged); err != nil {
		return v, err
	}
	v.Status = ReviewStatus(status)
	v.Scope = review.Scope(scope)
	if len(summary) > 0 && string(summary) != "null" {
		var s review.Summary
		if err := json.Unmarshal(summary, &s); err != nil {
			return v, fmt.Errorf("decode summary: %w", err)
		}
		v.Summary = &s
	}
	if len(confidence) > 0 && string(confidence) != "null" {
		var c review.Confidence
		if err := json.Unmarshal(confidence, &c); err != nil {
			return v, fmt.Errorf("decode confidence: %w", err)
		}
		v.Confidence = &c
	}
	return v, nil
}

// ListPullReviews returns every review of a pull request, newest first.
func ListPullReviews(ctx context.Context, tx pgx.Tx, pullRequestID string) ([]ReviewRow, error) {
	rows, err := tx.Query(ctx, `SELECT `+reviewColumns+` WHERE v.pull_request_id = $1 ORDER BY v.created_at DESC, v.id DESC`, pullRequestID)
	if err != nil {
		return nil, fmt.Errorf("store: list reviews: %w", err)
	}
	out, err := pgx.CollectRows(rows, scanReview)
	if err != nil {
		return nil, fmt.Errorf("store: list reviews: %w", err)
	}
	return out, nil
}

// FindReview returns one review, or ErrNotFound.
func FindReview(ctx context.Context, tx pgx.Tx, id string) (ReviewRow, error) {
	if uuid.Validate(id) != nil {
		return ReviewRow{}, ErrNotFound
	}
	rows, err := tx.Query(ctx, `SELECT `+reviewColumns+` WHERE v.id = $1::uuid`, id)
	if err != nil {
		return ReviewRow{}, fmt.Errorf("store: find review: %w", err)
	}
	v, err := pgx.CollectExactlyOneRow(rows, scanReview)
	if errors.Is(err, pgx.ErrNoRows) {
		return ReviewRow{}, ErrNotFound
	}
	if err != nil {
		return ReviewRow{}, fmt.Errorf("store: find review: %w", err)
	}
	return v, nil
}

// FollowupRow is one followups row.
type FollowupRow struct {
	ID            string
	PullRequestID string
	Repository    string
	Number        int
	// PullURL is the pull request on the forge, where the comments are.
	PullURL        string
	CommentID      int64
	Author         string
	Inline         bool
	Path           string
	Line           int
	Status         FollowupStatus
	Reason         string
	ReplyCommentID *int64
	Model          string
	CreatedAt      time.Time
}

// FollowupFilter narrows ListFollowups; zero fields match everything.
type FollowupFilter struct {
	RepositoryID  string
	PullRequestID string
	CommentID     int64
}

// ListFollowups returns a page of follow-ups, newest first.
func ListFollowups(ctx context.Context, tx pgx.Tx, f FollowupFilter, p Page) ([]FollowupRow, *Cursor, error) {
	if err := p.check(); err != nil {
		return nil, nil, err
	}
	rows, err := tx.Query(ctx, `SELECT f.id, f.pull_request_id, r.name, p.number, p.url, f.comment_id, f.author, f.inline, f.path, f.line,
		f.status, f.reason, f.reply_comment_id, f.model, f.created_at
		FROM followups f JOIN pull_requests p ON p.id = f.pull_request_id JOIN repositories r ON r.id = p.repository_id
		WHERE ($1::uuid IS NULL OR p.repository_id = $1) AND ($2::uuid IS NULL OR f.pull_request_id = $2)
			AND ($3::bigint = 0 OR f.comment_id = $3) AND ($4 OR (f.created_at, f.id) < ($5, $6::uuid))
		ORDER BY f.created_at DESC, f.id DESC LIMIT $7`,
		uuidParam(f.RepositoryID), uuidParam(f.PullRequestID), f.CommentID, p.After.First(), p.After.T, p.afterID(), p.Limit+1)
	if err != nil {
		return nil, nil, fmt.Errorf("store: list follow-ups: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (FollowupRow, error) {
		var x FollowupRow
		var status string
		err := row.Scan(&x.ID, &x.PullRequestID, &x.Repository, &x.Number, &x.PullURL, &x.CommentID, &x.Author, &x.Inline, &x.Path, &x.Line,
			&status, &x.Reason, &x.ReplyCommentID, &x.Model, &x.CreatedAt)
		x.Status = FollowupStatus(status)
		return x, err
	})
	if err != nil {
		return nil, nil, fmt.Errorf("store: list follow-ups: %w", err)
	}
	items, next := paged(out, p.Limit, func(x FollowupRow) Cursor { return Cursor{T: x.CreatedAt, ID: x.ID} })
	return items, next, nil
}
