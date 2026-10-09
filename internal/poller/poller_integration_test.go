//go:build integration

package poller

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/configfile/configfiletest"
	"github.com/home-operations/kritika/internal/forge"
	"github.com/home-operations/kritika/internal/ingest"
	"github.com/home-operations/kritika/internal/store"
	"github.com/home-operations/kritika/internal/store/storetest"
	"github.com/home-operations/kritika/internal/webhook"
)

const configYAML = `
apps:
  bot-ross:
    accounts: [onedr0p]
    clientId: Iv1.x
    privateKey: { env: TEST_PEM }
    webhookSecret: { env: TEST_SECRET }
repositories:
  onedr0p/home-ops: {}
`

// baseForge is what every fake forge here answers a pull request the
// poller asks for by number with: the forge does not know it.
type baseForge struct{ forge.Client }

func (baseForge) PullRequest(context.Context, string, string, int) (forge.OpenPullRequest, error) {
	return forge.OpenPullRequest{}, fs.ErrNotExist
}

// The reactions of reviews other suites left on the shared database are
// read with the poll; a fake with no comments answers them.
func (baseForge) ListInline(context.Context, string, string, int) ([]forge.Comment, error) {
	return nil, nil
}

func (baseForge) ListConversation(context.Context, string, string, int) ([]forge.Comment, error) {
	return nil, nil
}

// listForge answers only the listing call; the poller needs nothing else.
type listForge struct {
	baseForge

	mu     sync.Mutex
	prs    []forge.OpenPullRequest
	sinces []time.Time
	// byNumber are the pull requests the forge returns by number, open or
	// closed, and asked the numbers it was asked for.
	byNumber map[int]forge.OpenPullRequest
	asked    []int
}

func (f *listForge) PullRequest(_ context.Context, _, _ string, number int) (forge.OpenPullRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, number)
	if pr, ok := f.byNumber[number]; ok {
		return pr, nil
	}
	return forge.OpenPullRequest{}, fs.ErrNotExist
}

func (f *listForge) ListOpenPullRequests(_ context.Context, _, _ string, since time.Time) ([]forge.OpenPullRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sinces = append(f.sinces, since)
	return f.prs, nil
}

type forges struct{ f forge.Client }

func (f *forges) For(context.Context, *configfile.Connection, string) (forge.Client, error) {
	return f.f, nil
}

