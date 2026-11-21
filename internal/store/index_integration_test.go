//go:build integration

package store

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/runspec"
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
// makes sure that it names want, in order. names maps a repository ID to its
// name.
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

// checkMixed checks whether FindGeneration says that a step of another
// index format advanced the repository's active generation.
func checkMixed(t *testing.T, s *Store, account, repository string, want bool) {
	t.Helper()
	ctx := t.Context()
	var g Generation
	if err := s.WithAccount(ctx, accountID(t, s, account), func(tx pgx.Tx) error {
		var run string
		if err := tx.QueryRow(ctx, `SELECT active_index_run_id::text FROM repositories WHERE id = $1`, repository).Scan(&run); err != nil {
			return err
		}
		var err error
		g, err = FindGeneration(ctx, tx, run)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if g.Mixed != want {
		t.Fatalf("generation of %s mixed = %v, want %v", repository, g.Mixed, want)
	}
}

// TestMixedWhateverTheClock: a generation is mixed once a step of another
// index format completes on it, and not by one that completed on the
// generation before it, whatever the database clock said when each run
// started and the generation was published. A clock can step back, or a
// failover land on a server whose clock is behind, while a rollout is part
// done.
func TestMixedWhateverTheClock(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, soloAccount("mixed-clock"))); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}
	account := accountID(t, s, "mixed-clock")
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := s.owner.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	for _, tt := range []struct {
		name string
		// before is a step of another format that completes before the
		// generation is published, on the one before it, rather than after.
		before bool
		// started is when, by the clock, the step's run started, from when
		// the generation is published.
		started string
		want    bool
	}{
		{"a step that started before the generation, by a clock that stepped back", false, "-1 hour", true},
		{"a step that advanced the generation before, by a clock ahead", true, "1 hour", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			name := "mixed-clock/" + strings.ReplaceAll(tt.name, " ", "-")
			listRepos(t, s, "mixed-clock", name)
			var repo string
			if err := s.owner.QueryRow(ctx, `SELECT id FROM repositories WHERE name = $1`, name).Scan(&repo); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				ctx := context.Background()
				_, _ = s.owner.Exec(ctx, `UPDATE repositories SET active_index_run_id = NULL WHERE id = $1`, repo)
				_, _ = s.owner.Exec(ctx, `DELETE FROM index_runs WHERE repository_id = $1`, repo)
			})
			publish := func() {
				t.Helper()
				exec(`WITH g AS (INSERT INTO index_runs (account_id, repository_id, commit_sha, embed_model, embed_dims, mode, status, index_format,
						finished_at) VALUES ($1, $2, 'abc', 'm', 8, 'full', 'completed', $3, now()) RETURNING id, repository_id)
				UPDATE repositories r SET active_index_run_id = g.id FROM g WHERE r.id = g.repository_id`, account, repo, runspec.IndexFormat)
			}
			step := func() {
				t.Helper()
				var run string
				if err := s.owner.QueryRow(ctx, `INSERT INTO index_runs (account_id, repository_id, commit_sha, embed_model, embed_dims, mode,
						status, index_format, created_at)
					VALUES ($1, $2, 'def', 'm', 8, 'incremental', 'running', $3, now() + $4::interval) RETURNING id`,
					account, repo, runspec.IndexFormat-1, tt.started).Scan(&run); err != nil {
					t.Fatal(err)
				}
				if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
					return FinishIndexRun(ctx, tx, run, IndexCompleted, 1, "")
				}); err != nil {
					t.Fatal(err)
				}
			}
			publish()
			step()
			if tt.before {
				publish()
			}
			checkMixed(t, s, "mixed-clock", repo, tt.want)
		})
	}
}

