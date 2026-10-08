package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/chatgpt"
)

// ChatGPTSession is a plan's live token set, keyed by the client its
// sign-in registered.
type ChatGPTSession struct {
	Credentials chatgpt.Credentials
	// SignedOutAt is when a refresh found the session over; zero while it
	// lives. SignedOutReason says why.
	SignedOutAt     time.Time
	SignedOutReason string
}

// SignedOut reports whether the session is over.
func (s ChatGPTSession) SignedOut() bool { return !s.SignedOutAt.IsZero() }

// seedHash identifies a record by its tokens.
func seedHash(c chatgpt.Credentials) string {
	sum := sha256.Sum256([]byte(c.AccessToken + "\x00" + c.RefreshToken))
	return hex.EncodeToString(sum[:])
}

// SeedChatGPTSession puts the configured record in place of none, or of
// the set an earlier record seeded: a new sign-in in the configuration
// replaces the live set and ends a sign-out, while the same record leaves
// the set the refreshes have built alone.
func (s *Store) SeedChatGPTSession(ctx context.Context, c chatgpt.Credentials) error {
	raw, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("store: seed chatgpt session: %w", err)
	}
	if _, err := s.app.Exec(ctx, `INSERT INTO chatgpt_sessions (client_id, seed_hash, credentials) VALUES ($1, $2, $3)
		ON CONFLICT (client_id) DO UPDATE
		SET seed_hash = EXCLUDED.seed_hash, credentials = EXCLUDED.credentials, signed_out_at = NULL, signed_out_reason = '', updated_at = now()
		WHERE chatgpt_sessions.seed_hash <> EXCLUDED.seed_hash`, c.ClientID, seedHash(c), raw); err != nil {
		return fmt.Errorf("store: seed chatgpt session: %w", err)
	}
	return nil
}

// ChatGPTSession returns the client's session, and whether there is one.
func (s *Store) ChatGPTSession(ctx context.Context, clientID string) (ChatGPTSession, bool, error) {
	rows, err := s.app.Query(ctx, `SELECT credentials, coalesce(signed_out_at, 'epoch'::timestamptz), signed_out_reason
		FROM chatgpt_sessions WHERE client_id = $1`, clientID)
	if err != nil {
		return ChatGPTSession{}, false, fmt.Errorf("store: chatgpt session: %w", err)
	}
	session, err := pgx.CollectExactlyOneRow(rows, scanChatGPTSession)
	if errors.Is(err, pgx.ErrNoRows) {
		return ChatGPTSession{}, false, nil
	}
	if err != nil {
		return ChatGPTSession{}, false, fmt.Errorf("store: chatgpt session: %w", err)
	}
	return session, true, nil
}

// ChatGPTSessions returns every session.
func (s *Store) ChatGPTSessions(ctx context.Context) ([]ChatGPTSession, error) {
	rows, err := s.app.Query(ctx, `SELECT credentials, coalesce(signed_out_at, 'epoch'::timestamptz), signed_out_reason
		FROM chatgpt_sessions ORDER BY client_id`)
	if err != nil {
		return nil, fmt.Errorf("store: chatgpt sessions: %w", err)
	}
	sessions, err := pgx.CollectRows(rows, scanChatGPTSession)
	if err != nil {
		return nil, fmt.Errorf("store: chatgpt sessions: %w", err)
	}
	return sessions, nil
}

func scanChatGPTSession(row pgx.CollectableRow) (ChatGPTSession, error) {
	var s ChatGPTSession
	var raw []byte
	if err := row.Scan(&raw, &s.SignedOutAt, &s.SignedOutReason); err != nil {
		return s, err
	}
	if err := json.Unmarshal(raw, &s.Credentials); err != nil {
		return s, fmt.Errorf("credentials: %w", err)
	}
	if s.SignedOutAt.Equal(time.Unix(0, 0)) {
		s.SignedOutAt = time.Time{}
	}
	return s, nil
}

// RefreshChatGPTSession replaces the set whose refresh token is was with c,
// and reports whether it did: a set another process replaced meanwhile is
// left as it is, since its refresh token is the one OpenAI knows.
func (s *Store) RefreshChatGPTSession(ctx context.Context, c chatgpt.Credentials, was string) (bool, error) {
	raw, err := json.Marshal(c)
	if err != nil {
		return false, fmt.Errorf("store: refresh chatgpt session: %w", err)
	}
	tag, err := s.app.Exec(ctx, `UPDATE chatgpt_sessions SET credentials = $2, updated_at = now()
		WHERE client_id = $1 AND credentials->>'refresh_token' = $3`, c.ClientID, raw, was)
	if err != nil {
		return false, fmt.Errorf("store: refresh chatgpt session: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// SignOutChatGPTSession records that a refresh found the session over.
func (s *Store) SignOutChatGPTSession(ctx context.Context, clientID, reason string) error {
	if _, err := s.app.Exec(ctx, `UPDATE chatgpt_sessions SET signed_out_at = now(), signed_out_reason = $2, updated_at = now()
		WHERE client_id = $1 AND signed_out_at IS NULL`, clientID, reason); err != nil {
		return fmt.Errorf("store: sign out chatgpt session: %w", err)
	}
	return nil
}

// ExpireChatGPTSession marks the session's access token spent, after the
// provider refused it, so the leader renews it on its next pass rather
// than at the hour's end.
func (s *Store) ExpireChatGPTSession(ctx context.Context, clientID string) error {
	if _, err := s.app.Exec(ctx, `UPDATE chatgpt_sessions SET credentials = credentials || '{"expires_in": 0}'::jsonb, updated_at = now()
		WHERE client_id = $1`, clientID); err != nil {
		return fmt.Errorf("store: expire chatgpt session: %w", err)
	}
	return nil
}