func TestPollerEnqueuesOnceAndAdvancesState(t *testing.T) {
	ctx := t.Context()
	logger := slog.New(slog.DiscardHandler)
	st := storetest.Open(t)
	t.Setenv("TEST_PEM", "pem")
	t.Setenv("TEST_SECRET", "s")
	file := configfiletest.Load(t, configYAML)
	if err := st.ApplyConfig(ctx, file); err != nil {
		t.Fatal(err)
	}
	queue, err := river.NewClient(riverpgxv5.New(st.App()), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	in, _ := file.Connection("bot-ross")
	account, _ := file.Account(configfile.ForgeGitHub, "onedr0p")
	var installed time.Time
	if err := st.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT created_at FROM accounts WHERE id = $1`, account.ID()).Scan(&installed)
	}); err != nil {
		t.Fatal(err)
	}
	lf := &listForge{prs: []forge.OpenPullRequest{{
		Number: 7, Title: "poll me", Author: "onedr0p", State: "open", HeadRef: "f", HeadSHA: "abc123", BaseRef: "main",
		UpdatedAt: time.Now(), DefaultBranch: "main",
	}, {
		// Last touched before kritika knew the account.
		Number: 8, Title: "leave me", Author: "onedr0p", State: "open", HeadRef: "g", HeadSHA: "old888", BaseRef: "main",
		UpdatedAt: installed.Add(-time.Hour), DefaultBranch: "main",
	}}}
	p := &Poller{
		Store: st, Current: configfile.NewCurrent(file), Forges: &forges{f: lf}, Dispatcher: ingest.NewService(st, queue),
		Logger: logger,
	}

	// Start from no poll state, no pull requests 7 or 8 and home-ops as
	// the one enabled repository, whatever earlier suites left behind:
	// the repositories they listed are the forge's again, and enabled.
	err = st.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM poll_state WHERE account_id = $1`, account.ID()); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE repositories SET enabled = false WHERE name <> 'onedr0p/home-ops'`); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM pull_requests WHERE number IN (7, 8)`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.App().Exec(ctx, `DELETE FROM river_job WHERE kind = 'review' AND args->>'number' IN ('7', '8')`); err != nil {
		t.Fatal(err)
	}
	before := time.Now()
	n, err := p.Poll(ctx, file, account, in)
	if err != nil || n != 2 {
		t.Fatalf("first poll: n=%d err=%v", n, err)
	}
	countJobs := func(number int) (jobs int, trigger string) {
		t.Helper()
		// Only this test's pull requests: the ingest suite leaves jobs of
		// its own on the shared database.
		if err := st.App().QueryRow(ctx, `SELECT count(*), coalesce(max(args->>'trigger'), '') FROM river_job
			WHERE kind = 'review' AND (args->>'number')::int = $1`, number).Scan(&jobs, &trigger); err != nil {
			t.Fatal(err)
		}
		return jobs, trigger
	}
	if jobs, trigger := countJobs(7); jobs != 1 || trigger != "poll" {
		t.Fatalf("after first poll: jobs=%d trigger=%q", jobs, trigger)
	}
	checkBaseline(ctx, t, st, account.ID(), 8, "old888")
	var head string
	var polledAt time.Time
	err = st.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT head_sha FROM pull_requests WHERE number = 7`).Scan(&head); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT last_polled_at FROM poll_state WHERE account_id = $1`, account.ID()).Scan(&polledAt)
	})
	if err != nil || head != "abc123" || polledAt.Before(before) {
		t.Fatalf("rows: err=%v head=%s polled=%v", err, head, polledAt)
	}

	// The pull request moved again since the first poll, with the same
	// head: ingest sees a duplicate, no second job.
	lf.mu.Lock()
	lf.prs = lf.prs[:1]
	lf.prs[0].UpdatedAt = time.Now()
	lf.mu.Unlock()
	if n, err := p.Poll(ctx, file, account, in); err != nil || n != 1 {
		t.Fatalf("second poll: n=%d err=%v", n, err)
	}
	if jobs, _ := countJobs(7); jobs != 1 {
		t.Fatalf("after second poll: jobs=%d, want the head deduplicated", jobs)
	}
	lf.mu.Lock()
	sinces := lf.sinces
	lf.mu.Unlock()
	// The forge is asked for every open pull request each time, and the
	// poller keeps the ones updated since the last poll itself.
	if len(sinces) != 2 || !sinces[0].IsZero() || !sinces[1].IsZero() {
		t.Fatalf("since values = %v, want every listing asked for all open pull requests", sinces)
	}

	// A closed pull request from the forge is recorded by ingest, not
	// reviewed, and not an error.
	lf.mu.Lock()
	lf.prs[0].State = "closed"
	lf.prs[0].HeadSHA = "def456"
	lf.prs[0].UpdatedAt = time.Now()
	lf.mu.Unlock()
	if n, err := p.Poll(ctx, file, account, in); err != nil || n != 1 {
		t.Fatalf("third poll: n=%d err=%v", n, err)
	}
	if jobs, _ := countJobs(7); jobs != 1 {
		t.Fatalf("after third poll: jobs=%d, want the closed pull request's new head left unreviewed", jobs)
	}
	checkAccountOff(ctx, t, p, file, account, in, lf)
}

// checkAccountOff asserts a poll lists nothing for an account that leaves
// its repositories off, though the App still reaches them.
func checkAccountOff(
	ctx context.Context, t *testing.T, p *Poller, file *configfile.File, account *configfile.Account, in *configfile.Connection, lf *listForge,
) {
	t.Helper()
	off := *account
	off.Enabled = new(false)
	lf.mu.Lock()
	listed := len(lf.sinces)
	lf.mu.Unlock()
	if n, err := p.Poll(ctx, file, &off, in); err != nil || n != 0 {
		t.Fatalf("poll with the account off: n=%d err=%v", n, err)
	}
	lf.mu.Lock()
	defer lf.mu.Unlock()
	if len(lf.sinces) != listed {
		t.Fatalf("the forge was listed %d more times with the account off", len(lf.sinces)-listed)
	}
}

