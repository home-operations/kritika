package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Usage roles, matching the usage table's CHECK.
const (
	RoleReview     = "review"
	RoleEmbedding  = "embedding"
	RoleFollowUp   = "followup"
	RoleConfidence = "confidence"
)

// Usage is one model call charged to an account, and to the review it
// served when there is one: its whole prompt, cached part included, as
// input.
type Usage struct {
	AccountID, RepositoryID, ReviewID, Role, Model, Upstream string
	RunnerRunID                                              string
	Input, Output                                            int64
	CostUSD                                                  float64
	ChatGPTPlan                                              bool
}

// InsertUsage records u, where the caps count it.
func InsertUsage(ctx context.Context, tx pgx.Tx, u Usage) error {
	if u.ChatGPTPlan {
		u.CostUSD = 0
	}
	if _, err := tx.Exec(ctx, `INSERT INTO usage
		(account_id, repository_id, review_id, role, model, upstream, input_tokens, output_tokens, cost_usd, chatgpt_plan, runner_run_id)
		VALUES ($1, $2, nullif($3, '')::uuid, $4, $5, $6, $7, $8, $9, $10, nullif($11, '')::uuid)`,
		u.AccountID, u.RepositoryID, u.ReviewID, u.Role, u.Model, u.Upstream, u.Input, u.Output, u.CostUSD, u.ChatGPTPlan,
		u.RunnerRunID); err != nil {
		return fmt.Errorf("store: insert usage: %w", err)
	}
	return nil
}

// PullCost is what the pull request's reviews have cost together: every
// usage row charged to one of them.
func PullCost(ctx context.Context, tx pgx.Tx, pullRequestID string) (float64, error) {
	var cost float64
	err := tx.QueryRow(ctx, `SELECT coalesce(sum(u.cost_usd), 0)::float8 FROM usage u JOIN reviews v ON v.id = u.review_id
		WHERE v.pull_request_id = $1`, pullRequestID).Scan(&cost)
	if err != nil {
		return 0, fmt.Errorf("store: pull cost: %w", err)
	}
	return cost, nil
}
