package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// MintRunnerToken replaces the database capability for one run. Only its
// hash is stored; public run IDs and notifications convey no authority.
func (s *Store) MintRunnerToken(ctx context.Context, accountID, runID string) (string, error) {
	token := RandomToken()
	err := s.WithAccount(ctx, accountID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE runner_runs SET access_token_hash = $2 WHERE id = $1`, runID, tokenHash(token))
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return pgx.ErrNoRows
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("store: mint runner credential: %w", err)
	}
	return token, nil
}

// ForRunner returns a view sharing s's pools with a run's credential.
// The caller must not close the view; the original store owns the pools.
func (s *Store) ForRunner(token string) *Store {
	view := *s
	view.runnerToken = token
	return &view
}