// checkBaseline asserts the pull request numbered number was recorded at
// head with no review job: the first poll's baseline.
func checkBaseline(ctx context.Context, t *testing.T, st *store.Store, accountID string, number int, head string) {
	t.Helper()
	var jobs int
	var got string
	if err := st.App().QueryRow(ctx, `SELECT count(*) FROM river_job WHERE kind = 'review' AND (args->>'number')::int = $1`, number).
		Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	err := st.WithAccount(ctx, accountID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT head_sha FROM pull_requests WHERE number = $1`, number).Scan(&got)
	})
	if err != nil || got != head || jobs != 0 {
		t.Fatalf("baseline pull request %d: head=%q jobs=%d err=%v; want %s recorded with no review job", number, got, jobs, err, head)
	}
}

// tipForge answers the listing with nothing and the branch tip with tip,
// counting the tip calls.
type tipForge struct {
	baseForge
	tip   string
	calls int
}

func (f *tipForge) ListOpenPullRequests(context.Context, string, string, time.Time) ([]forge.OpenPullRequest, error) {
	return nil, nil
}

func (f *tipForge) CommitSubject(context.Context, string, string, string) (string, error) {
	return "feat(x): the head commit", nil
}

func (f *tipForge) BranchTip(context.Context, string, string, string) (string, string, error) {
	f.calls++
	return f.tip, "main", nil
}

// TestPollerIndexesAMovedDefaultBranch: a connection no webhook reaches
// has its indexed repositories' default branches checked, and an index job
// queued when one moved; one that webhooks reach does not.
func TestPollerIndexesAMovedDefaultBranch(t *testing.T) {
	ctx := t.Context()
	logger := slog.New(slog.DiscardHandler)
	st := storetest.Open(t)
	t.Setenv("TEST_PEM", "pem")
	t.Setenv("TEST_SECRET", "s")
	file := configfiletest.Load(t, configYAML)
	if err := st.ApplyConfig(ctx, file); err != nil {
		t.Fatal(err)
	}
	queue, err := river.NewClient(riverpgxv5.New(st.App()), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	in, _ := file.Connection("bot-ross")
	account, _ := file.Account(configfile.ForgeGitHub, "onedr0p")
	repoID := configfile.RepositoryID(account.ID(), "onedr0p/home-ops")
	exec := func(sql string, args ...any) {
		t.Helper()
		if err := st.WithAccount(context.Background(), account.ID(), func(tx pgx.Tx) error {
			_, err := tx.Exec(context.Background(), sql, args...)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	var runID string
	if err := st.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO index_runs (account_id, repository_id, commit_sha, embed_model, embed_dims, mode, status)
			VALUES ($1, $2, 'indexed', 'fake-embed', 8, 'full', 'completed') RETURNING id`, account.ID(), repoID).Scan(&runID)
	}); err != nil {
		t.Fatal(err)
	}
	exec(`UPDATE repositories SET active_index_run_id = $1 WHERE id = $2`, runID, repoID)
	// The worker's tests share the database and index this repository from
	// nothing.
	t.Cleanup(func() {
		exec(`UPDATE repositories SET active_index_run_id = NULL WHERE id = $1`, repoID)
		exec(`DELETE FROM index_runs WHERE id = $1`, runID)
		_, _ = st.App().Exec(context.Background(), `DELETE FROM river_job WHERE kind = 'index'`)
	})
	indexJobs := func() []string {
		t.Helper()
		rows, err := st.App().Query(ctx, `SELECT args->>'commit_sha' FROM river_job WHERE kind = 'index' AND args->>'repository_id' = $1`, repoID)
		if err != nil {
			t.Fatal(err)
		}
		commits, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		return commits
	}
	tf := &tipForge{tip: "moved"}
	p := &Poller{Store: st, Current: configfile.NewCurrent(file), Forges: &forges{f: tf}, Dispatcher: ingest.NewService(st, queue), Logger: logger}
	poll := func() {
		t.Helper()
		if _, err := p.Poll(ctx, file, account, in); err != nil {
			t.Fatal(err)
		}
	}

	// Earlier suites leave index jobs of this repository behind.
	if _, err := st.App().Exec(ctx, `DELETE FROM river_job WHERE kind = 'index'`); err != nil {
		t.Fatal(err)
	}
	exec(`UPDATE connections SET last_webhook_at = NULL WHERE id = $1`, in.ID())
	poll()
	if got := indexJobs(); len(got) != 1 || got[0] != "moved" {
		t.Fatalf("index jobs = %v, want one at the moved tip", got)
	}

	if _, err := st.App().Exec(ctx, `DELETE FROM river_job WHERE kind = 'index'`); err != nil {
		t.Fatal(err)
	}
	exec(`UPDATE connections SET last_webhook_at = now() WHERE id = $1`, in.ID())
	calls := tf.calls
	poll()
	if tf.calls != calls || len(indexJobs()) != 0 {
		t.Fatalf("with webhooks arriving: %d tip checks and index jobs %v, want none", tf.calls-calls, indexJobs())
	}

	exec(`UPDATE connections SET last_webhook_at = NULL WHERE id = $1`, in.ID())
	tf.tip = "indexed"
	poll()
	if got := indexJobs(); len(got) != 0 {
		t.Fatalf("index jobs = %v, want none for a tip the index covers", got)
	}
}