// TestMixedReadsOtherFormats: whether a step of another index format
// advanced a generation, which a search asks each time it runs, reads the
// lowest and the highest format of the steps that advanced it alone. Neither
// the repository's history of runs, which grows with each push, nor the rest
// of the steps of the generation's own format, which do too while it stays
// active, is read. A step of a format either below or above the
// generation's is found.
func TestMixedReadsOtherFormats(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, soloAccount("mixed-plan"))); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}
	account := accountID(t, s, "mixed-plan")
	listRepos(t, s, "mixed-plan", "mixed-plan/one")
	var repo, gen string
	if err := s.owner.QueryRow(ctx, `SELECT id FROM repositories WHERE name = 'mixed-plan/one'`).Scan(&repo); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = s.owner.Exec(ctx, `UPDATE repositories SET active_index_run_id = NULL WHERE id = $1`, repo)
		_, _ = s.owner.Exec(ctx, `DELETE FROM index_runs WHERE repository_id = $1`, repo)
	})
	// The history: generations that were superseded, and a step that
	// advanced each. The trigger records a completed step as advancing the
	// active generation, none yet, so each step's is set after, by an update
	// that leaves status as it is, which the trigger does not see.
	const history, steps = 2000, 1000
	if _, err := s.owner.Exec(ctx, `INSERT INTO index_runs (account_id, repository_id, commit_sha, embed_model, embed_dims, mode, status,
			index_format)
		SELECT $1, $2, 'old', 'm', 8, CASE WHEN i % 2 = 0 THEN 'full' ELSE 'incremental' END,
			CASE WHEN i % 2 = 0 THEN 'superseded' ELSE 'completed' END, $3
		FROM generate_series(1, $4) AS i`, account, repo, runspec.IndexFormat, history); err != nil {
		t.Fatal(err)
	}
	if _, err := s.owner.Exec(ctx, `UPDATE index_runs st SET stepped_run_id = g.id
		FROM (SELECT id, row_number() OVER (ORDER BY id) AS n FROM index_runs
				WHERE repository_id = $1 AND commit_sha = 'old' AND mode = 'full') g,
			(SELECT id, row_number() OVER (ORDER BY id) AS n FROM index_runs
				WHERE repository_id = $1 AND commit_sha = 'old' AND mode = 'incremental') h
		WHERE st.id = h.id AND h.n = g.n`, repo); err != nil {
		t.Fatal(err)
	}
	if err := s.owner.QueryRow(ctx, `WITH g AS (INSERT INTO index_runs (account_id, repository_id, commit_sha, embed_model, embed_dims, mode,
			status, index_format) VALUES ($1, $2, 'abc', 'm', 8, 'full', 'completed', $3) RETURNING id, repository_id)
		UPDATE repositories r SET active_index_run_id = g.id FROM g WHERE r.id = g.repository_id RETURNING g.id`,
		account, repo, runspec.IndexFormat).Scan(&gen); err != nil {
		t.Fatal(err)
	}
	// The generation's own steps, which the trigger records as advancing
	// it.
	if _, err := s.owner.Exec(ctx, `INSERT INTO index_runs (account_id, repository_id, commit_sha, embed_model, embed_dims, mode, status,
			index_format)
		SELECT $1, $2, 'new', 'm', 8, 'incremental', 'completed', $3 FROM generate_series(1, $4)`,
		account, repo, runspec.IndexFormat, steps); err != nil {
		t.Fatal(err)
	}
	// Both the history's steps and the generation's are in the index that
	// the check reads.
	var historical, own int
	if err := s.owner.QueryRow(ctx, `SELECT count(*) FILTER (WHERE commit_sha = 'old'), count(*) FILTER (WHERE stepped_run_id = $2)
		FROM index_runs WHERE repository_id = $1 AND status = 'completed' AND stepped_run_id IS NOT NULL`, repo, gen).
		Scan(&historical, &own); err != nil {
		t.Fatal(err)
	}
	if historical != history/2 || own != steps {
		t.Fatalf("%d of the history's steps and %d of the generation's advanced a generation, want %d and %d",
			historical, own, history/2, steps)
	}
	if _, err := s.owner.Exec(ctx, `ANALYZE index_runs`); err != nil {
		t.Fatal(err)
	}
	mixed := `SELECT ` + steppedInAnotherFormat + ` FROM index_runs g WHERE g.id = $1`
	var plan []byte
	var got bool
	if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `EXPLAIN (ANALYZE, FORMAT JSON) `+mixed, gen).Scan(&plan); err != nil {
			return err
		}
		return tx.QueryRow(ctx, mixed, gen).Scan(&got)
	}); err != nil {
		t.Fatal(err)
	}
	if got {
		t.Fatal("a generation that only steps of its own format advanced is mixed")
	}
	var root []struct{ Plan planNode }
	if err := json.Unmarshal(plan, &root); err != nil || len(root) != 1 {
		t.Fatalf("plan %s: %v", plan, err)
	}
	// A step at each end of the generation's formats, the lowest and the
	// highest.
	var read float64
	for _, alias := range []string{"st", "st_1"} {
		read += root[0].Plan.rowsRead(alias)
	}
	if read > 2 {
		t.Errorf("the check reads %v runs, want one at each end of the generation's formats, "+
			"and none of the %d of the repository's history or the rest of the generation's %d steps:\n%s",
			read, history, steps, plan)
	}
	for _, format := range []int{runspec.IndexFormat - 1, runspec.IndexFormat + 1} {
		var step string
		if err := s.owner.QueryRow(ctx, `INSERT INTO index_runs (account_id, repository_id, commit_sha, embed_model, embed_dims, mode, status,
				index_format) VALUES ($1, $2, 'other', 'm', 8, 'incremental', 'completed', $3) RETURNING id`,
			account, repo, format).Scan(&step); err != nil {
			t.Fatal(err)
		}
		checkMixed(t, s, "mixed-plan", repo, true)
		if _, err := s.owner.Exec(ctx, `DELETE FROM index_runs WHERE id = $1`, step); err != nil {
			t.Fatal(err)
		}
	}
}

