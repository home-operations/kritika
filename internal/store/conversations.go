package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// KeptConversation is what a review weighs of the conversation the last
// review's run kept before carrying it on.
type KeptConversation struct {
	// RunID is the run that kept it, Session the conversation its steps
	// were sent as, and Model the model reference that run was granted.
	RunID, Session, Model string
	// Tokens is its size as its last step counted it.
	Tokens int64
	// Age is how long ago it was kept, by the database's clock.
	Age time.Duration
}

// ReviewConversation returns the conversation the newest run of reviewID
// kept, or ErrNotFound when that run kept none.
func ReviewConversation(ctx context.Context, tx pgx.Tx, reviewID string) (KeptConversation, error) {
	var c KeptConversation
	var age float64
	err := tx.QueryRow(ctx, `SELECT c.runner_run_id, c.session, c.model, c.tokens, extract(epoch FROM now() - c.created_at)::float8
		FROM agent_conversations c
		WHERE c.runner_run_id = (SELECT id FROM runner_runs WHERE review_id = $1 ORDER BY created_at DESC LIMIT 1)`, reviewID).
		Scan(&c.RunID, &c.Session, &c.Model, &c.Tokens, &age)
	if errors.Is(err, pgx.ErrNoRows) {
		return KeptConversation{}, ErrNotFound
	}
	if err != nil {
		return KeptConversation{}, fmt.Errorf("store: read kept conversation: %w", err)
	}
	c.Age = time.Duration(age * float64(time.Second))
	return c, nil
}

// Conversation returns the conversation runID kept, as the runner encoded
// it, and the model reference it was kept under, or ErrNotFound.
func Conversation(ctx context.Context, tx pgx.Tx, runID string) (text, model string, err error) {
	err = tx.QueryRow(ctx, `SELECT conversation, model FROM agent_conversations WHERE runner_run_id = $1`, runID).Scan(&text, &model)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", ErrNotFound
	}
	if err != nil {
		return "", "", fmt.Errorf("store: read conversation: %w", err)
	}
	return text, model, nil
}
