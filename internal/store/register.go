package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/configfile"
)

// ReachedRepository is a repository as the forge reports it. Traits is nil
// when the report names it without saying what it is, as an installation
// event does. The row keeps what it knew, or starts as neither archived
// nor a fork.
type ReachedRepository struct {
	FullName, DefaultBranch string
	Traits                  *configfile.RepoTraits
}

// RegisterRepositories records the repositories of account accountID that
// its connection's App reaches, as a webhook from each does. So polling and
// onboarding know them before any event arrives. repos is the account's whole
// listing, taken from listedAt on, by the database's clock as Now reads it.
// The forge-reported repositories that it lacks, deleted, renamed, transferred
// or no longer reached, are disabled as an installation event does, unless
// named since listedAt. A webhook can name them after the listing. An empty
// listing disables nothing, so a bad one never disables a whole account. It
// returns how many were new and how many it disabled.
func (s *Store) RegisterRepositories(
	ctx context.Context, accountID string, repos []ReachedRepository, listedAt time.Time,
) (added, disabled int, err error) {
	if len(repos) == 0 {
		return 0, 0, nil
	}
	err = s.WithAccount(ctx, accountID, func(tx pgx.Tx) error {
		ids := make([]string, 0, len(repos))
		for _, r := range repos {
			id, isNew, err := ensureRepository(ctx, tx, accountID, r, &listedAt)
			if err != nil {
				return err
			}
			ids = append(ids, id)
			if isNew {
				added++
			}
		}
		tag, err := tx.Exec(ctx, `UPDATE repositories SET enabled = false, disabled_at = coalesce(disabled_at, now()), updated_at = now()
			WHERE account_id = $1 AND enabled AND id <> ALL($2::uuid[]) AND updated_at < $3`,
			accountID, ids, listedAt)
		if err != nil {
			return fmt.Errorf("store: disable repositories no longer listed: %w", err)
		}
		disabled = int(tag.RowsAffected())
		return nil
	})
	return added, disabled, err
}

// EnsureRepository records repository r of account accountID as the forge
// names it, with its spelling, and returns its id and whether it is new.
// The row is reachable (enabled), since the forge just named it, but
// starts turned off until an admin enables it in the dashboard. Existing
// rows keep their turned_on choice. A DefaultBranch of "" leaves the known
// one. A nil Traits leaves the known traits, and a Visibility of "" leaves
// the known visibility.
//
// A row enabled again loses the time when the index sweep found it stopped.
// It can run, and stop again, before the next sweep sees it. updated_at is
// the time of the write, not of the transaction's start, for
// RegisterRepositories to leave a row named while a listing ran.
func EnsureRepository(ctx context.Context, tx pgx.Tx, accountID string, r ReachedRepository) (id string, isNew bool, err error) {
	return ensureRepository(ctx, tx, accountID, r, nil)
}

// ensureRepository is EnsureRepository for a report taken at listedAt, nil
// for one just made. What a listing says of a repository, its default branch
// and traits, is left aside on a row written since the listing was taken.
// That it is reachable is left aside on a row disabled since. A webhook that
// says something newer can do either.
func ensureRepository(
	ctx context.Context, tx pgx.Tx, accountID string, r ReachedRepository, listedAt *time.Time,
) (id string, isNew bool, err error) {
	var archived, fork *bool
	var visibility string
	if r.Traits != nil {
		archived, fork, visibility = &r.Traits.Archived, &r.Traits.Fork, r.Traits.Visibility
	}
	// xmax is 0 only on a row the statement inserted, not one it updated.
	err = tx.QueryRow(ctx, `
		INSERT INTO repositories (id, account_id, name, default_branch, enabled, turned_on, archived, fork, visibility, updated_at)
		VALUES ($1, $2, $3, $4, true, false, coalesce($5, false), coalesce($6, false), $7, clock_timestamp())
		ON CONFLICT (id) DO UPDATE SET
			name = EXCLUDED.name,
			default_branch = CASE WHEN EXCLUDED.default_branch <> '' AND ($8::timestamptz IS NULL OR repositories.updated_at < $8)
				THEN EXCLUDED.default_branch ELSE repositories.default_branch END,
			enabled = CASE WHEN $8::timestamptz IS NULL OR repositories.disabled_at IS NULL OR repositories.disabled_at < $8
				THEN true ELSE repositories.enabled END,
			disabled_at = CASE WHEN $8::timestamptz IS NULL OR repositories.disabled_at IS NULL OR repositories.disabled_at < $8
				THEN NULL ELSE repositories.disabled_at END,
			stopped_at = CASE WHEN NOT repositories.enabled
				AND ($8::timestamptz IS NULL OR repositories.disabled_at IS NULL OR repositories.disabled_at < $8)
				THEN NULL ELSE repositories.stopped_at END,
			archived = CASE WHEN $8::timestamptz IS NULL OR repositories.updated_at < $8
				THEN coalesce($5, repositories.archived) ELSE repositories.archived END,
			fork = CASE WHEN $8::timestamptz IS NULL OR repositories.updated_at < $8 THEN coalesce($6, repositories.fork) ELSE repositories.fork END,
			visibility = CASE WHEN EXCLUDED.visibility <> '' AND ($8::timestamptz IS NULL OR repositories.updated_at < $8)
				THEN EXCLUDED.visibility ELSE repositories.visibility END,
			updated_at = clock_timestamp()
		RETURNING id, xmax = 0`,
		configfile.RepositoryID(accountID, r.FullName), accountID, r.FullName, r.DefaultBranch, archived, fork, visibility, listedAt).
		Scan(&id, &isNew)
	if err != nil {
		return "", false, fmt.Errorf("store: ensure repository %s: %w", r.FullName, err)
	}
	return id, isNew, nil
}

