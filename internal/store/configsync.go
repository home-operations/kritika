package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/home-operations/kritika/internal/configfile"
)

// IsConfigContentError reports whether an ApplyConfig error was caused by
// the configuration it was given rather than by the database: a value the
// schema cannot store (class 22), or a NOT NULL (23502) or CHECK (23514)
// constraint it breaks. Those depend only on the configuration. Unique
// (23505) and foreign-key (23503) violations are not counted: ingest
// inserting a repository concurrently with ApplyConfig raises one, and a
// retry succeeds.
func IsConfigContentError(err error) bool {
	pgErr, ok := errors.AsType[*pgconn.PgError](err)
	if !ok {
		return false
	}
	return strings.HasPrefix(pgErr.Code, "22") || pgErr.Code == "23502" || pgErr.Code == "23514"
}

// ApplyConfig upserts the configuration's Apps and the accounts they
// serve, disables the Apps and accounts it no longer has, and records the
// applied hash. It runs as the owner in one transaction, so a replica
// reading config_state never sees a half-applied configuration. Only the
// leader calls it.
func (s *Store) ApplyConfig(ctx context.Context, f *configfile.File) error {
	if s.owner == nil {
		return errors.New("store: applying configuration needs the owner DSN")
	}
	tx, err := s.owner.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: begin config apply: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := disableUndeclared(ctx, tx, f); err != nil {
		return err
	}
	for i := range f.Connections {
		if err := upsertConnection(ctx, tx, &f.Connections[i]); err != nil {
			return err
		}
	}
	for i := range f.Accounts {
		if err := upsertAccount(ctx, tx, &f.Accounts[i]); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO config_state (id, applied_hash) VALUES (1, $1)
		ON CONFLICT (id) DO UPDATE SET applied_hash = EXCLUDED.applied_hash`,
		f.Hash()); err != nil {
		return fmt.Errorf("store: record config state: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: commit config apply: %w", err)
	}
	return nil
}

// disableUndeclared disables every App f no longer has and every account
// no App serves. The upserts that follow re-enable what f still declares.
// Repositories are the forge's to report and are left alone.
func disableUndeclared(ctx context.Context, tx pgx.Tx, f *configfile.File) error {
	names := make([]string, 0, len(f.Connections))
	for i := range f.Connections {
		names = append(names, f.Connections[i].Name)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE connections SET enabled = false, disabled_at = coalesce(disabled_at, now()), updated_at = now()
		WHERE enabled AND name <> ALL($1::text[])`, names); err != nil {
		return fmt.Errorf("store: disable removed connections: %w", err)
	}
	ids := make([]string, 0, len(f.Accounts))
	for i := range f.Accounts {
		ids = append(ids, f.Accounts[i].ID())
	}
	if _, err := tx.Exec(ctx, `
		UPDATE accounts SET enabled = false, disabled_at = coalesce(disabled_at, now()), updated_at = now()
		WHERE enabled AND id <> ALL($1::uuid[])`, ids); err != nil {
		return fmt.Errorf("store: disable unserved accounts: %w", err)
	}
	return nil
}

// AppliedConfigHash returns the hash the leader last applied, or "" when no
// configuration has ever been applied. Any role may call it.
func (s *Store) AppliedConfigHash(ctx context.Context) (string, error) {
	var hash string
	err := s.app.QueryRow(ctx, `SELECT applied_hash FROM config_state WHERE id = 1`).Scan(&hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("store: read config state: %w", err)
	}
	return hash, nil
}

// upsertAccount enables a's row, creating it the first time a connection
// serves a, and keeps the name as the configuration spells it.
func upsertAccount(ctx context.Context, tx pgx.Tx, a *configfile.Account) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO accounts (id, forge, name) VALUES ($1, $2, $3)
		ON CONFLICT (id) DO UPDATE SET name = EXCLUDED.name, enabled = true, disabled_at = NULL, updated_at = now()`,
		a.ID(), string(a.Forge), a.Name); err != nil {
		return fmt.Errorf("store: upsert account %s: %w", a.Key(), err)
	}
	return nil
}

func upsertConnection(ctx context.Context, tx pgx.Tx, in *configfile.Connection) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO connections (id, name, forge, accounts) VALUES ($1, $2, $3, $4)
		ON CONFLICT (name) DO UPDATE SET
			forge = EXCLUDED.forge, accounts = EXCLUDED.accounts, enabled = true, disabled_at = NULL, updated_at = now()`,
		in.ID(), in.Name, string(in.Forge), in.Accounts); err != nil {
		return fmt.Errorf("store: upsert connection %s: %w", in.Name, err)
	}
	return nil
}
