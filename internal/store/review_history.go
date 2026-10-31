package store

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/jobs"
	"github.com/home-operations/kritika/internal/review"
)

// reusableBaselineSQL uses $1 for the pull request and $2 for an optional
// review ID to exclude. Partial results cannot stand in for a full review.
const reusableBaselineSQL = ` FROM reviews WHERE pull_request_id = $1 AND id::text <> $2
	AND status = 'completed' AND NOT partial ORDER BY created_at DESC LIMIT 1`

type CompletedReview struct {
	ID, HeadSHA, Trigger, MergeBase, Diagram string
	Changed, Checked                         []string
	Findings                                 []CompletedFinding
	Confidence                               *review.Confidence
	// Reviewers are the reviewers whose agent submitted for it: the
	// default one alone for a review that had no other.
	Reviewers []string
}

type CompletedFinding struct {
	review.Finding
	PostedInline bool
	CommentID    int64
}

// LastCompletedReview excludes partial reviews, which cannot serve as the
// baseline for a later review. An empty ID means no eligible review exists.
func LastCompletedReview(ctx context.Context, tx pgx.Tx, prID string) (CompletedReview, error) {
	var p CompletedReview
	var confidence []byte
	err := tx.QueryRow(ctx, `SELECT id, head_sha, trigger, merge_base_sha, coalesce(summary->>'diagram', ''),
		ARRAY(SELECT jsonb_array_elements_text(summary->'checked')), confidence`+reusableBaselineSQL, prID, "").
		Scan(&p.ID, &p.HeadSHA, &p.Trigger, &p.MergeBase, &p.Diagram, &p.Checked, &confidence)
	if errors.Is(err, pgx.ErrNoRows) {
		return CompletedReview{}, nil
	}
	if err != nil {
		return CompletedReview{}, fmt.Errorf("store: load last completed review: %w", err)
	}
	if confidence != nil {
		if err := json.Unmarshal(confidence, &p.Confidence); err != nil {
			return CompletedReview{}, fmt.Errorf("store: decode last completed review's confidence: %w", err)
		}
	}
	err = tx.QueryRow(ctx, `SELECT c.changed_paths FROM context_packs c JOIN runner_runs rr ON rr.id = c.runner_run_id
		WHERE rr.review_id = $1 ORDER BY c.created_at DESC LIMIT 1`, p.ID).Scan(&p.Changed)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return CompletedReview{}, fmt.Errorf("store: load last completed review's paths: %w", err)
	}
	if p.Reviewers, err = reviewersRun(ctx, tx, p.ID); err != nil {
		return CompletedReview{}, err
	}
	rows, err := tx.Query(ctx, `SELECT path, line, severity, category, title, explanation, suggested_fix, posted_inline,
		end_line, replacement, agent_prompt, coalesce(forge_comment_id, 0), rules, fingerprint, coalesce(carried_from, ''), agents
		FROM findings WHERE review_id = $1 ORDER BY path, line`, p.ID)
	if err != nil {
		return CompletedReview{}, fmt.Errorf("store: load findings: %w", err)
	}
	p.Findings, err = pgx.AppendRows(p.Findings, rows, func(row pgx.CollectableRow) (CompletedFinding, error) {
		var f CompletedFinding
		var sev, cat string
		err := row.Scan(&f.Path, &f.Line, &sev, &cat, &f.Title, &f.Explanation, &f.SuggestedFix, &f.PostedInline,
			&f.EndLine, &f.Replacement, &f.AgentPrompt, &f.CommentID, &f.Rules, &f.Fingerprint, &f.CarriedFrom, &f.Agents)
		f.Severity, f.Category = review.Severity(sev), review.Category(cat)
		if len(f.Agents) == 0 {
			f.Agents = nil
		}
		return f, err
	})
	if err != nil {
		return CompletedReview{}, fmt.Errorf("store: load findings: %w", err)
	}
	return p, nil
}

