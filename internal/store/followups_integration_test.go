//go:build integration

package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/home-operations/kritika/internal/configfile"
)

// followupsWithLimited puts back the follow-ups' check that admits the
// limited status, which 0072_followups_unlimited replaces.
const followupsWithLimited = `ALTER TABLE followups DROP CONSTRAINT followups_status_check;
	ALTER TABLE followups ADD CONSTRAINT followups_status_check CHECK (status IN ('answered', 'limited', 'ignored', 'failed'))`

// TestFollowupsMigrationDropsLimited: 0072_followups_unlimited records each
// account's limited mentions as ignored, with why, and drops the limit
// notice that a reply ID names. It leaves the other follow-ups as they are,
// and the check then refuses the limited status.
func TestFollowupsMigrationDropsLimited(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, twoAccounts)); err != nil {
		t.Fatal(err)
	}
	listRepos(t, s, "alpha", "alpha/one")
	listRepos(t, s, "beta", "beta/followups")
	alpha, beta := accountID(t, s, "alpha"), accountID(t, s, "beta")
	alphaRepo, betaRepo := configfile.RepositoryID(alpha, "alpha/one"), configfile.RepositoryID(beta, "beta/followups")
	// The suites that share the database count beta's repositories.
	t.Cleanup(func() {
		ctx := context.Background()
		for _, stmt := range []string{
			`DELETE FROM followups WHERE pull_request_id IN (SELECT id FROM pull_requests WHERE repository_id = $1)`,
			`DELETE FROM pull_requests WHERE repository_id = $1`,
			`DELETE FROM repositories WHERE id = $1`,
		} {
			if _, err := s.owner.Exec(ctx, stmt, betaRepo); err != nil {
				t.Error(err)
			}
		}
	})
	pullRequest := func(account, repo string) string {
		t.Helper()
		var id string
		if err := s.owner.QueryRow(ctx, `INSERT INTO pull_requests (account_id, repository_id, number, head_sha)
			VALUES ($1, $2, (random() * 1e6)::int, 'abc') RETURNING id`, account, repo).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	alphaPR, betaPR := pullRequest(alpha, alphaRepo), pullRequest(beta, betaRepo)
	t.Cleanup(func() {
		ctx := context.Background()
		if err := s.Migrate(ctx, "kritika_app"); err != nil {
			t.Errorf("Migrate in the cleanup: %v", err)
		}
		if _, err := s.owner.Exec(ctx, `DELETE FROM followups WHERE pull_request_id = $1`, alphaPR); err != nil {
			t.Error(err)
		}
	})
	if err := pgx.BeginFunc(ctx, s.owner, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, followupsWithLimited+`;
			DELETE FROM schema_migrations WHERE version = '0072_followups_unlimited'`); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO followups (account_id, pull_request_id, comment_id, inline, status, reason, reply_comment_id) VALUES
			($1, $2, 1, false, 'limited', '', 900),
			($1, $2, 2, true, 'limited', '', NULL),
			($1, $2, 3, true, 'answered', '', 901),
			($1, $2, 4, true, 'ignored', 'thread is resolved', 902),
			($1, $2, 5, false, 'failed', 'forge refused the reply', NULL),
			($3, $4, 6, true, 'limited', '', 903)`, alpha, alphaPR, beta, betaPR)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(ctx, "kritika_app"); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	rows, err := s.owner.Query(ctx, `SELECT format('%s %s %s (%s) %s', comment_id, inline::text, status, reason, coalesce(reply_comment_id, 0))
		FROM followups WHERE pull_request_id IN ($1, $2) ORDER BY comment_id`, alphaPR, betaPR)
	if err != nil {
		t.Fatal(err)
	}
	got, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	const limited = "the pull request had its follow-ups for the hour"
	want := []string{
		fmt.Sprintf("1 false ignored (%s) 0", limited),
		fmt.Sprintf("2 true ignored (%s) 0", limited),
		"3 true answered () 901",
		"4 true ignored (thread is resolved) 902",
		"5 false failed (forge refused the reply) 0",
		fmt.Sprintf("6 true ignored (%s) 0", limited),
	}
	if !slices.Equal(got, want) {
		t.Fatalf("follow-ups after the migration:\n%q\nwant\n%q", got, want)
	}
	_, err = s.owner.Exec(ctx, `INSERT INTO followups (account_id, pull_request_id, comment_id, status) VALUES ($1, $2, 7, 'limited')`,
		alpha, alphaPR)
	if pgerr, ok := errors.AsType[*pgconn.PgError](err); !ok || pgerr.Code != "23514" || pgerr.ConstraintName != "followups_status_check" {
		t.Fatalf("a limited follow-up = %v, want the status check to refuse it", err)
	}
}
