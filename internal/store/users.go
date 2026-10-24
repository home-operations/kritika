package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
)

// UserSummary is a user as an admin lists them: their sign-ins, by
// provider, origin and login, when they first appeared and last signed in,
// and their sessions still live, newest first.
type UserSummary struct {
	User
	CreatedAt    time.Time
	LastSignInAt *time.Time
	Identities   []SignInIdentity
	Sessions     []Session
}

// ListUsers returns every user, the most recently signed in first, with
// the sessions still live at now.
func (s *Store) ListUsers(ctx context.Context, now time.Time) ([]UserSummary, error) {
	var out []UserSummary
	err := pgx.BeginFunc(ctx, s.app, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id, display_name, email, email_verified, avatar_url, created_at, last_sign_in_at
			FROM users ORDER BY last_sign_in_at DESC NULLS LAST, created_at DESC, id`)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (UserSummary, error) {
			var u UserSummary
			err := row.Scan(&u.ID, &u.DisplayName, &u.Email, &u.EmailVerified, &u.AvatarURL, &u.CreatedAt, &u.LastSignInAt)
			return u, err
		})
		if err != nil {
			return err
		}
		index := make(map[string]*UserSummary, len(out))
		for i := range out {
			index[out[i].ID] = &out[i]
		}
		var userID string
		var id SignInIdentity
		rows, err = tx.Query(ctx, `SELECT user_id, provider, origin, login FROM identities ORDER BY created_at, provider, subject`)
		if err != nil {
			return err
		}
		if _, err := pgx.ForEachRow(rows, []any{&userID, &id.Provider, &id.Origin, &id.Login}, func() error {
			if u := index[userID]; u != nil {
				u.Identities = append(u.Identities, id)
			}
			return nil
		}); err != nil {
			return err
		}
		var sess Session
		rows, err = tx.Query(ctx, `SELECT user_id, provider, provider_origin, role, all_accounts, accounts, grant_key
			FROM sessions WHERE expires_at > $1 ORDER BY created_at DESC, token_hash`, now)
		if err != nil {
			return err
		}
		_, err = pgx.ForEachRow(rows, []any{&userID, &sess.Identity.Provider, &sess.Identity.Origin, &sess.Grant.Role,
			&sess.Grant.AllAccounts, &sess.Grant.Accounts, &sess.Grant.Key}, func() error {
			if u := index[userID]; u != nil {
				sess.User = u.User
				kept := sess
				kept.Grant.Accounts = slices.Clone(sess.Grant.Accounts)
				u.Sessions = append(u.Sessions, kept)
			}
			return nil
		})
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("store: list users: %w", err)
	}
	return out, nil
}

// GetUser returns the user with id, or ErrNotFound.
func GetUser(ctx context.Context, tx pgx.Tx, id string) (User, error) {
	var u User
	err := tx.QueryRow(ctx, `SELECT id, display_name, email, email_verified, avatar_url FROM users WHERE id = $1`, id).
		Scan(&u.ID, &u.DisplayName, &u.Email, &u.EmailVerified, &u.AvatarURL)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("store: get user: %w", err)
	}
	return u, nil
}

// DeleteUserSessions ends every session of the user, and reports how many
// there were.
func DeleteUserSessions(ctx context.Context, tx pgx.Tx, userID string) (int64, error) {
	tag, err := tx.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1`, userID)
	if err != nil {
		return 0, fmt.Errorf("store: delete user sessions: %w", err)
	}
	return tag.RowsAffected(), nil
}