// reviewersRun are the reviewers whose agent submitted for review
// reviewID, in the order its parts list them: the default one alone for a
// review whose agent was not split by reviewer, or for one with no agent
// run to read.
func reviewersRun(ctx context.Context, tx pgx.Tx, reviewID string) ([]string, error) {
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT a.parts FROM agent_runs a JOIN runner_runs rr ON rr.id = a.runner_run_id
		WHERE rr.review_id = $1 ORDER BY a.created_at DESC LIMIT 1`, reviewID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return []string{review.DefaultReviewer}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: read the reviewers of the last completed review: %w", err)
	}
	var parts []AgentPart
	if err := json.Unmarshal(raw, &parts); err != nil {
		return nil, fmt.Errorf("store: decode the parts of the last completed review: %w", err)
	}
	var out []string
	for _, part := range parts {
		name := cmp.Or(part.Reviewer, review.DefaultReviewer)
		if part.Stop == "submitted" && !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	if len(out) == 0 {
		out = []string{review.DefaultReviewer}
	}
	return out, nil
}

func LastForgePatchID(ctx context.Context, tx pgx.Tx, prID string) (string, error) {
	var patch string
	err := tx.QueryRow(ctx, `SELECT forge_patch_id`+reusableBaselineSQL, prID, "").Scan(&patch)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("store: read last forge patch: %w", err)
	}
	return patch, nil
}

// ReviewOwed reports whether the pull request owes someone a review: one
// asked for (jobs.TriggerManual) since its last completed review started
// that delivered none. That can be a job yet to start one. It can also be
// what the latest review asked for since says. That review is still running
// or prepared, which a newer review's start cancels. Or such a start canceled
// it, or a head that moved superseded it, before it started or under it.
//
// A review that replaces it stands in for it, so an unchanged bot patch is
// reviewed rather than deduped away. A request that ended on its own,
// completed, failed, skipped or capped, or whose cancel was asked for, is
// not owed. A review that a retry superseded is the request's word only
// until the retry records its own. One superseded before it started is dated
// by its job (see EndedReview.CreatedAt). So a stale job that wakes late
// does not outrank the review that answered it or a newer request.
func ReviewOwed(ctx context.Context, tx pgx.Tx, prID string) (bool, error) {
	var owed bool
	err := tx.QueryRow(ctx, `WITH last AS (SELECT coalesce(max(created_at), '-infinity') AS at FROM reviews
			WHERE pull_request_id = $1 AND status = 'completed')
		SELECT coalesce((SELECT r.cancel_requested_at IS NULL
					AND (r.status IN ('running', 'prepared', 'superseded') OR (r.status = 'canceled' AND r.error = $4))
				FROM reviews r, last WHERE r.pull_request_id = $1 AND r.trigger = $2 AND r.created_at > last.at
				ORDER BY r.created_at DESC, r.id DESC LIMIT 1), false)
		OR EXISTS (SELECT 1 FROM river_job j JOIN pull_requests p ON p.id = $1, last
			WHERE j.kind = $3 AND j.state IN (`+jobs.LiveStatesSQL()+`) AND j.args->>'trigger' = $2
				AND j.args->>'account_id' = p.account_id::text AND j.args->>'repository_id' = p.repository_id::text
				AND (j.args->>'number')::int = p.number AND j.created_at > last.at
				AND NOT EXISTS (SELECT 1 FROM reviews r WHERE r.pull_request_id = $1 AND r.river_job_id = j.id))`,
		prID, jobs.TriggerManual, jobs.ReviewArgs{}.Kind(), ReplacedReviewError).Scan(&owed)
	if err != nil {
		return false, fmt.Errorf("store: read whether a review is owed: %w", err)
	}
	return owed, nil
}

// LastReviewMergeBase can use a partial review: it needs only a base that
// still exposes the change after a merge, not a reusable review result.
func LastReviewMergeBase(ctx context.Context, tx pgx.Tx, prID, head string) (string, error) {
	var base string
	err := tx.QueryRow(ctx, `SELECT merge_base_sha FROM reviews WHERE pull_request_id = $1 AND status = 'completed'
		AND merge_base_sha NOT IN ('', $2) ORDER BY created_at DESC LIMIT 1`, prID, head).Scan(&base)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("store: read last review merge base: %w", err)
	}
	return base, nil
}

type ReviewBaseline struct {
	PatchID    string
	Confidence *review.Confidence
	// Changed is every path that the baseline's change touched, as its context
	// pack records. It is nil when none was written.
	Changed []string
}

// LastReviewBaseline returns the latest reusable review's patch, score and
// changed paths. It excludes reviewID when nonempty. No baseline yields a
// zero value, and SQL NULL or JSON null confidence yields a nil score. The
// caller chooses the threshold and the risk floors to apply to the score.
func LastReviewBaseline(ctx context.Context, tx pgx.Tx, prID, reviewID string) (ReviewBaseline, error) {
	var baseline ReviewBaseline
	var id string
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT id, patch_id, confidence`+reusableBaselineSQL, prID, reviewID).Scan(&id, &baseline.PatchID, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return ReviewBaseline{}, nil
	}
	if err != nil {
		return ReviewBaseline{}, fmt.Errorf("store: read last review baseline: %w", err)
	}
	if raw != nil {
		if err := json.Unmarshal(raw, &baseline.Confidence); err != nil {
			return ReviewBaseline{}, fmt.Errorf("store: decode last review confidence: %w", err)
		}
	}
	err = tx.QueryRow(ctx, `SELECT c.changed_paths FROM context_packs c JOIN runner_runs rr ON rr.id = c.runner_run_id
		WHERE rr.review_id = $1 ORDER BY c.created_at DESC LIMIT 1`, id).Scan(&baseline.Changed)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return ReviewBaseline{}, fmt.Errorf("store: read last review baseline's paths: %w", err)
	}
	return baseline, nil
}

