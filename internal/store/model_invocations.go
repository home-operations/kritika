package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ErrModelBudget means the run cannot reserve another model call.
var ErrModelBudget = errors.New("store: model call budget exhausted or token expired")

// BeginModelInvocation records an outbound call and reserves its budget in the
// same transaction. A reservation with no durable outcome is never auto-refunded.
func (s *Store) BeginModelInvocation(ctx context.Context, u Usage, token string, reserved int64, expires time.Time) (string, error) {
	id := uuid.NewString()
	payload, err := json.Marshal(u)
	if err != nil {
		return "", fmt.Errorf("store: encode model invocation: %w", err)
	}
	err = s.WithAccount(ctx, u.AccountID, func(tx pgx.Tx) error {
		var hash []byte
		if token != "" {
			hash = tokenHash(token)
			tag, err := tx.Exec(ctx, `UPDATE gateway_tokens SET spent_tokens = spent_tokens + $2
				WHERE token_hash = $1 AND account_id = $3 AND runner_run_id = $4
				AND expires_at > now() AND spent_tokens < budget_tokens`, hash, reserved, u.AccountID, u.RunnerRunID)
			if err != nil {
				return fmt.Errorf("store: reserve model invocation: %w", err)
			}
			if tag.RowsAffected() == 0 {
				return ErrModelBudget
			}
		}
		_, err := tx.Exec(ctx, `INSERT INTO model_invocations
			(id, account_id, runner_run_id, token_hash, reserved_tokens, usage, expires_at)
			VALUES ($1, $2, nullif($3, '')::uuid, $4, $5, $6, $7)`, id, u.AccountID, u.RunnerRunID, hash, reserved, payload, expires)
		if err != nil {
			return fmt.Errorf("store: begin model invocation: %w", err)
		}
		return nil
	})
	return id, err
}

// CompleteModelInvocation durably saves the first known outcome before any
// accounting writes. A late outcome can resolve an uncertain invocation.
func (s *Store) CompleteModelInvocation(ctx context.Context, id string, u Usage, answered bool) error {
	payload, err := json.Marshal(u)
	if err != nil {
		return fmt.Errorf("store: encode model outcome: %w", err)
	}
	return s.WithAccount(ctx, u.AccountID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE model_invocations SET state = 'completed', usage = $2, answered = $3
			WHERE id = $1 AND state IN ('reserved', 'uncertain')`, id, payload, answered)
		if err != nil {
			return fmt.Errorf("store: complete model invocation: %w", err)
		}
		return nil
	})
}

// SettleModelInvocation atomically settles a known outcome's reservation and
// usage. The row lock and stable usage ID make retries safe after a lost commit
// acknowledgment. Revoked tokens need no budget update. Usage still survives.
func (s *Store) SettleModelInvocation(ctx context.Context, accountID, id string) (bool, error) {
	settled := false
	err := s.WithAccount(ctx, accountID, func(tx pgx.Tx) error {
		var state string
		var hash, payload []byte
		var reserved int64
		var answered *bool
		var created time.Time
		if err := tx.QueryRow(ctx, `SELECT state, token_hash, reserved_tokens, usage, answered, created_at
			FROM model_invocations WHERE id = $1 FOR UPDATE`, id).
			Scan(&state, &hash, &reserved, &payload, &answered, &created); err != nil {
			return fmt.Errorf("store: read model invocation: %w", err)
		}
		if state != "completed" {
			return nil
		}
		var u Usage
		if err := json.Unmarshal(payload, &u); err != nil {
			return fmt.Errorf("store: decode model outcome: %w", err)
		}
		u.ID, u.AccountID, u.CreatedAt = id, accountID, &created
		spent := u.Input + u.Output
		if len(hash) != 0 {
			charge := -reserved
			if *answered {
				charge += spent
			}
			if _, err := tx.Exec(ctx, `UPDATE gateway_tokens SET spent_tokens = spent_tokens + $2 WHERE token_hash = $1`, hash, charge); err != nil {
				return fmt.Errorf("store: settle model reservation: %w", err)
			}
		}
		if spent != 0 || (*answered && len(hash) != 0) {
			if err := InsertUsage(ctx, tx, u); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE model_invocations SET state = 'settled', settled_at = now() WHERE id = $1`, id); err != nil {
			return fmt.Errorf("store: settle model invocation: %w", err)
		}
		settled = true
		return nil
	})
	return settled && err == nil, err
}

// SweepModelInvocations deletes every account's model invocations settled
// more than olderThan ago, a batch at a time, and returns how many it
// deleted. A settled call's accounting is its usage row. Its record has only
// to keep a late outcome from settling it twice, which no outcome that old
// can do. Reserved, completed and uncertain calls stay for the rescuer.
// Owner connection, leader only, like SweepModelCalls.
func (s *Store) SweepModelInvocations(ctx context.Context, olderThan time.Duration) (int64, error) {
	if s.owner == nil {
		return 0, errors.New("store: SweepModelInvocations needs the owner connection")
	}
	if olderThan <= 0 {
		return 0, fmt.Errorf("store: model invocation retention %s is not positive", olderThan)
	}
	n, err := s.sweepBatches(ctx, `DELETE FROM model_invocations WHERE id IN (
		SELECT id FROM model_invocations WHERE state = 'settled' AND settled_at < now() - make_interval(secs => $1) LIMIT $2)`,
		olderThan, modelCallSweepBatch)
	if err != nil {
		return n, fmt.Errorf("store: sweep model invocations: %w", err)
	}
	return n, nil
}

// InvocationRecovery describes one bounded leader pass over unfinished calls.
type InvocationRecovery struct {
	Settled, Uncertain int
}

// ReconcileModelInvocations settles known outcomes and marks expired calls whose
// outcomes were lost as uncertain. It never repeats a provider request.
func (s *Store) ReconcileModelInvocations(ctx context.Context, limit int) (InvocationRecovery, error) {
	var result InvocationRecovery
	if s.owner == nil {
		return result, errors.New("store: ReconcileModelInvocations needs the owner connection")
	}
	rows, err := s.owner.Query(ctx, `SELECT id::text, account_id::text FROM model_invocations
		WHERE state = 'completed' OR (state = 'reserved' AND expires_at < now()) ORDER BY created_at, id LIMIT $1`, limit)
	if err != nil {
		return result, fmt.Errorf("store: list unsettled model invocations: %w", err)
	}
	type pending struct{ ID, Account string }
	calls, err := pgx.CollectRows(rows, pgx.RowToStructByPos[pending])
	if err != nil {
		return result, fmt.Errorf("store: read unsettled model invocations: %w", err)
	}
	var errs []error
	for _, call := range calls {
		var uncertain int
		err := s.WithAccount(ctx, call.Account, func(tx pgx.Tx) error {
			tag, err := tx.Exec(ctx, `UPDATE model_invocations SET state = 'uncertain'
				WHERE id = $1 AND state = 'reserved' AND expires_at < now()`, call.ID)
			if err == nil {
				uncertain = int(tag.RowsAffected())
			}
			return err
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("store: mark uncertain model invocation: %w", err))
			continue
		}
		result.Uncertain += uncertain
		settled, err := s.SettleModelInvocation(ctx, call.Account, call.ID)
		if err != nil {
			errs = append(errs, err)
		} else if settled {
			result.Settled++
		}
	}
	return result, errors.Join(errs...)
}
