package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// APIToken is a personal access token that a user created for an agent. It
// is a read-only copy of what the sign-in its session came from let them
// read: every account, or the forge accounts that Grant names. It stands
// while that sign-in's configuration still matches Grant.Key, until
// ExpiresAt, nil for never. Grant.Role is always RoleMember: a token never
// administers.
type APIToken struct {
	ID         string
	User       User
	Name       string
	Identity   SignInIdentity
	Grant      SessionGrant
	CreatedAt  time.Time
	ExpiresAt  *time.Time
	LastUsedAt *time.Time
}

// APITokenPrefix starts every token, so that a leaked one is recognisable.
const APITokenPrefix = "kritika_"

// Session is the session t reads as: its user, the sign-in its grant came
// from, and that grant.
func (t APIToken) Session() Session {
	return Session{User: t.User, Identity: t.Identity, Grant: t.Grant}
}

// apiTokenColumns are the columns scanAPIToken reads, from api_tokens t
// joined to users u.
const apiTokenColumns = `t.id, t.name, t.provider, t.provider_origin, t.all_accounts, t.accounts, t.grant_key,
	t.created_at, t.expires_at, t.last_used_at, u.id, u.display_name, u.email, u.email_verified, u.avatar_url`

func scanAPIToken(row pgx.Row) (APIToken, error) {
	t := APIToken{Grant: SessionGrant{Role: RoleMember}}
	err := row.Scan(&t.ID, &t.Name, &t.Identity.Provider, &t.Identity.Origin, &t.Grant.AllAccounts, &t.Grant.Accounts, &t.Grant.Key,
		&t.CreatedAt, &t.ExpiresAt, &t.LastUsedAt, &t.User.ID, &t.User.DisplayName, &t.User.Email, &t.User.EmailVerified, &t.User.AvatarURL)
	return t, err
}

// CreateAPIToken stores t for its user, created at t.CreatedAt, and returns
// the token's value, of which only the SHA-256 is kept, and its id.
func CreateAPIToken(ctx context.Context, tx pgx.Tx, t APIToken) (token, id string, err error) {
	accounts := t.Grant.Accounts
	if accounts == nil {
		accounts = []string{}
	}
	token = APITokenPrefix + RandomToken()
	err = tx.QueryRow(ctx, `INSERT INTO api_tokens
		(token_hash, user_id, name, provider, provider_origin, all_accounts, accounts, grant_key, created_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) RETURNING id`,
		tokenHash(token), t.User.ID, t.Name, t.Identity.Provider, t.Identity.Origin, t.Grant.AllAccounts, accounts, t.Grant.Key,
		t.CreatedAt, t.ExpiresAt).Scan(&id)
	if err != nil {
		return "", "", fmt.Errorf("store: create api token: %w", err)
	}
	return token, id, nil
}

// LookupAPIToken returns the token that a value names, with its user, unless
// it expired by now, and records its use at most once a minute. Otherwise it
// returns ErrSession.
func (s *Store) LookupAPIToken(ctx context.Context, token string, now time.Time) (APIToken, error) {
	t, err := scanAPIToken(s.app.QueryRow(ctx, `SELECT `+apiTokenColumns+`
		FROM api_tokens t JOIN users u ON u.id = t.user_id
		WHERE t.token_hash = $1 AND (t.expires_at IS NULL OR t.expires_at > $2)`, tokenHash(token), now))
	if errors.Is(err, pgx.ErrNoRows) {
		return APIToken{}, ErrSession
	}
	if err != nil {
		return APIToken{}, fmt.Errorf("store: look up api token: %w", err)
	}
	if t.LastUsedAt == nil || now.Sub(*t.LastUsedAt) >= time.Minute {
		marked, err := markAPITokenUsed(ctx, s.app, t.ID, now)
		if err != nil {
			return APIToken{}, fmt.Errorf("store: look up api token: %w", err)
		}
		if marked {
			t.LastUsedAt = &now
		}
	}
	return t, nil
}

// markAPITokenUsed records the token's use at now, and reports whether it
// did: not within a minute of the last recorded use, nor before it. The
// condition is the update's own, so it holds against a concurrent lookup
// that read the same last use.
func markAPITokenUsed(ctx context.Context, db interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}, id string, now time.Time) (bool, error) {
	tag, err := db.Exec(ctx, `UPDATE api_tokens SET last_used_at = $2
		WHERE id = $1 AND (last_used_at IS NULL OR last_used_at <= $2::timestamptz - interval '1 minute')`, id, now)
	return tag.RowsAffected() == 1, err
}

// ListAPITokens returns the tokens of the user, or of every user when
// userID is "", the newest first.
func ListAPITokens(ctx context.Context, tx pgx.Tx, userID string) ([]APIToken, error) {
	rows, err := tx.Query(ctx, `SELECT `+apiTokenColumns+`
		FROM api_tokens t JOIN users u ON u.id = t.user_id
		WHERE $1 = '' OR t.user_id::text = $1
		ORDER BY t.created_at DESC, t.id`, userID)
	if err != nil {
		return nil, fmt.Errorf("store: list api tokens: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (APIToken, error) { return scanAPIToken(row) })
	if err != nil {
		return nil, fmt.Errorf("store: list api tokens: %w", err)
	}
	return out, nil
}

// DeleteAPIToken deletes the token with id and returns it as it was. When
// userID is not "", it deletes the token only if it is the user's. It returns
// ErrNotFound when there is no such token.
func DeleteAPIToken(ctx context.Context, tx pgx.Tx, id, userID string) (APIToken, error) {
	t, err := scanAPIToken(tx.QueryRow(ctx, `WITH gone AS (
			DELETE FROM api_tokens WHERE id::text = $1 AND ($2 = '' OR user_id::text = $2) RETURNING *)
		SELECT `+apiTokenColumns+` FROM gone t JOIN users u ON u.id = t.user_id`, id, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return APIToken{}, ErrNotFound
	}
	if err != nil {
		return APIToken{}, fmt.Errorf("store: delete api token: %w", err)
	}
	return t, nil
}