func CompletedReviewCount(ctx context.Context, tx pgx.Tx, prID string) (int, error) {
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM reviews WHERE pull_request_id = $1 AND status = 'completed'`, prID).
		Scan(&count); err != nil {
		return 0, fmt.Errorf("store: count completed reviews: %w", err)
	}
	return count, nil
}

// OtherReviewRunning includes prepared reviews awaiting publication.
func OtherReviewRunning(ctx context.Context, tx pgx.Tx, prID, reviewID string) (bool, error) {
	var running bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM reviews WHERE pull_request_id = $1 AND id::text <> $2
		AND status IN ('running', 'prepared'))`,
		prID, reviewID).Scan(&running)
	if err != nil {
		return false, fmt.Errorf("store: read running reviews: %w", err)
	}
	return running, nil
}

func ReviewHeadStatus(ctx context.Context, tx pgx.Tx, reviewID string) (status ReviewStatus, current bool, err error) {
	err = tx.QueryRow(ctx, `SELECT r.status, r.head_sha = p.head_sha FROM reviews r
		JOIN pull_requests p ON p.id = r.pull_request_id WHERE r.id = $1`, reviewID).Scan(&status, &current)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, ErrNotFound
	}
	if err != nil {
		return "", false, fmt.Errorf("store: read review head status: %w", err)
	}
	return status, current, nil
}

// ReviewStatusSince prefers the current head over a superseded head that
// finished later. No review finished since requested yields an empty status.
func ReviewStatusSince(ctx context.Context, tx pgx.Tx, prID string, requested time.Time) (ReviewStatus, error) {
	var status ReviewStatus
	err := tx.QueryRow(ctx, `SELECT r.status FROM reviews r JOIN pull_requests p ON p.id = r.pull_request_id
		WHERE r.pull_request_id = $1 AND r.finished_at >= $2
		ORDER BY r.head_sha = p.head_sha DESC, r.finished_at DESC LIMIT 1`, prID, requested).Scan(&status)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("store: read requested review: %w", err)
	}
	return status, nil
}
