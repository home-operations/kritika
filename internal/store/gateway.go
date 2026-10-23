package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/review"
)

// gatewayTokenPrefix marks a run token, so a leaked one is recognisable.
const gatewayTokenPrefix = "krk_"

// ErrGatewayToken is a run token that is unknown, revoked or expired.
var ErrGatewayToken = errors.New("store: gateway token is not valid")

// GatewayGrant is what a run token lets its bearer do at the model gateway.
type GatewayGrant struct {
	RunID, AccountID, ReviewID, RepositoryID string
	// FollowupCommentID is the comment a follow-up's run answers, 0 for a
	// review's run. A follow-up's ReviewID is the pull request's last
	// completed review, "" without one.
	FollowupCommentID int64
	// Model and Fallback are the provider/model references the run may
	// call; Fallback may be empty. Effort is how hard they reason, a
	// model.Effort, or empty for the provider's default.
	Model, Fallback, Effort string
	// Budget is the tokens the run may spend, Spent what it has. An index
	// run's is 0: its token admits it to the egress proxy alone.
	Budget, Spent int64
	// Continues is the run whose kept conversation a review's run may
	// carry on, "" for none, and Session the conversation its steps are
	// sent as, "" for its own.
	Continues, Session string
	// Reviewers are the models of the reviewers other than the default
	// one the run may run, by name; a step for one names it, and a step
	// that names none is the default reviewer's, on Model.
	Reviewers map[string]ReviewerGrant
}

// ReviewerGrant is what one reviewer's steps may call: its provider/model
// references and its effort, as GatewayGrant's own.
type ReviewerGrant struct {
	Model    string `json:"model"`
	Fallback string `json:"fallback,omitempty"`
	Effort   string `json:"effort,omitempty"`
}

// For is what a step for reviewer may call: the grant's own models for
// the default reviewer, named or not, and a granted reviewer's for
// another; false for a reviewer the grant does not name.
func (g GatewayGrant) For(reviewer string) (ReviewerGrant, bool) {
	if reviewer == "" || reviewer == review.DefaultReviewer {
		return ReviewerGrant{Model: g.Model, Fallback: g.Fallback, Effort: g.Effort}, true
	}
	r, ok := g.Reviewers[reviewer]
	return r, ok
}

// MintGatewayToken stores a new run token for g, valid until expires, and
// returns it. Only its SHA-256 is kept.
func (s *Store) MintGatewayToken(ctx context.Context, g GatewayGrant, expires time.Time) (string, error) {
	raw := make([]byte, 32)
	_, _ = rand.Read(raw) // never fails
	token := gatewayTokenPrefix + hex.EncodeToString(raw)
	grants := g.Reviewers
	if grants == nil {
		grants = map[string]ReviewerGrant{}
	}
	reviewers, err := json.Marshal(grants)
	if err != nil {
		return "", fmt.Errorf("store: encode gateway token reviewers: %w", err)
	}
	_, err = s.app.Exec(ctx, `INSERT INTO gateway_tokens
		(token_hash, runner_run_id, account_id, review_id, repository_id, model, fallback, effort, budget_tokens, expires_at, followup_comment_id,
		 continues, session, reviewers)
		VALUES ($1, $2, $3, nullif($4, '')::uuid, $5, $6, $7, $8, $9, $10, nullif($11::bigint, 0), nullif($12, '')::uuid, $13, $14::jsonb)`,
		tokenHash(token), g.RunID, g.AccountID, g.ReviewID, g.RepositoryID, g.Model, g.Fallback, g.Effort, g.Budget, expires, g.FollowupCommentID,
		g.Continues, g.Session, reviewers)
	if err != nil {
		return "", fmt.Errorf("store: mint gateway token: %w", err)
	}
	return token, nil
}

// LookupGatewayToken returns the grant of an unexpired token, or
// ErrGatewayToken.
func (s *Store) LookupGatewayToken(ctx context.Context, token string) (GatewayGrant, error) {
	if !strings.HasPrefix(token, gatewayTokenPrefix) {
		return GatewayGrant{}, ErrGatewayToken
	}
	var g GatewayGrant
	var reviewers []byte
	err := s.app.QueryRow(ctx, `SELECT runner_run_id, account_id, coalesce(review_id::text, ''), repository_id, model, fallback, effort,
		budget_tokens, spent_tokens, coalesce(followup_comment_id, 0), coalesce(continues::text, ''), session, reviewers
		FROM gateway_tokens WHERE token_hash = $1 AND expires_at > now()`, tokenHash(token)).
		Scan(&g.RunID, &g.AccountID, &g.ReviewID, &g.RepositoryID, &g.Model, &g.Fallback, &g.Effort, &g.Budget, &g.Spent, &g.FollowupCommentID,
			&g.Continues, &g.Session, &reviewers)
	if errors.Is(err, pgx.ErrNoRows) {
		return GatewayGrant{}, ErrGatewayToken
	}
	if err != nil {
		return GatewayGrant{}, fmt.Errorf("store: look up gateway token: %w", err)
	}
	if err := json.Unmarshal(reviewers, &g.Reviewers); err != nil {
		return GatewayGrant{}, fmt.Errorf("store: decode gateway token reviewers: %w", err)
	}
	if len(g.Reviewers) == 0 {
		g.Reviewers = nil
	}
	return g, nil
}

// ReserveGatewayTokens adds tokens to what the token's run has spent,
// before a step, if the run has not spent its budget, and reports whether
// it did. Each step's reservation is visible to the next at once, so
// concurrent steps overshoot the budget by at most one step, as the
// agent's own loop may.
func (s *Store) ReserveGatewayTokens(ctx context.Context, token string, tokens int64) (bool, error) {
	tag, err := s.app.Exec(ctx, `UPDATE gateway_tokens SET spent_tokens = spent_tokens + $2
		WHERE token_hash = $1 AND expires_at > now() AND spent_tokens < budget_tokens`, tokenHash(token), tokens)
	if err != nil {
		return false, fmt.Errorf("store: reserve gateway tokens: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// ChargeGatewayToken adds tokens, which may be negative, to what the
// token's run has spent: a step's actual spend less its reservation.
func (s *Store) ChargeGatewayToken(ctx context.Context, token string, tokens int64) error {
	if _, err := s.app.Exec(ctx, `UPDATE gateway_tokens SET spent_tokens = spent_tokens + $2 WHERE token_hash = $1`,
		tokenHash(token), tokens); err != nil {
		return fmt.Errorf("store: charge gateway token: %w", err)
	}
	return nil
}

// RevokeGatewayTokens deletes the run's tokens, and with them every token
// that has expired, so a worker that died before revoking leaves nothing
// behind for long.
func (s *Store) RevokeGatewayTokens(ctx context.Context, runID string) error {
	if _, err := s.app.Exec(ctx, `DELETE FROM gateway_tokens WHERE runner_run_id = $1 OR expires_at <= now()`, runID); err != nil {
		return fmt.Errorf("store: revoke gateway tokens: %w", err)
	}
	return nil
}
