package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/chatgpt"
)

// ChatGPTSession is one provider's registration and live credentials.
type ChatGPTSession struct {
	Key             string
	Credentials     chatgpt.Credentials
	SignedOutAt     time.Time
	SignedOutReason string
}

// SignedOut reports whether the renewable session has ended.
func (s ChatGPTSession) SignedOut() bool { return !s.SignedOutAt.IsZero() }

// PrepareChatGPTSession persists a host ID before OAuth begins. Repeated
// attempts reuse the registration, including after a terminal refresh error.
func (s *Store) PrepareChatGPTSession(ctx context.Context, key string) (ChatGPTSession, error) {
	c := chatgpt.Credentials{Issuer: chatgpt.Issuer, HostID: "urn:uuid:" + uuid.NewString()}
	raw, err := json.Marshal(c)
	if err != nil {
		return ChatGPTSession{}, fmt.Errorf("store: prepare chatgpt session: %w", err)
	}
	if _, err := s.app.Exec(ctx, `INSERT INTO chatgpt_sessions (key, credentials) VALUES ($1, $2)
		ON CONFLICT (key) DO NOTHING`, key, raw); err != nil {
		return ChatGPTSession{}, fmt.Errorf("store: prepare chatgpt session: %w", err)
	}
	session, _, err := s.ChatGPTSession(ctx, key)
	return session, err
}

// ConnectChatGPTSession saves a validated sign-in. A concurrent first
// registration must not overwrite a different client established meanwhile.
func (s *Store) ConnectChatGPTSession(ctx context.Context, key string, c chatgpt.Credentials, wasClientID string) error {
	if err := c.Validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("store: connect chatgpt session: %w", err)
	}
	tag, err := s.app.Exec(ctx, `UPDATE chatgpt_sessions
		SET credentials = $2, signed_out_at = NULL, signed_out_reason = '', allowances = '{}'::jsonb, updated_at = now()
		WHERE key = $1 AND credentials->>'client_id' = $3`, key, raw, wasClientID)
	if err != nil {
		return fmt.Errorf("store: connect chatgpt session: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return errors.New("store: chatgpt registration changed during sign-in; try again")
	}
	return nil
}

// ChatGPTSession returns the provider's saved session, if present.
func (s *Store) ChatGPTSession(ctx context.Context, key string) (ChatGPTSession, bool, error) {
	rows, err := s.app.Query(ctx, `SELECT key, credentials, coalesce(signed_out_at, 'epoch'::timestamptz), signed_out_reason
		FROM chatgpt_sessions WHERE key = $1`, key)
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

// ChatGPTSessions returns all saved registrations for the leader's refresh.
func (s *Store) ChatGPTSessions(ctx context.Context) ([]ChatGPTSession, error) {
	rows, err := s.app.Query(ctx, `SELECT key, credentials, coalesce(signed_out_at, 'epoch'::timestamptz), signed_out_reason
		FROM chatgpt_sessions ORDER BY key`)
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
	if err := row.Scan(&s.Key, &raw, &s.SignedOutAt, &s.SignedOutReason); err != nil {
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

// RefreshChatGPTSession replaces only the token set used for this refresh.
func (s *Store) RefreshChatGPTSession(ctx context.Context, key string, c chatgpt.Credentials, was string) (bool, error) {
	raw, err := json.Marshal(c)
	if err != nil {
		return false, fmt.Errorf("store: refresh chatgpt session: %w", err)
	}
	tag, err := s.app.Exec(ctx, `UPDATE chatgpt_sessions SET credentials = $2, updated_at = now()
		WHERE key = $1 AND credentials->>'refresh_token' = $3 AND signed_out_at IS NULL`, key, raw, was)
	if err != nil {
		return false, fmt.Errorf("store: refresh chatgpt session: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// SignOutChatGPTSession clears unusable tokens while retaining the account
// and host for reauthorization. A newer sign-in remains usable.
func (s *Store) SignOutChatGPTSession(ctx context.Context, key, was, reason string) error {
	if _, err := s.app.Exec(ctx, `UPDATE chatgpt_sessions
		SET credentials = credentials || '{"access_token":"","refresh_token":"","id_token":""}'::jsonb,
		    signed_out_at = now(), signed_out_reason = $3, allowances = '{}'::jsonb, updated_at = now()
		WHERE key = $1 AND credentials->>'refresh_token' = $2 AND signed_out_at IS NULL`, key, was, reason); err != nil {
		return fmt.Errorf("store: sign out chatgpt session: %w", err)
	}
	return nil
}

// ExpireChatGPTSession asks the leader to refresh a rejected access token.
// A delayed 401 for an earlier token must not expire its replacement.
func (s *Store) ExpireChatGPTSession(ctx context.Context, key, token string) error {
	if _, err := s.app.Exec(ctx, `UPDATE chatgpt_sessions
		SET credentials = credentials || '{"expires_in":0}'::jsonb, updated_at = now()
		WHERE key = $1 AND credentials->>'access_token' = $2 AND signed_out_at IS NULL`, key, token); err != nil {
		return fmt.Errorf("store: expire chatgpt session: %w", err)
	}
	return nil
}

// UpdateChatGPTAllowances retains the newest observation of each quota bucket.
// A response from a replaced sign-in must not repopulate its allowances.
func (s *Store) UpdateChatGPTAllowances(ctx context.Context, key, token string, allowances []chatgpt.Allowance) error {
	updates := make(map[string]chatgpt.Allowance, len(allowances))
	for _, a := range allowances {
		updates[a.LimitID] = a
	}
	raw, err := json.Marshal(updates)
	if err != nil {
		return fmt.Errorf("store: encode chatgpt allowances: %w", err)
	}
	if _, err := s.app.Exec(ctx, `UPDATE chatgpt_sessions SET allowances = allowances || (
		SELECT coalesce(jsonb_object_agg(incoming.key, incoming.value), '{}'::jsonb)
		FROM jsonb_each($3::jsonb) incoming
		WHERE coalesce((allowances -> incoming.key ->> 'observedAt')::timestamptz, 'epoch'::timestamptz)
			<= (incoming.value ->> 'observedAt')::timestamptz)
		WHERE key = $1 AND credentials->>'access_token' = $2 AND signed_out_at IS NULL`, key, token, raw); err != nil {
		return fmt.Errorf("store: update chatgpt allowances: %w", err)
	}
	return nil
}

// ChatGPTAllowances exposes connection state and quota data without credentials.
func (s *Store) ChatGPTAllowances(ctx context.Context, key string) (bool, map[string]chatgpt.Allowance, error) {
	var connected bool
	var raw []byte
	err := s.app.QueryRow(ctx, `SELECT signed_out_at IS NULL AND coalesce(credentials->>'access_token', '') <> '', allowances
		FROM chatgpt_sessions WHERE key = $1`, key).Scan(&connected, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil, nil
	}
	if err != nil {
		return false, nil, fmt.Errorf("store: read chatgpt allowances: %w", err)
	}
	var allowances map[string]chatgpt.Allowance
	if err := json.Unmarshal(raw, &allowances); err != nil {
		return false, nil, fmt.Errorf("store: decode chatgpt allowances: %w", err)
	}
	return connected, allowances, nil
}