// TestSyncRepositories: the repositories a running connection's App reaches
// are registered on the accounts it serves, with what the forge says of
// them, and a second sync adds none; an account it does not serve gets
// nothing. A forge-reported repository the listing no longer has is
// disabled, but not one the configuration lists, nor one a webhook named
// while the listing ran, and a listing that fails or comes back empty
// disables nothing.
func TestSyncRepositories(t *testing.T) {
	ctx := t.Context()
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	st := storetest.Open(t)
	t.Setenv("TEST_PEM", "pem")
	t.Setenv("TEST_SECRET", "s")
	file := configfiletest.Load(t, configYAML)
	if err := st.ApplyConfig(ctx, file); err != nil {
		t.Fatal(err)
	}
	account, _ := file.Account(configfile.ForgeGitHub, "onedr0p")
	// The other suites poll every enabled repository of the account.
	t.Cleanup(func() {
		_ = st.WithAccount(context.Background(), account.ID(), func(tx pgx.Tx) error {
			_, err := tx.Exec(context.Background(), `UPDATE repositories SET enabled = false WHERE name LIKE 'onedr0p/synced%'`)
			return err
		})
	})
	var asked []string
	p := &Poller{Store: st, Current: configfile.NewCurrent(file), Logger: logger,
		Reach: func(_ context.Context, in *configfile.Connection) (map[string][]store.ReachedRepository, error) {
			asked = append(asked, in.Name)
			return map[string][]store.ReachedRepository{
				"onedr0p": {
					{FullName: "onedr0p/synced", DefaultBranch: "main", Traits: &configfile.RepoTraits{}},
					{FullName: "onedr0p/synced-fork", DefaultBranch: "main", Traits: &configfile.RepoTraits{Fork: true}},
				},
				"stranger": {{FullName: "stranger/elsewhere", Traits: &configfile.RepoTraits{}}},
			}, nil
		},
	}
	rows := func() string {
		t.Helper()
		var out []string
		if err := st.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
			r, err := tx.Query(ctx, `SELECT name, enabled, default_branch, fork FROM repositories
				WHERE name LIKE 'onedr0p/synced%' OR name LIKE 'stranger/%' ORDER BY name COLLATE "C"`)
			if err != nil {
				return err
			}
			var name, branch string
			var enabled, fork bool
			_, err = pgx.ForEachRow(r, []any{&name, &enabled, &branch, &fork}, func() error {
				out = append(out, fmt.Sprintf("%s:%v:%s:%v", name, enabled, branch, fork))
				return nil
			})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return strings.Join(out, ",")
	}

	p.SyncRepositories(ctx)
	want := "onedr0p/synced:true:main:false,onedr0p/synced-fork:true:main:true"
	if got := rows(); got != want || len(asked) != 1 || asked[0] != "bot-ross" {
		t.Fatalf("after a sync: repositories %q, asked %v; want %q from bot-ross alone", got, asked, want)
	}
	if !strings.Contains(logs.String(), "repositories registered") || !strings.Contains(logs.String(), "added=2") {
		t.Fatalf("logs = %s", logs.String())
	}
	logs.Reset()
	p.SyncRepositories(ctx)
	if got := rows(); got != want || strings.Contains(logs.String(), "repositories registered") {
		t.Fatalf("a second sync: repositories %q, logs %s; want nothing new", got, logs.String())
	}

	homeOps := func() bool {
		t.Helper()
		var on bool
		if err := st.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT enabled FROM repositories WHERE name = 'onedr0p/home-ops'`).Scan(&on)
		}); err != nil {
			t.Fatal(err)
		}
		return on
	}
	listedBefore := homeOps()
	logs.Reset()
	// A webhook names synced-late in a transaction that began before the
	// listing and writes while it runs.
	began, write, named := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		named <- st.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
			close(began)
			<-write
			_, _, err := store.EnsureRepository(ctx, tx, account.ID(), store.ReachedRepository{FullName: "onedr0p/synced-late"})
			return err
		})
	}()
	<-began
	p.Reach = func(context.Context, *configfile.Connection) (map[string][]store.ReachedRepository, error) {
		close(write)
		if err := <-named; err != nil {
			return nil, err
		}
		return map[string][]store.ReachedRepository{
			"onedr0p": {{FullName: "onedr0p/synced", DefaultBranch: "main", Traits: &configfile.RepoTraits{}}},
		}, nil
	}
	p.SyncRepositories(ctx)
	want = "onedr0p/synced:true:main:false,onedr0p/synced-fork:false:main:true,onedr0p/synced-late:true::false"
	if got := rows(); got != want || !strings.Contains(logs.String(), "disabled=1") || homeOps() != listedBefore {
		t.Fatalf("a sync without synced-fork: repositories %q, home-ops enabled %v, logs %s; want %q, home-ops as it was",
			got, homeOps(), logs.String(), want)
	}

	p.Reach = func(context.Context, *configfile.Connection) (map[string][]store.ReachedRepository, error) {
		return map[string][]store.ReachedRepository{"onedr0p": {}}, nil
	}
	p.SyncRepositories(ctx)
	if got := rows(); got != want {
		t.Fatalf("an empty listing: repositories %q, want %q", got, want)
	}
	p.Reach = func(context.Context, *configfile.Connection) (map[string][]store.ReachedRepository, error) {
		return nil, errors.New("github: list App installations: 502")
	}
	p.SyncRepositories(ctx)
	if !strings.Contains(logs.String(), "repositories not synced") || !strings.Contains(logs.String(), "502") {
		t.Fatalf("a failed listing is not logged: %s", logs.String())
	}
	if got := rows(); got != want {
		t.Fatalf("a failed listing: repositories %q, want %q", got, want)
	}
}

// reactionForge lists no open pull requests, and the inline comments of
// the pull requests it has them for.
type reactionForge struct {
	baseForge

	mu     sync.Mutex
	inline map[int][]forge.Comment
	listed []int
}

func (f *reactionForge) ListOpenPullRequests(context.Context, string, string, time.Time) ([]forge.OpenPullRequest, error) {
	return nil, nil
}

func (f *reactionForge) ListInline(_ context.Context, _, _ string, number int) ([]forge.Comment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listed = append(f.listed, number)
	return f.inline[number], nil
}

// TestPollerReadsReactions: a poll reads the reactions on kritika's inline
// comments into every finding that carries the comment's thread, for the
// pull requests reviewed lately only.
func TestPollerReadsReactions(t *testing.T) {
	ctx := t.Context()
	logger := slog.New(slog.DiscardHandler)
	st := storetest.Open(t)
	t.Setenv("TEST_PEM", "pem")
	t.Setenv("TEST_SECRET", "s")
	file := configfiletest.Load(t, configYAML)
	if err := st.ApplyConfig(ctx, file); err != nil {
		t.Fatal(err)
	}
	in, _ := file.Connection("bot-ross")
	account, _ := file.Account(configfile.ForgeGitHub, "onedr0p")

	var recent, stale string
	err := st.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE repositories SET enabled = true WHERE name = 'onedr0p/home-ops'`); err != nil {
			return err
		}
		pull := func(number int, reviewed time.Time, comments ...int64) string {
			var pr string
			if err := tx.QueryRow(ctx, `INSERT INTO pull_requests (account_id, repository_id, number, head_sha)
				SELECT $1, id, $2, 'h' FROM repositories WHERE name = 'onedr0p/home-ops' RETURNING id`, account.ID(), number).Scan(&pr); err != nil {
				t.Fatal(err)
			}
			// Two reviews, the second carrying the first's threads.
			for i := range 2 {
				var review string
				if err := tx.QueryRow(ctx, `INSERT INTO reviews (account_id, pull_request_id, head_sha, status, created_at)
					VALUES ($1, $2, $3, 'completed', $4) RETURNING id`, account.ID(), pr, fmt.Sprintf("h%d", i), reviewed).Scan(&review); err != nil {
					t.Fatal(err)
				}
				for _, id := range comments {
					if _, err := tx.Exec(ctx, `INSERT INTO findings (account_id, review_id, path, line, severity, title, explanation,
						posted_inline, forge_comment_id) VALUES ($1, $2, 'a.go', 1, 'p2', 'n', '', true, $3)`, account.ID(), review, id); err != nil {
						t.Fatal(err)
					}
				}
			}
			return pr
		}
		if _, err := tx.Exec(ctx, `DELETE FROM pull_requests WHERE number IN (70, 71)`); err != nil {
			return err
		}
		recent = pull(70, time.Now(), 9001, 9002)
		stale = pull(71, time.Now().Add(-30*24*time.Hour), 9101)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	rf := &reactionForge{inline: map[int][]forge.Comment{
		70: {{ID: 9001, ReactionsUp: 3, ReactionsDown: 1}, {ID: 9002}, {ID: 5, ReactionsUp: 9}},
		71: {{ID: 9101, ReactionsUp: 4}},
	}}
	p := &Poller{Store: st, Current: configfile.NewCurrent(file), Forges: &forges{f: rf}, Logger: logger}
	if _, err := p.Poll(ctx, file, account, in); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if !slices.Equal(rf.listed, []int{70}) {
		t.Fatalf("listed the inline comments of %v; want only the pull request reviewed lately", rf.listed)
	}
	reactions := func(pr string) map[int64][2]int {
		t.Helper()
		out := map[int64][2]int{}
		if err := st.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
			rows, err := tx.Query(ctx, `SELECT f.forge_comment_id, f.reactions_up, f.reactions_down FROM findings f
				JOIN reviews v ON v.id = f.review_id WHERE v.pull_request_id = $1`, pr)
			if err != nil {
				return err
			}
			var id int64
			var up, down int
			_, err = pgx.ForEachRow(rows, []any{&id, &up, &down}, func() error {
				if got, ok := out[id]; ok && got != [2]int{up, down} {
					return fmt.Errorf("comment %d reads %v on one review and %v on another", id, got, [2]int{up, down})
				}
				out[id] = [2]int{up, down}
				return nil
			})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return out
	}
	if got := reactions(recent); got[9001] != [2]int{3, 1} || got[9002] != [2]int{0, 0} {
		t.Fatalf("reactions = %v", got)
	}
	if got := reactions(stale); got[9101] != [2]int{0, 0} {
		t.Fatalf("a pull request reviewed a month ago was read: %v", got)
	}
}