// RepositoryTraits is what is known of the repository with id, the
// dashboard's choice included, after recording what reported says of it,
// if anything: that it is archived or a fork, and its visibility when the
// payload names one. A payload without one leaves the known visibility,
// which is what it returns. nil records nothing, for a report that only
// repeats what the store already said. The row is written only when the
// report changes it, so a pull request's every push does not stamp a known
// repository. A repository not known returns ErrNotFound.
func RepositoryTraits(ctx context.Context, tx pgx.Tx, id string, reported *configfile.RepoTraits) (configfile.RepoTraits, error) {
	var known configfile.RepoTraits
	if reported != nil {
		// One statement, so the visibility a payload without one keeps is
		// the row's as written, not as read a moment before.
		err := tx.QueryRow(ctx, `UPDATE repositories SET archived = $2, fork = $3,
				visibility = CASE WHEN $4 <> '' THEN $4 ELSE visibility END, updated_at = clock_timestamp()
			WHERE id = $1 AND (archived <> $2 OR fork <> $3 OR ($4 <> '' AND visibility <> $4))
			RETURNING archived, fork, visibility, turned_on`, id, reported.Archived, reported.Fork, reported.Visibility).
			Scan(&known.Archived, &known.Fork, &known.Visibility, &known.TurnedOn)
		if err == nil {
			return known, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return known, fmt.Errorf("store: record repository %s traits: %w", id, err)
		}
	}
	err := tx.QueryRow(ctx, `SELECT archived, fork, visibility, turned_on FROM repositories WHERE id = $1`, id).
		Scan(&known.Archived, &known.Fork, &known.Visibility, &known.TurnedOn)
	if errors.Is(err, pgx.ErrNoRows) {
		return known, ErrNotFound
	}
	if err != nil {
		return known, fmt.Errorf("store: read repository %s: %w", id, err)
	}
	return known, nil
}

// PreserveRenamedEnablement carries the old name's enablement within the
// account, unless an admin has already chosen a state for the new name.
func PreserveRenamedEnablement(ctx context.Context, tx pgx.Tx, previousID, id string) error {
	_, err := tx.Exec(ctx, `UPDATE repositories r SET turned_on = old.turned_on, turned_at = old.turned_at
		FROM repositories old WHERE old.id = $1 AND r.id = $2 AND r.account_id = old.account_id AND r.turned_at IS NULL`,
		previousID, id)
	if err != nil {
		return fmt.Errorf("store: preserve renamed repository enablement: %w", err)
	}
	return nil
}

// TurnOn records an admin's choice to review the repository with id or
// not, and when it was made. Turning it on clears the time the index sweep
// found it stopped, as EnsureRepository does enabling it.
func TurnOn(ctx context.Context, tx pgx.Tx, id string, on bool) error {
	tag, err := tx.Exec(ctx, `UPDATE repositories SET turned_on = $2, turned_at = now(),
		stopped_at = CASE WHEN $2 THEN NULL ELSE stopped_at END, updated_at = now() WHERE id = $1`, id, on)
	if err != nil {
		return fmt.Errorf("store: turn repository %s on or off: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
