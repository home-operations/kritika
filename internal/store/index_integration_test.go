//go:build integration

package store

import (
	"context"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/configfile"
)

const onboardAccounts = `
apps:
  alpha-bot:
    accounts: [alpha]
    clientId: Iv1.test
    privateKey: { env: KRITIKA_TEST_TOKEN }
    webhookSecret: { env: KRITIKA_TEST_TOKEN }
  beta-bot:
    accounts: [beta]
    clientId: Iv1.beta-bot
    privateKey: { env: KRITIKA_TEST_TOKEN }
    webhookSecret: { env: KRITIKA_TEST_TOKEN }
  east-bot:
    accounts: [east]
    clientId: Iv1.east-bot
    privateKey: { env: KRITIKA_TEST_TOKEN }
    webhookSecret: { env: KRITIKA_TEST_TOKEN }
  west-bot:
    accounts: [west]
    clientId: Iv1.west-bot
    privateKey: { env: KRITIKA_TEST_TOKEN }
    webhookSecret: { env: KRITIKA_TEST_TOKEN }
`

// checkBacklog reads account's onboarding backlog under its own policy and
// checks it names want, in order; names maps a repository ID to its name.
func checkBacklog(t *testing.T, s *Store, account string, names map[string]string, want []string) {
	t.Helper()
	ctx := t.Context()
	var backlog []string
	if err := s.WithAccount(ctx, accountID(t, s, account), func(tx pgx.Tx) error {
		waiting, err := OnboardBacklog(ctx, tx, time.Hour)
		for _, r := range waiting {
			backlog = append(backlog, names[r.ID])
		}
		return err
	}); err != nil {
		t.Fatalf("OnboardBacklog: %v", err)
	}
	if !slices.Equal(backlog, want) {
		t.Fatalf("OnboardBacklog(%s) = %v, want %v", account, backlog, want)
	}
}