// skipForge lists one pull request for every repository but fails the
// listing of the one named failing, and has no inline comments to read
// reactions from.
type skipForge struct {
	baseForge
	failing string
}

func (f *skipForge) ListInline(context.Context, string, string, int) ([]forge.Comment, error) {
	return nil, nil
}

func (f *skipForge) ListOpenPullRequests(_ context.Context, _, name string, _ time.Time) ([]forge.OpenPullRequest, error) {
	if name == f.failing {
		return nil, errors.New("secondary rate limit")
	}
	return []forge.OpenPullRequest{{
		Number: 9, Title: "poll me", Author: "onedr0p", State: "open", HeadRef: "h", HeadSHA: "abc999", BaseRef: "main",
		UpdatedAt: time.Now(), DefaultBranch: "main",
	}}, nil
}

// TestPollerPollsPastAFailingRepository: a repository the forge will not
// list is skipped, the others are still polled, and the poll state is left
// for the next pass to cover the skipped one.
func TestPollerPollsPastAFailingRepository(t *testing.T) {
	ctx := t.Context()
	st := storetest.Open(t)
	t.Setenv("TEST_PEM", "pem")
	t.Setenv("TEST_SECRET", "s")
	file := configfiletest.Load(t, configYAML)
	if err := st.ApplyConfig(ctx, file); err != nil {
		t.Fatal(err)
	}
	queue, err := river.NewClient(riverpgxv5.New(st.App()), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	in, _ := file.Connection("bot-ross")
	account, _ := file.Account(configfile.ForgeGitHub, "onedr0p")
	if _, _, err := st.RegisterRepositories(ctx, account.ID(), []store.ReachedRepository{
		{FullName: "onedr0p/flaky", DefaultBranch: "main", Traits: &configfile.RepoTraits{}},
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
	// A webhook reached the connection lately, so no default branch is
	// checked: this poll is about the listing alone.
	if err := st.RecordWebhookDelivery(ctx, in.ID()); err != nil {
		t.Fatal(err)
	}
	err = st.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM poll_state WHERE account_id = $1`, account.ID()); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE repositories SET enabled = name IN ('onedr0p/home-ops', 'onedr0p/flaky')`); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM pull_requests WHERE number = 9`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = st.WithAccount(context.Background(), account.ID(), func(tx pgx.Tx) error {
			_, err := tx.Exec(context.Background(), `UPDATE repositories SET enabled = false WHERE name = 'onedr0p/flaky'`)
			return err
		})
	})
	p := &Poller{
		Store: st, Current: configfile.NewCurrent(file), Forges: &forges{f: &skipForge{failing: "flaky"}},
		Dispatcher: ingest.NewService(st, queue), Logger: slog.New(slog.DiscardHandler),
	}
	n, err := p.Poll(ctx, file, account, in)
	if err == nil || !strings.Contains(err.Error(), "1 of 2 repositories not polled") || !strings.Contains(err.Error(), "onedr0p/flaky") {
		t.Fatalf("Poll() error = %v, want the skipped repository named", err)
	}
	if n != 1 {
		t.Fatalf("Poll() handled %d, want home-ops's pull request polled past the failure", n)
	}
	var polled int
	if err := st.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM poll_state WHERE account_id = $1`, account.ID()).Scan(&polled)
	}); err != nil {
		t.Fatal(err)
	}
	if polled != 0 {
		t.Fatal("poll state recorded although a repository was skipped")
	}
}

