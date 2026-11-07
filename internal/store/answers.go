package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/review"
)

// RecordAnswers keeps the answers that a review was shown, by the id of each
// finding in its prompt.
func RecordAnswers(ctx context.Context, tx pgx.Tx, reviewID string, answers map[string][]review.Answer) error {
	raw, err := json.Marshal(answers)
	if err != nil {
		return fmt.Errorf("store: encode answers: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE reviews SET answers = $2 WHERE id = $1`, reviewID, raw); err != nil {
		return fmt.Errorf("store: record answers: %w", err)
	}
	return nil
}

// ReviewAnswers are the answers that RecordAnswers kept for a review, nil
// for none.
func ReviewAnswers(ctx context.Context, tx pgx.Tx, reviewID string) (map[string][]review.Answer, error) {
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT answers FROM reviews WHERE id = $1`, reviewID).Scan(&raw); err != nil {
		return nil, fmt.Errorf("store: read answers: %w", err)
	}
	if raw == nil {
		return nil, nil
	}
	var answers map[string][]review.Answer
	if err := json.Unmarshal(raw, &answers); err != nil {
		return nil, fmt.Errorf("store: decode answers: %w", err)
	}
	return answers, nil
}