// TestOnboardCandidates makes sure that the onboarding feeder is offered the
// correct repositories in the correct order: accounts take turns, and an
// account's repositories whose pull requests moved last go first. A
// repository whose index another embedder or index format built, or a step
// of another index format advanced, is offered again.
func TestOnboardCandidates(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, onboardAccounts)); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}
	listRepos(t, s, "alpha", "alpha/one", "alpha/two")
	listRepos(t, s, "east", "east/busy", "east/quiet", "east/later", "east/rebuilt", "east/skipped", "east/failed", "east/queued", "east/indexed", "east/stale", "east/reformat", "east/mixed", "east/stepped", "east/off")
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
	if rows.Err() != nil || len(ids) != 15 {
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
	for name, age := range map[string]string{"east/quiet": "3 hours", "west/old-failure": "2 hours", "east/later": "1 hour", "east/rebuilt": "30 minutes",
		"east/reformat": "20 minutes", "east/mixed": "10 minutes", "east/stepped": "5 minutes"} {
		exec(`UPDATE repositories SET created_at = now() - $2::interval WHERE id = $1`, ids[name], age)
	}
	for name, age := range map[string]string{"east/busy": "1 minute", "west/one": "1 day"} {
		exec(`INSERT INTO pull_requests (account_id, repository_id, number, head_sha, updated_at)
			SELECT account_id, id, 1, 'abc', now() - $2::interval FROM repositories WHERE id = $1`, ids[name], age)
	}
	// east/stale's index was built with another document prompt.
	exec(`WITH g AS (
			INSERT INTO index_runs (account_id, repository_id, commit_sha, embed_model, embed_dims, document_prompt, mode, status, index_format)
			SELECT r.account_id, r.id, 'abc', 'm', 8, p.prompt, 'full', 'completed', $3
			FROM repositories r JOIN (VALUES ($1::uuid, ''), ($2::uuid, 'passage: ')) p (id, prompt) ON p.id = r.id
			RETURNING id, repository_id)
		UPDATE repositories r SET active_index_run_id = g.id FROM g WHERE r.id = g.repository_id`, ids["east/indexed"], ids["east/stale"], runspec.IndexFormat)
	// east/reformat's index was built by a build of another index format.
	exec(`WITH g AS (
			INSERT INTO index_runs (account_id, repository_id, commit_sha, embed_model, embed_dims, mode, status, index_format)
			SELECT account_id, id, 'abc', 'm', 8, 'full', 'completed', $2 FROM repositories WHERE id = $1
			RETURNING id, repository_id)
		UPDATE repositories r SET active_index_run_id = g.id FROM g WHERE r.id = g.repository_id`, ids["east/reformat"], runspec.IndexFormat+1)
	// east/mixed's index, of this format, was since stepped by a build of
	// another format. east/stepped's was stepped by one of this format, and a
	// step of another format completed before it was published.
	step := func(name string, format int) {
		t.Helper()
		exec(`INSERT INTO index_runs (account_id, repository_id, commit_sha, embed_model, embed_dims, mode, status, index_format)
			SELECT account_id, id, 'def', 'm', 8, 'incremental', 'completed', $2 FROM repositories WHERE id = $1`, ids[name], format)
	}
	step("east/stepped", runspec.IndexFormat-1)
	exec(`WITH g AS (
			INSERT INTO index_runs (account_id, repository_id, commit_sha, embed_model, embed_dims, mode, status, index_format)
			SELECT account_id, id, 'abc', 'm', 8, 'full', 'completed', $2 FROM repositories WHERE id = ANY($1)
			RETURNING id, repository_id)
		UPDATE repositories r SET active_index_run_id = g.id FROM g WHERE r.id = g.repository_id`,
		[]string{ids["east/mixed"], ids["east/stepped"]}, runspec.IndexFormat)
	step("east/mixed", runspec.IndexFormat-1)
	step("east/stepped", runspec.IndexFormat)
	checkMixed(t, s, "east", ids["east/mixed"], true)
	checkMixed(t, s, "east", ids["east/stepped"], false)

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
	want := []string{"east/busy", "west/one", "east/quiet", "west/old-failure", "east/later", "east/rebuilt", "east/reformat", "east/mixed", "east/stale"}
	if !slices.Equal(got, want) {
		t.Fatalf("candidates = %v, want %v", got, want)
	}
	if limited, err := s.OnboardCandidates(ctx, 1, time.Hour, all); err != nil || len(limited) != 1 {
		t.Fatalf("OnboardCandidates(1) = %v, %v", limited, err)
	}
	// The dashboard's backlog of one account is the feeder's order, less
	// the other accounts' turns.
	checkBacklog(t, s, "east", names, []string{"east/busy", "east/quiet", "east/later", "east/rebuilt", "east/reformat", "east/mixed", "east/stale"})
	// A repository that its configuration leaves off is passed over, and takes
	// none of the limit: here, every one of east's.
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