// TestOnboardCandidates checks which repositories the onboarding feeder is
// offered and in what order: accounts take turns, and an account's repositories
// whose pull requests moved last go first. A repository whose index another
// embedder built is offered again.
func TestOnboardCandidates(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, onboardAccounts)); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}
	listRepos(t, s, "alpha", "alpha/one", "alpha/two")
	listRepos(t, s, "east", "east/busy", "east/quiet", "east/later", "east/rebuilt", "east/skipped", "east/failed", "east/queued", "east/indexed", "east/stale", "east/off")
	listRepos(t, s, "west", "west/one", "west/old-failure")
	if _, err := s.EnsureIndexSchema(ctx, "kritika_app", "m", 8, ""); err != nil {
		t.Fatalf("EnsureIndexSchema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = s.owner.Exec(context.Background(), `UPDATE repositories SET active_index_run_id = NULL;
			DROP TABLE IF EXISTS index_chunks; DELETE FROM index_schema`)
	})
	ids := map[string]string{}
	names := map[string]string{}
	rows, err := s.owner.Query(ctx, `SELECT id, name FROM repositories WHERE name LIKE 'east/%' OR name LIKE 'west/%'`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			t.Fatal(err)
		}
		ids[name], names[id] = id, name
	}
	if rows.Err() != nil || len(ids) != 12 {
		t.Fatalf("repositories = %v, %v", ids, rows.Err())
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := s.owner.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	// The App lost east/off.
	exec(`UPDATE repositories SET enabled = false, disabled_at = now() WHERE id = $1`, ids["east/off"])
	t.Cleanup(func() {
		_, _ = s.owner.Exec(context.Background(), `DELETE FROM river_job WHERE args->>'repository_id' = ANY($1)`, slices.Collect(maps.Values(ids)))
	})
	// One apply gives every repository the same created_at.
	for name, age := range map[string]string{"east/quiet": "3 hours", "west/old-failure": "2 hours", "east/later": "1 hour", "east/rebuilt": "30 minutes"} {
		exec(`UPDATE repositories SET created_at = now() - $2::interval WHERE id = $1`, ids[name], age)
	}
	for name, age := range map[string]string{"east/busy": "1 minute", "west/one": "1 day"} {
		exec(`INSERT INTO pull_requests (account_id, repository_id, number, head_sha, updated_at)
			SELECT account_id, id, 1, 'abc', now() - $2::interval FROM repositories WHERE id = $1`, ids[name], age)
	}
	// east/stale's index was built with another document prompt.
	exec(`WITH g AS (
			INSERT INTO index_runs (account_id, repository_id, commit_sha, embed_model, embed_dims, document_prompt, mode, status)
			SELECT r.account_id, r.id, 'abc', 'm', 8, p.prompt, 'full', 'completed'
			FROM repositories r JOIN (VALUES ($1::uuid, ''), ($2::uuid, 'passage: ')) p (id, prompt) ON p.id = r.id
			RETURNING id, repository_id)
		UPDATE repositories r SET active_index_run_id = g.id FROM g WHERE r.id = g.repository_id`, ids["east/indexed"], ids["east/stale"])

	before, err := s.OnboardingInFlight(ctx)
	if err != nil {
		t.Fatalf("OnboardingInFlight: %v", err)
	}
	// job queues an index job, or with a finished age, one that finished
	// that long ago.
	job := func(name, trigger, state, finished string) {
		t.Helper()
		exec(`INSERT INTO river_job (kind, queue, args, max_attempts, state, finalized_at)
			VALUES ('index', 'index', jsonb_build_object('repository_id', $1::text, 'trigger', $2::text), 3, $3,
				now() - nullif($4, '')::interval)`,
			ids[name], trigger, state, finished)
	}
	job("east/queued", "push", "running", "")
	job("east/failed", "onboard", "discarded", "10 minutes")
	job("west/old-failure", "onboard", "discarded", "2 hours")
	job("east/later", "push", "completed", "1 minute")
	job("east/skipped", "onboard", "completed", "5 minutes")
	// An onboarding that built an index since dropped, as a model change
	// drops them all, is no reason to wait.
	job("east/rebuilt", "onboard", "completed", "5 minutes")
	exec(`INSERT INTO index_runs (account_id, repository_id, commit_sha, embed_model, embed_dims, mode, status)
		SELECT account_id, id, 'abc', 'm', 8, 'full', 'superseded' FROM repositories WHERE id = $1`, ids["east/rebuilt"])
	after, err := s.OnboardingInFlight(ctx)
	if err != nil || after != before {
		t.Fatalf("OnboardingInFlight = %d, %v; want %d: pushes and finished jobs are not onboarding", after, err, before)
	}
	job("east/queued", "onboard", "retryable", "")
	if after, err := s.OnboardingInFlight(ctx); err != nil || after != before+1 {
		t.Fatalf("OnboardingInFlight = %d, %v; want %d", after, err, before+1)
	}

	all := func(string, configfile.RepoTraits) bool { return true }
	refs, err := s.OnboardCandidates(ctx, 1000, time.Hour, all)
	if err != nil {
		t.Fatalf("OnboardCandidates: %v", err)
	}
	var got []string
	for _, r := range refs {
		if name, ok := names[r.ID]; ok {
			got = append(got, name)
		}
	}
	want := []string{"east/busy", "west/one", "east/quiet", "west/old-failure", "east/later", "east/rebuilt", "east/stale"}
	if !slices.Equal(got, want) {
		t.Fatalf("candidates = %v, want %v", got, want)
	}
	if limited, err := s.OnboardCandidates(ctx, 1, time.Hour, all); err != nil || len(limited) != 1 {
		t.Fatalf("OnboardCandidates(1) = %v, %v", limited, err)
	}
	// The dashboard's backlog of one account is the feeder's order, less
	// the other accounts' turns.
	checkBacklog(t, s, "east", names, []string{"east/busy", "east/quiet", "east/later", "east/rebuilt", "east/stale"})
	// A repository its settings leave off is passed over, and takes none
	// of the limit: here, every one of east's.
	west := accountID(t, s, "west")
	ours := func(account string, _ configfile.RepoTraits) bool { return account == west }
	if limited, err := s.OnboardCandidates(ctx, 1, time.Hour, ours); err != nil || len(limited) != 1 || names[limited[0].ID] != "west/one" {
		t.Fatalf("OnboardCandidates(1, without east's) = %v, %v; want west/one", limited, err)
	}
	// The callback sees what the forge says of each: here, that the
	// busiest is a fork.
	exec(`UPDATE repositories SET fork = true WHERE id = $1`, ids["east/busy"])
	t.Cleanup(func() {
		_, _ = s.owner.Exec(context.Background(), `UPDATE repositories SET fork = false WHERE id = $1`, ids["east/busy"])
	})
	east := accountID(t, s, "east")
	sources := func(account string, traits configfile.RepoTraits) bool {
		return (account == east || account == west) && !traits.Fork
	}
	if limited, err := s.OnboardCandidates(ctx, 1, time.Hour, sources); err != nil || len(limited) != 1 || names[limited[0].ID] != "west/one" {
		t.Fatalf("OnboardCandidates(1, no forks) = %v, %v; want west/one", limited, err)
	}
}
