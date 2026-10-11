package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/review"
)

type CompletedReview struct {
	ID, HeadSHA, Trigger, MergeBase, Diagram string
	Changed, Checked, Impact                 []string
	Findings                                 []CompletedFinding
	Confidence                               *review.Confidence
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
		ARRAY(SELECT jsonb_array_elements_text(summary->'checked')),
		ARRAY(SELECT jsonb_array_elements_text(summary->'impact')), confidence FROM reviews
		WHERE pull_request_id = $1 AND status = 'completed' AND NOT partial ORDER BY created_at DESC LIMIT 1`, prID).
		Scan(&p.ID, &p.HeadSHA, &p.Trigger, &p.MergeBase, &p.Diagram, &p.Checked, &p.Impact, &confidence)
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
	rows, err := tx.Query(ctx, `SELECT path, line, severity, category, title, explanation, suggested_fix, posted_inline,
		end_line, replacement, agent_prompt, coalesce(forge_comment_id, 0), rules, fingerprint, coalesce(carried_from, '')
		FROM findings WHERE review_id = $1 ORDER BY path, line`, p.ID)
	if err != nil {
		return CompletedReview{}, fmt.Errorf("store: load findings: %w", err)
	}
	p.Findings, err = pgx.AppendRows(p.Findings, rows, func(row pgx.CollectableRow) (CompletedFinding, error) {
		var f CompletedFinding
		var sev, cat string
		err := row.Scan(&f.Path, &f.Line, &sev, &cat, &f.Title, &f.Explanation, &f.SuggestedFix, &f.PostedInline,
			&f.EndLine, &f.Replacement, &f.AgentPrompt, &f.CommentID, &f.Rules, &f.Fingerprint, &f.CarriedFrom)
		f.Severity, f.Category = review.Severity(sev), review.Category(cat)
		return f, err
	})
	if err != nil {
		return CompletedReview{}, fmt.Errorf("store: load findings: %w", err)
	}
	return p, nil
}

// LastReviewPatchID returns the latest complete baseline's patch, excluding
// reviewID, or an empty string if there is no baseline.
func LastReviewPatchID(ctx context.Context, tx pgx.Tx, prID, reviewID string) (string, error) {
	var patch string
	err := tx.QueryRow(ctx, `SELECT patch_id FROM reviews WHERE pull_request_id = $1 AND id <> $2
		AND status = 'completed' AND NOT partial ORDER BY created_at DESC LIMIT 1`, prID, reviewID).Scan(&patch)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("store: read last review patch: %w", err)
	}
	return patch, nil
}

func LastForgePatchID(ctx context.Context, tx pgx.Tx, prID string) (string, error) {
	var patch string
	err := tx.QueryRow(ctx, `SELECT forge_patch_id FROM reviews WHERE pull_request_id = $1
		AND status = 'completed' AND NOT partial ORDER BY created_at DESC LIMIT 1`, prID).Scan(&patch)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("store: read last forge patch: %w", err)
	}
	return patch, nil
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

// LastReviewConfidence leaves threshold selection to the caller. A missing
// review or SQL NULL confidence yields nil without an error.
func LastReviewConfidence(ctx context.Context, tx pgx.Tx, prID, reviewID string) (*review.Confidence, error) {
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT confidence FROM reviews WHERE pull_request_id = $1 AND id::text <> $2
		AND status = 'completed' AND NOT partial ORDER BY created_at DESC LIMIT 1`, prID, reviewID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && raw == nil) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: read last review confidence: %w", err)
	}
	c := &review.Confidence{}
	if err := json.Unmarshal(raw, c); err != nil {
		return nil, fmt.Errorf("store: decode last review confidence: %w", err)
	}
	return c, nil
}

func CompletedReviewCount(ctx context.Context, tx pgx.Tx, prID string) (int, error) {
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM reviews WHERE pull_request_id = $1 AND status = 'completed'`, prID).
		Scan(&count); err != nil {
		return 0, fmt.Errorf("store: count completed reviews: %w", err)
	}
	return count, nil
}

func OtherReviewRunning(ctx context.Context, tx pgx.Tx, prID, reviewID string) (bool, error) {
	var running bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM reviews WHERE pull_request_id = $1 AND id::text <> $2 AND status = 'running')`,
		prID, reviewID).Scan(&running)
	if err != nil {
		return false, fmt.Errorf("store: read running reviews: %w", err)
	}
	return running, nil
}

func ReviewHeadStatus(ctx context.Context, tx pgx.Tx, reviewID string) (status ReviewStatus, current bool, err error) {
	err = tx.QueryRow(ctx, `SELECT r.status, r.head_sha = p.head_sha FROM reviews r
		JOIN pull_requests p ON p.id = r.pull_request_id WHERE r.id = $1`, reviewID).Scan(&status, &current)
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
