package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// runnerTokenPrefix marks a report token, so a leaked one is recognisable.
const runnerTokenPrefix = "krr_"

// ErrRunnerToken is a report token that is malformed, revoked, another run's,
// or the token of a run that ended.
var ErrRunnerToken = errors.New("store: runner token is not valid")

// RunnerGrant is what a report token authorizes: its one run. Everything a
// runner writes takes its account and run from here, never from the request.
type RunnerGrant struct {
	AccountID, RunID string
}

// newRunnerToken names its account and run, which are public metadata that
// grant nothing. The application role reaches a run's row only under its
// account, so the token has to say which. The random part is the authority,
// and only the whole token's hash is stored.
func newRunnerToken(accountID, runID string) string {
	return runnerTokenPrefix + accountID + "." + runID + "." + RandomToken()
}

// parseRunnerToken reads the grant that a token claims. The claim is checked
// against the run's row. Both IDs reach a uuid cast, so only their canonical
// spelling passes, and a malformed token never reaches the database.
func parseRunnerToken(token string) (RunnerGrant, bool) {
	rest, ok := strings.CutPrefix(token, runnerTokenPrefix)
	if !ok {
		return RunnerGrant{}, false
	}
	parts := strings.SplitN(rest, ".", 3)
	if len(parts) != 3 || parts[2] == "" {
		return RunnerGrant{}, false
	}
	for _, id := range parts[:2] {
		if parsed, err := uuid.Parse(id); err != nil || parsed.String() != id {
			return RunnerGrant{}, false
		}
	}
	return RunnerGrant{AccountID: parts[0], RunID: parts[1]}, true
}

// MintRunnerToken replaces the report credential for one run. Only its hash
// is stored.
func (s *Store) MintRunnerToken(ctx context.Context, accountID, runID string) (string, error) {
	token := newRunnerToken(accountID, runID)
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

// WithRunner runs fn in a transaction under the account of the run that token
// authorizes, or returns ErrRunnerToken. It locks the run's row for the
// transaction. So a credential revoked while staging is discarded refuses
// every write that starts after it, and the writes of one run go one at a time.
func (s *Store) WithRunner(ctx context.Context, token string, fn func(pgx.Tx, RunnerGrant) error) error {
	grant, ok := parseRunnerToken(token)
	if !ok {
		return ErrRunnerToken
	}
	return s.WithAccount(ctx, grant.AccountID, func(tx pgx.Tx) error {
		var id string
		err := tx.QueryRow(ctx, `SELECT id FROM runner_runs WHERE id = $1 AND access_token_hash = $2 AND finished_at IS NULL
			FOR NO KEY UPDATE`, grant.RunID, tokenHash(token)).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrRunnerToken
		}
		if err != nil {
			return fmt.Errorf("store: authorize runner: %w", err)
		}
		return fn(tx, grant)
	})
}