// stallForge lists nothing until its context ends, and counts the polls
// that reached it.
type stallForge struct {
	baseForge
	polls atomic.Int32
}

func (f *stallForge) ListOpenPullRequests(ctx context.Context, _, _ string, _ time.Time) ([]forge.OpenPullRequest, error) {
	f.polls.Add(1)
	<-ctx.Done()
	return nil, ctx.Err()
}

// TestPollerRunCutsAPollAtItsInterval: a poll that outlasts the interval
// is cut, the next one starts on time, and the poll state is left for it.
func TestPollerRunCutsAPollAtItsInterval(t *testing.T) {
	ctx := t.Context()
	st := storetest.Open(t)
	t.Setenv("TEST_PEM", "pem")
	t.Setenv("TEST_SECRET", "s")
	t.Setenv("KRITIKA_POLL_INTERVAL", "100ms")
	file := configfiletest.Load(t, configYAML)
	if err := st.ApplyConfig(ctx, file); err != nil {
		t.Fatal(err)
	}
	queue, err := river.NewClient(riverpgxv5.New(st.App()), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	in, _ := file.Connection("bot-ross")
	account, _ := file.Account(configfile.ForgeGitHub, "onedr0p")
	if err := st.RecordWebhookDelivery(ctx, in.ID()); err != nil {
		t.Fatal(err)
	}
	err = st.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM poll_state WHERE account_id = $1`, account.ID())
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	sf := &stallForge{}
	p := &Poller{
		Store: st, Current: configfile.NewCurrent(file), Forges: &forges{f: sf},
		Dispatcher: ingest.NewService(st, queue), Logger: slog.New(slog.NewTextHandler(&log, nil)),
	}
	rctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.Run(rctx)
	}()
	<-done
	if n := sf.polls.Load(); n < 2 {
		t.Fatalf("the forge was polled %d times in 2s at a 100ms interval; want the stalled poll cut and another started", n)
	}
	if !strings.Contains(log.String(), "poll cut at its interval") {
		t.Fatalf("log = %q, want the cut poll reported", log.String())
	}
	var polled int
	if err := st.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM poll_state WHERE account_id = $1`, account.ID()).Scan(&polled)
	}); err != nil {
		t.Fatal(err)
	}
	if polled != 0 {
		t.Fatal("poll state recorded although the poll was cut")
	}
}

// TestPollerClosesAPullRequestWhoseEventWasMissed: a pull request kritika
// holds open that the forge no longer lists as open is asked for by number
// and recorded closed, merged or not; one the forge still has open, and one
// it does not know, are left as they are.
func TestPollerClosesAPullRequestWhoseEventWasMissed(t *testing.T) {
	ctx := t.Context()
	st := storetest.Open(t)
	t.Setenv("TEST_PEM", "pem")
	t.Setenv("TEST_SECRET", "s")
	file := configfiletest.Load(t, configYAML)
	if err := st.ApplyConfig(ctx, file); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}
	queue, err := river.NewClient(riverpgxv5.New(st.App()), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	account, _ := file.Account(configfile.ForgeGitHub, "onedr0p")
	in := file.ConnectionFor(account)
	svc := ingest.NewService(st, queue)
	open := func(number int) forge.OpenPullRequest {
		return forge.OpenPullRequest{
			Number: number, Title: "t", Author: "onedr0p", State: "open", HeadRef: "f", HeadSHA: fmt.Sprintf("c%d", number), BaseRef: "main",
			DefaultBranch: "main",
		}
	}
	numbers := []int{931, 932, 933, 934}
	if err := st.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM pull_requests WHERE number = ANY($1)`, numbers)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, n := range numbers {
		pr := open(n)
		if _, err := svc.Dispatch(ctx, ingest.Request{File: file, Account: account, Event: webhook.Event{
			Kind: webhook.KindPullRequest, Action: "opened", Account: "onedr0p",
			Repository: &webhook.Repository{FullName: "onedr0p/home-ops", DefaultBranch: "main"}, PullRequest: &pr.PullRequest,
		}}); err != nil {
			t.Fatal(err)
		}
	}
	closedAt := time.Date(2026, 10, 2, 8, 59, 24, 0, time.UTC)
	merged, closed := open(931), open(932)
	merged.State, merged.Merged, merged.ClosedAt = "closed", true, &closedAt
	closed.State, closed.ClosedAt = "closed", &closedAt
	// 933 is still open on the forge though the listing lacks it, as one
	// opened since the listing would be; 934 the forge does not know.
	lf := &listForge{byNumber: map[int]forge.OpenPullRequest{931: merged, 932: closed, 933: open(933)}}
	p := &Poller{
		Store: st, Current: configfile.NewCurrent(file), Forges: &forges{f: lf}, Dispatcher: svc, Logger: slog.New(slog.DiscardHandler),
	}
	if _, err := p.Poll(ctx, file, account, in); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	// The one the forge did not know is not asked for again, so it cannot
	// use up a poll's checks for good; the others are asked each poll.
	lf.mu.Lock()
	lf.asked = nil
	lf.mu.Unlock()
	if _, err := p.Poll(ctx, file, account, in); err != nil {
		t.Fatalf("second Poll: %v", err)
	}
	lf.mu.Lock()
	asked := lf.asked
	lf.mu.Unlock()
	if !slices.Equal(asked, []int{933}) {
		t.Fatalf("second poll asked for %v, want only the pull request still open on the forge", asked)
	}
	type row struct {
		state    string
		merged   bool
		closedAt *time.Time
	}
	want := map[int]row{931: {"closed", true, &closedAt}, 932: {"closed", false, &closedAt}, 933: {"open", false, nil}, 934: {"open", false, nil}}
	for _, n := range numbers {
		var got row
		if err := st.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT state, merged, closed_at FROM pull_requests WHERE number = $1`, n).
				Scan(&got.state, &got.merged, &got.closedAt)
		}); err != nil {
			t.Fatal(err)
		}
		w := want[n]
		if got.state != w.state || got.merged != w.merged || (got.closedAt == nil) != (w.closedAt == nil) ||
			(got.closedAt != nil && !got.closedAt.Equal(*w.closedAt)) {
			t.Errorf("#%d = %+v, want %+v", n, got, w)
		}
	}
}
