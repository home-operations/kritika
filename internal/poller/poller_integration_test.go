//go:build integration

package poller

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gh "github.com/google/go-github/v93/github"
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
`

// baseForge is what every fake forge here answers a pull request the
// poller asks for by number with: the forge does not know it.
type baseForge struct{ forge.Client }

func (baseForge) PullRequest(context.Context, string, string, int) (forge.OpenPullRequest, error) {
	return forge.OpenPullRequest{}, forge.ErrNotFound
}

// The reactions of reviews that other suites left on the shared database
// are read with the poll. A fake with no comments answers them.
func (baseForge) ListInline(context.Context, string, string, int) ([]forge.Comment, error) {
	return nil, nil
}

func (baseForge) ListConversation(context.Context, string, string, int) ([]forge.Comment, error) {
	return nil, nil
}

// listForge answers only the listing call. The poller needs nothing else.
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
	return forge.OpenPullRequest{}, forge.ErrNotFound
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
	storetest.Repositories(t, st, file, "onedr0p/home-ops")
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
	// the one enabled repository, whatever earlier suites left behind.
	// home-ops stands for a repository known before dashboard enablement,
	// with no choice recorded. So turning it on does not catch up on the
	// pull requests from before the account was known.
	err = st.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM poll_state WHERE account_id = $1`, account.ID()); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE repositories SET enabled = false WHERE name <> 'onedr0p/home-ops'`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE repositories SET turned_on = NULL, turned_at = NULL WHERE name = 'onedr0p/home-ops'`); err != nil {
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

// checkAccountOff asserts a poll lists nothing for an account whose
// repositories are turned off, though the App still reaches them.
func checkAccountOff(
	ctx context.Context, t *testing.T, p *Poller, file *configfile.File, account *configfile.Account, in *configfile.Connection, lf *listForge,
) {
	t.Helper()
	turn := func(on bool) {
		t.Helper()
		if err := p.Store.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
			return store.TurnOn(ctx, tx, configfile.RepositoryID(account.ID(), "onedr0p/home-ops"), on)
		}); err != nil {
			t.Fatal(err)
		}
	}
	turn(false)
	defer turn(true)
	lf.mu.Lock()
	listed := len(lf.sinces)
	lf.mu.Unlock()
	if n, err := p.Poll(ctx, file, account, in); err != nil || n != 0 {
		t.Fatalf("poll with the repositories off: n=%d err=%v", n, err)
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

// tipForge answers the listing with nothing and the branch tip with tip, or
// with err when set, counting the tip calls.
type tipForge struct {
	baseForge
	tip   string
	err   error
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
	return f.tip, "main", f.err
}

// recordingDispatcher records the events a poll dispatches.
type recordingDispatcher struct {
	ingest.Dispatcher
	mu     sync.Mutex
	events []webhook.Event
}

func (d *recordingDispatcher) Dispatch(ctx context.Context, req ingest.Request) (ingest.Outcome, error) {
	d.mu.Lock()
	d.events = append(d.events, req.Event)
	d.mu.Unlock()
	return d.Dispatcher.Dispatch(ctx, req)
}

// tipPoll polls onedr0p's repositories through tf, with each of repos
// indexed at "indexed", no index job of any of them and no webhook delivered.
type tipPoll struct {
	t          *testing.T
	st         *store.Store
	file       *configfile.File
	account    *configfile.Account
	in         *configfile.Connection
	repoIDs    []string
	tf         *tipForge
	dispatched *recordingDispatcher
	p          *Poller
	logs       bytes.Buffer
}

func newTipPoll(t *testing.T, repos ...string) *tipPoll {
	t.Helper()
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
	s := &tipPoll{t: t, st: st, file: file, tf: &tipForge{tip: "moved"}}
	s.in, _ = file.Connection("bot-ross")
	s.account, _ = file.Account(configfile.ForgeGitHub, "onedr0p")
	storetest.Repositories(t, st, file, repos...)
	for _, name := range repos {
		repoID := configfile.RepositoryID(s.account.ID(), name)
		var runID string
		if err := st.WithAccount(ctx, s.account.ID(), func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `INSERT INTO index_runs (account_id, repository_id, commit_sha, embed_model, embed_dims, mode, status)
				VALUES ($1, $2, 'indexed', 'fake-embed', 8, 'full', 'completed') RETURNING id`, s.account.ID(), repoID).Scan(&runID)
		}); err != nil {
			t.Fatal(err)
		}
		s.exec(`UPDATE repositories SET active_index_run_id = $1 WHERE id = $2`, runID, repoID)
		// The worker's tests share the database and index these repositories
		// from nothing.
		t.Cleanup(func() {
			s.exec(`UPDATE repositories SET active_index_run_id = NULL WHERE id = $1`, repoID)
			s.exec(`DELETE FROM index_runs WHERE id = $1`, runID)
		})
		s.repoIDs = append(s.repoIDs, repoID)
	}
	t.Cleanup(s.clearIndexJobs)
	// Earlier suites leave index jobs of these repositories behind.
	s.clearIndexJobs()
	s.webhooks(false)
	s.dispatched = &recordingDispatcher{Dispatcher: ingest.NewService(st, queue)}
	s.p = &Poller{
		Store: st, Current: configfile.NewCurrent(file), Forges: &forges{f: s.tf}, Dispatcher: s.dispatched,
		Logger: slog.New(slog.NewTextHandler(&s.logs, nil)),
	}
	return s
}

func (s *tipPoll) exec(sql string, args ...any) {
	s.t.Helper()
	if err := s.st.WithAccount(context.Background(), s.account.ID(), func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), sql, args...)
		return err
	}); err != nil {
		s.t.Fatal(err)
	}
}

func (s *tipPoll) clearIndexJobs() {
	s.t.Helper()
	if _, err := s.st.App().Exec(context.Background(), `DELETE FROM river_job WHERE kind = 'index'`); err != nil {
		s.t.Fatal(err)
	}
}

// webhooks marks the connection's webhook as delivered just now, or as
// never delivered.
func (s *tipPoll) webhooks(arriving bool) {
	s.t.Helper()
	if arriving {
		s.exec(`UPDATE connections SET last_webhook_at = now() WHERE id = $1`, s.in.ID())
		return
	}
	s.exec(`UPDATE connections SET last_webhook_at = NULL WHERE id = $1`, s.in.ID())
}

func (s *tipPoll) poll() {
	s.t.Helper()
	if _, err := s.p.Poll(s.t.Context(), s.file, s.account, s.in); err != nil {
		s.t.Fatal(err)
	}
	if err := s.p.pollTips(s.t.Context(), s.file, s.account, s.in); err != nil {
		s.t.Fatal(err)
	}
}

// indexJobs are the commits of the index jobs of the first repository.
func (s *tipPoll) indexJobs() []string {
	s.t.Helper()
	rows, err := s.st.App().Query(s.t.Context(), `SELECT coalesce(args->>'commit_sha', '') FROM river_job
		WHERE kind = 'index' AND args->>'repository_id' = $1 ORDER BY id`, s.repoIDs[0])
	if err != nil {
		s.t.Fatal(err)
	}
	commits, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		s.t.Fatal(err)
	}
	return commits
}

// TestPollerIndexesAMovedDefaultBranch: a poll looks at the default branches
// of the indexed repositories, since one push webhook missed or one index job
// failed leaves the index behind otherwise. It queues an index job when a
// branch moved. It does not ask the forge about a repository whose index job
// is already queued or running: that job indexes the tip it finds.
func TestPollerIndexesAMovedDefaultBranch(t *testing.T) {
	ctx := t.Context()
	s := newTipPoll(t, "onedr0p/home-ops")
	s.poll()
	if got := s.indexJobs(); len(got) != 1 || got[0] != "moved" {
		t.Fatalf("index jobs = %v, want one at the moved tip", got)
	}
	// The push carries the traits the poll read, which ingest writes back
	// only for a webhook's report, not a poll's.
	var pushes []webhook.Event
	for _, ev := range s.dispatched.events {
		if ev.Kind == webhook.KindPush && ev.Repository.FullName == "onedr0p/home-ops" {
			pushes = append(pushes, ev)
		}
	}
	if len(pushes) != 1 || pushes[0].Action != ingest.ActionPoll {
		t.Fatalf("pushes dispatched for onedr0p/home-ops = %+v, want one marked as the poll's", pushes)
	}

	// The job is still queued: the branch is not asked for again.
	calls := s.tf.calls
	s.tf.tip = "moved again"
	s.poll()
	if got := s.indexJobs(); s.tf.calls != calls || len(got) != 1 || got[0] != "moved" {
		t.Fatalf("with an index job queued: %d tip checks and index jobs %v, want none and the one queued", s.tf.calls-calls, got)
	}

	s.clearIndexJobs()
	s.tf.tip = "indexed"
	s.poll()
	if got := s.indexJobs(); len(got) != 0 {
		t.Fatalf("index jobs = %v, want none for a tip the index covers", got)
	}

	// A tip the forge does not give is asked for at the next poll. It has
	// no part in the pull requests' window, which the poll still records.
	polledAt := func() time.Time {
		t.Helper()
		var at time.Time
		if err := s.st.WithAccount(ctx, s.account.ID(), func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT last_polled_at FROM poll_state WHERE account_id = $1`, s.account.ID()).Scan(&at)
		}); err != nil {
			t.Fatal(err)
		}
		return at
	}
	s.exec(`UPDATE poll_state SET last_polled_at = now() - interval '1 hour' WHERE account_id = $1`, s.account.ID())
	before := polledAt()
	s.tf.err = errors.New("branch not found")
	s.poll()
	if after := polledAt(); !after.After(before) || len(s.indexJobs()) != 0 {
		t.Fatalf("after a failed tip: polled at %s, before %s, index jobs %v; want the poll recorded and no job", after, before, s.indexJobs())
	}
}

// TestPollerLogsAMissingBranchOnce: a default branch the forge does not
// have, of a repository deleted or a default branch renamed without an
// event, is logged once, not every poll.
func TestPollerLogsAMissingBranchOnce(t *testing.T) {
	s := newTipPoll(t, "onedr0p/home-ops")
	s.tf.err = fmt.Errorf("github: branch main of onedr0p/home-ops: %w", forge.ErrNotFound)
	s.poll()
	s.poll()
	logs := s.logs.String()
	if s.tf.calls != 2 || strings.Count(logs, "default branch unknown to the forge") != 1 || strings.Contains(logs, "default branch not polled") {
		t.Fatalf("%d tip checks, logs after two polls of a missing branch:\n%s\nwant it asked for twice and logged once", s.tf.calls, logs)
	}
}

// TestPollerBacksOffAfterAnIndexJobThatPublishedNothing: an index job that
// ended without bringing the index to its commit, failed or skipped because
// the repository's .kritika.yaml turns indexing off, will end so again. So
// the poll leaves the branch alone for jobs.OnboardRetryAfter after it, as
// the onboarding feeder does a repository whose first index failed. A job
// that published and then failed or found indexing off at a newer tip ended
// so too, as did a forced rebuild's catch-up that failed. The last job to
// end is the one that counts.
func TestPollerBacksOffAfterAnIndexJobThatPublishedNothing(t *testing.T) {
	s := newTipPoll(t, "onedr0p/home-ops")
	for _, tc := range []struct {
		name string
		// end finalizes the job the first poll queued, id, in its own way.
		end      string
		backsOff bool
	}{
		{name: "skipped", end: `UPDATE river_job SET state = 'completed', finalized_at = now() WHERE id = $1`, backsOff: true},
		{name: "failed", end: `UPDATE river_job SET state = 'discarded', finalized_at = now() WHERE id = $1`, backsOff: true},
		{name: "cancelled", end: `UPDATE river_job SET state = 'cancelled', finalized_at = now() WHERE id = $1`, backsOff: true},
		{name: "ended long ago", end: `UPDATE river_job SET state = 'discarded', finalized_at = now() - interval '61 minutes' WHERE id = $1`},
		{name: "published", end: `WITH run AS (INSERT INTO index_runs (account_id, repository_id, commit_sha, embed_model, embed_dims, mode, status)
			SELECT (args->>'account_id')::uuid, (args->>'repository_id')::uuid, 'published', 'fake-embed', 8, 'incremental', 'superseded'
			FROM river_job WHERE id = $1)
			UPDATE river_job SET state = 'completed', finalized_at = now() WHERE id = $1`},
		{name: "found the index at its commit", end: `UPDATE river_job SET state = 'completed', finalized_at = now(),
			args = jsonb_set(args, '{commit_sha}', '"indexed"') WHERE id = $1`},
		// The job published the commit it was queued for, snoozed for the
		// branch that moved meanwhile, and failed at the newer tip.
		{name: "published, then failed at a newer tip", end: `WITH run AS (INSERT INTO index_runs
			(account_id, repository_id, commit_sha, embed_model, embed_dims, mode, status)
			SELECT (args->>'account_id')::uuid, (args->>'repository_id')::uuid, 'published', 'fake-embed', 8, 'incremental', 'completed'
			FROM river_job WHERE id = $1)
			UPDATE river_job SET state = 'discarded', finalized_at = now(), args = jsonb_set(args, '{commit_sha}', '"indexed"') WHERE id = $1`,
			backsOff: true},
		{name: "published, then found a newer tip off", end: `WITH run AS (INSERT INTO index_runs
			(account_id, repository_id, commit_sha, embed_model, embed_dims, mode, status)
			SELECT (args->>'account_id')::uuid, (args->>'repository_id')::uuid, 'published', 'fake-embed', 8, 'incremental', 'completed'
			FROM river_job WHERE id = $1), off AS (UPDATE repositories SET repo_config_off = true
			WHERE id = (SELECT (args->>'repository_id')::uuid FROM river_job WHERE id = $1))
			UPDATE river_job SET state = 'completed', finalized_at = now(), args = jsonb_set(args, '{commit_sha}', '"indexed"') WHERE id = $1`,
			backsOff: true},
		// A forced rebuild's catch-up is queued for no commit.
		{name: "a catch-up that failed", end: `UPDATE river_job SET state = 'discarded', finalized_at = now(), args = args - 'commit_sha'
			WHERE id = $1`, backsOff: true},
		// The last job to end is the one that counts.
		{name: "published after a failure", end: `WITH later AS (INSERT INTO river_job (args, kind, max_attempts, queue, state, attempt, finalized_at)
			SELECT args, kind, max_attempts, queue, 'completed', 1, now() FROM river_job WHERE id = $1),
			run AS (INSERT INTO index_runs (account_id, repository_id, commit_sha, embed_model, embed_dims, mode, status)
			SELECT (args->>'account_id')::uuid, (args->>'repository_id')::uuid, 'published', 'fake-embed', 8, 'incremental', 'superseded'
			FROM river_job WHERE id = $1)
			UPDATE river_job SET state = 'discarded', finalized_at = now() - interval '10 minutes' WHERE id = $1`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(func() {
				s.exec(`DELETE FROM index_runs WHERE commit_sha = 'published' AND repository_id = $1`, s.repoIDs[0])
				s.exec(`UPDATE repositories SET repo_config_off = false WHERE id = $1`, s.repoIDs[0])
			})
			s.clearIndexJobs()
			s.tf.tip = "moved"
			s.poll()
			var id int64
			if err := s.st.App().QueryRow(t.Context(), `SELECT id FROM river_job WHERE kind = 'index' AND args->>'repository_id' = $1`,
				s.repoIDs[0]).Scan(&id); err != nil {
				t.Fatalf("the first poll's index job: %v", err)
			}
			s.exec(tc.end, id)
			calls, jobs := s.tf.calls, len(s.indexJobs())
			s.poll()
			asked, queued := s.tf.calls-calls, len(s.indexJobs())-jobs
			if tc.backsOff && (asked != 0 || queued != 0) {
				t.Fatalf("%d tip checks and %d index jobs queued, want the branch left alone", asked, queued)
			}
			if !tc.backsOff && (asked != 1 || queued != 1) {
				t.Fatalf("%d tip checks and %d index jobs queued, want one of each", asked, queued)
			}
		})
	}
}

// TestPollerChecksTipsHourlyWhileWebhooksArrive: while the connection's
// webhooks arrive, a push webhook is rarely missed, so the default branches
// are looked at once every tipCheckInterval, not every poll: a check costs a
// request per indexed repository. A connection whose webhooks have been
// silent for the poll lookback, or never arrived, has them looked at every
// poll. A newly elected leader looks at once.
func TestPollerChecksTipsHourlyWhileWebhooksArrive(t *testing.T) {
	s := newTipPoll(t, "onedr0p/home-ops")
	s.tf.tip = "indexed"
	for _, tc := range []struct {
		name string
		// delivered is when the webhook last arrived, as SQL.
		delivered string
		// checked is how long before the first poll this process last
		// looked, 0 for never.
		checked time.Duration
		want    int
	}{
		{name: "arriving, first poll", delivered: "now()", want: 1},
		{name: "arriving, checked lately", delivered: "now()", checked: 10 * time.Minute},
		{name: "arriving, checked over an interval ago", delivered: "now()", checked: tipCheckInterval + time.Minute, want: 1},
		{name: "silent past the lookback", delivered: "now() - interval '25 hours'", checked: time.Minute, want: 2},
		{name: "never arrived", delivered: "NULL", checked: time.Minute, want: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s.exec(`UPDATE connections SET last_webhook_at = `+tc.delivered+` WHERE id = $1`, s.in.ID())
			s.p.tips = nil
			if tc.checked > 0 {
				s.p.tips = map[string]*tipScan{s.account.ID(): {swept: time.Now().Add(-tc.checked)}}
			}
			calls := s.tf.calls
			s.poll()
			s.poll()
			if got := s.tf.calls - calls; got != tc.want {
				t.Fatalf("two polls looked at the branch %d times, want %d", got, tc.want)
			}
		})
	}
}

// TestPollerStopsTipChecksAtARateLimit: past the forge's rate limit, every
// other branch of the poll would only be refused too.
func TestPollerStopsTipChecksAtARateLimit(t *testing.T) {
	s := newTipPoll(t, "onedr0p/flux", "onedr0p/home-ops")
	s.tf.err = fmt.Errorf("github: branch main of onedr0p/flux: %w", &gh.RateLimitError{
		Rate: gh.Rate{Reset: gh.Timestamp{Time: time.Now().Add(time.Hour)}}, Response: &http.Response{StatusCode: http.StatusForbidden},
	})
	s.poll()
	if s.tf.calls != 1 || !strings.Contains(s.logs.String(), "default branches not polled past the rate limit") {
		t.Fatalf("%d tip checks, logs:\n%s\nwant one, and the rest logged as left", s.tf.calls, s.logs.String())
	}
}

// limitForge answers allowed branch lookups, then refuses the rest past
// GitHub's rate limit, which resets at once. It records the repositories it
// was asked for.
type limitForge struct {
	tipForge
	allowed int
	asked   []string
}

func (f *limitForge) BranchTip(_ context.Context, _, name, _ string) (string, string, error) {
	f.asked = append(f.asked, name)
	if f.allowed == 0 {
		return "", "", fmt.Errorf("github: branch main of onedr0p/%s: %w", name, &gh.RateLimitError{
			Rate: gh.Rate{Reset: gh.Timestamp{Time: time.Now()}}, Response: &http.Response{StatusCode: http.StatusForbidden},
		})
	}
	f.allowed--
	return f.tip, "main", nil
}

// TestPollerResumesTipChecksCutByARateLimit: a look at the default branches
// that the rate limit cuts short waits the limit out, and then resumes at
// the branch it stopped at, even while webhooks arrive. So looks that the
// limit cuts short every time still come round to every repository.
func TestPollerResumesTipChecksCutByARateLimit(t *testing.T) {
	s := newTipPoll(t, "onedr0p/flux", "onedr0p/home-ops", "onedr0p/talos")
	s.webhooks(true)
	lf := &limitForge{}
	lf.tip = "indexed"
	s.p.Forges = &forges{f: lf}
	look := func() {
		t.Helper()
		lf.allowed = 1
		s.poll()
	}
	look()
	look()
	if want := []string{"flux", "home-ops"}; !slices.Equal(lf.asked, want) {
		t.Fatalf("two looks within the limit's wait asked for %q, want %q", lf.asked, want)
	}
	for range 2 {
		time.Sleep(1100 * time.Millisecond)
		look()
	}
	if want := []string{"flux", "home-ops", "home-ops", "talos", "talos", "flux"}; !slices.Equal(lf.asked, want) {
		t.Fatalf("looks past the limit's waits asked for %q, want %q", lf.asked, want)
	}
}

// stallTipForge looks up no branch until the test releases it, whatever its
// context, as a forge that never answers. It lists a pull request of
// home-ops alone, and counts the listings of home-ops.
type stallTipForge struct {
	tipForge
	entered, release chan struct{}
	enter            sync.Once
	listed           atomic.Int32
}

func (f *stallTipForge) BranchTip(context.Context, string, string, string) (string, string, error) {
	f.enter.Do(func() { close(f.entered) })
	<-f.release
	return "", "", errors.New("stalled")
}

func (*stallTipForge) ListInline(context.Context, string, string, int) ([]forge.Comment, error) {
	return nil, nil
}

func (f *stallTipForge) ListOpenPullRequests(_ context.Context, _, name string, _ time.Time) ([]forge.OpenPullRequest, error) {
	if name != "home-ops" {
		return nil, nil
	}
	f.listed.Add(1)
	return []forge.OpenPullRequest{{
		Number: 19, Title: "poll me", Author: "onedr0p", State: "open", HeadRef: "h", HeadSHA: "abc919", BaseRef: "main",
		UpdatedAt: time.Now(), DefaultBranch: "main",
	}}, nil
}

// TestPollerPollsPullRequestsPastAStalledBranch: a branch lookup that does
// not answer holds back no pull request, of a later repository either, and
// the poll state still advances. The next poll starts on its interval while
// the lookup still hangs.
func TestPollerPollsPullRequestsPastAStalledBranch(t *testing.T) {
	t.Setenv("KRITIKA_POLL_INTERVAL", "300ms")
	s := newTipPoll(t, "onedr0p/flux", "onedr0p/home-ops")
	s.exec(`DELETE FROM poll_state WHERE account_id = $1`, s.account.ID())
	s.exec(`DELETE FROM pull_requests WHERE number = 19`)
	f := &stallTipForge{entered: make(chan struct{}), release: make(chan struct{})}
	s.p.Forges = &forges{f: f}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.p.Run(ctx)
	}()
	var stop sync.Once
	end := func() {
		stop.Do(func() {
			close(f.release)
			cancel()
			<-done
		})
	}
	defer end()
	select {
	case <-f.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("no branch was looked up within 10s of the poller starting")
	}
	waitUntil(t, "second poll of the pull requests while the branch lookup hangs", func() bool { return f.listed.Load() >= 2 })
	end()
	var polled []int
	for _, ev := range s.dispatched.events {
		if ev.Kind == webhook.KindPullRequest && ev.Repository.FullName == "onedr0p/home-ops" {
			polled = append(polled, ev.PullRequest.Number)
		}
	}
	var recorded bool
	if err := s.st.WithAccount(t.Context(), s.account.ID(), func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT EXISTS (SELECT 1 FROM poll_state WHERE account_id = $1)`, s.account.ID()).Scan(&recorded)
	}); err != nil {
		t.Fatal(err)
	}
	if len(polled) < 2 || slices.ContainsFunc(polled, func(n int) bool { return n != 19 }) || !recorded {
		t.Fatalf("pull requests of home-ops polled %v, poll state recorded %v; want #19 at each poll and the state recorded", polled, recorded)
	}
}

// waitUntil fails the test unless cond holds within 10s.
func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("no %s within 10s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestPollerRechecksAnOptOutAtTheIndexedCommit: a repository whose
// .kritika.yaml turned indexing off, at a branch that a force push took back
// to the indexed commit with no webhook, has its file read again. Otherwise
// the mark stays and retention drops an index that the file turns on. A job
// that finds it still off is not queued again every poll.
func TestPollerRechecksAnOptOutAtTheIndexedCommit(t *testing.T) {
	s := newTipPoll(t, "onedr0p/home-ops")
	s.tf.tip = "indexed"
	s.exec(`UPDATE repositories SET repo_config_off = true WHERE id = $1`, s.repoIDs[0])
	t.Cleanup(func() { s.exec(`UPDATE repositories SET repo_config_off = false WHERE id = $1`, s.repoIDs[0]) })
	s.poll()
	if got := s.indexJobs(); !slices.Equal(got, []string{"indexed"}) {
		t.Fatalf("index jobs = %v, want one at the indexed commit", got)
	}
	s.exec(`UPDATE river_job SET state = 'completed', finalized_at = now() WHERE kind = 'index'`)
	calls := s.tf.calls
	s.poll()
	if got := s.indexJobs(); s.tf.calls != calls || len(got) != 1 {
		t.Fatalf("after a job that found it off: %d tip checks and index jobs %v, want none and the one", s.tf.calls-calls, got)
	}
}

// TestSyncRepositories: the repositories that a running connection's App
// reaches are registered on the accounts it serves, with what the forge says
// of them. A second sync adds none. An account that it does not serve gets
// nothing. A repository that the listing no longer has is disabled, but not
// one that a webhook named while the listing ran. A listing that fails or
// comes back empty disables nothing.
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
		Reach: func(_ context.Context, in *configfile.Connection) (map[string]store.Listing, error) {
			asked = append(asked, in.Name)
			return map[string]store.Listing{
				"onedr0p": {Repos: []store.ReachedRepository{
					{FullName: "onedr0p/synced", DefaultBranch: "main", Traits: &configfile.RepoTraits{}},
					{FullName: "onedr0p/synced-fork", DefaultBranch: "main", Traits: &configfile.RepoTraits{Fork: true}},
				}},
				"stranger": {Repos: []store.ReachedRepository{{FullName: "stranger/elsewhere", Traits: &configfile.RepoTraits{}}}},
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
	if err := st.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
		for _, name := range []string{"onedr0p/synced", "onedr0p/synced-fork"} {
			r, err := store.FindRepo(ctx, tx, name)
			if err != nil {
				return err
			}
			if file.Runs(account, r.RepoTraits) {
				t.Errorf("newly discovered %s should wait for dashboard enablement", name)
			}
		}
		return store.TurnOn(ctx, tx, configfile.RepositoryID(account.ID(), "onedr0p/synced"), true)
	}); err != nil {
		t.Fatal(err)
	}
	logs.Reset()
	p.SyncRepositories(ctx)
	if got := rows(); got != want || strings.Contains(logs.String(), "repositories registered") {
		t.Fatalf("a second sync: repositories %q, logs %s; want nothing new", got, logs.String())
	}
	if err := st.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
		r, err := store.FindRepo(ctx, tx, "onedr0p/synced")
		if err == nil && !file.Runs(account, r.RepoTraits) {
			t.Error("resync should preserve dashboard enablement")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}

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
	p.Reach = func(context.Context, *configfile.Connection) (map[string]store.Listing, error) {
		close(write)
		if err := <-named; err != nil {
			return nil, err
		}
		return map[string]store.Listing{
			"onedr0p": {Repos: []store.ReachedRepository{{FullName: "onedr0p/synced", DefaultBranch: "main", Traits: &configfile.RepoTraits{}}}},
		}, nil
	}
	p.SyncRepositories(ctx)
	want = "onedr0p/synced:true:main:false,onedr0p/synced-fork:false:main:true,onedr0p/synced-late:true::false"
	if got := rows(); got != want || !strings.Contains(logs.String(), "disabled=1") {
		t.Fatalf("a sync without synced-fork: repositories %q, logs %s; want %q", got, logs.String(), want)
	}

	p.Reach = func(context.Context, *configfile.Connection) (map[string]store.Listing, error) {
		return map[string]store.Listing{"onedr0p": {}}, nil
	}
	p.SyncRepositories(ctx)
	if got := rows(); got != want {
		t.Fatalf("an empty listing: repositories %q, want %q", got, want)
	}
	p.Reach = func(context.Context, *configfile.Connection) (map[string]store.Listing, error) {
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
	storetest.Repositories(t, st, file, "onedr0p/home-ops")

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
// listing of the one named failing. It has no inline comments to read
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

// TestPollerPollsPastAFailingRepository: a repository that the forge will
// not list is skipped, and the others are still polled. The poll state is
// left for the next pass to cover the skipped one.
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
	storetest.Repositories(t, st, file, "onedr0p/home-ops")
	if _, _, err := st.RegisterRepositories(ctx, account.ID(), store.Listing{Repos: []store.ReachedRepository{
		{FullName: "onedr0p/flaky", DefaultBranch: "main", Traits: &configfile.RepoTraits{}},
	}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	// A webhook reached the connection lately, so the poll looks at no default
	// branch. This poll is about the listing alone.
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
		if err := store.TurnOn(ctx, tx, configfile.RepositoryID(account.ID(), "onedr0p/flaky"), true); err != nil {
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
	storetest.Repositories(t, st, file, "onedr0p/home-ops")
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

// TestPollerClosesAPullRequestWhoseEventWasMissed: a pull request that
// kritika holds open and that the forge no longer lists as open is asked for
// by number. It is recorded closed, merged or not. One that the forge still
// has open, and one that it does not know, are left as they are.
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
	storetest.Repositories(t, st, file, "onedr0p/home-ops")
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
	// opened since the listing is. The forge does not know 934.
	lf := &listForge{byNumber: map[int]forge.OpenPullRequest{931: merged, 932: closed, 933: open(933)}}
	p := &Poller{
		Store: st, Current: configfile.NewCurrent(file), Forges: &forges{f: lf}, Dispatcher: svc, Logger: slog.New(slog.DiscardHandler),
	}
	if _, err := p.Poll(ctx, file, account, in); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	// The one that the forge did not know is not asked for again, so it cannot
	// use up a poll's checks for good. The others are asked each poll.
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

// TestPollerRunPollsOnStart: a leader polls as it starts, before its first
// interval, so what it missed while there was none is caught up at once.
func TestPollerRunPollsOnStart(t *testing.T) {
	ctx := t.Context()
	st := storetest.Open(t)
	t.Setenv("TEST_PEM", "pem")
	t.Setenv("TEST_SECRET", "s")
	t.Setenv("KRITIKA_POLL_INTERVAL", "1h")
	file := configfiletest.Load(t, configYAML)
	if err := st.ApplyConfig(ctx, file); err != nil {
		t.Fatal(err)
	}
	queue, err := river.NewClient(riverpgxv5.New(st.App()), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	storetest.Repositories(t, st, file, "onedr0p/home-ops")
	lf := &listForge{}
	p := &Poller{
		Store: st, Current: configfile.NewCurrent(file), Forges: &forges{f: lf}, Dispatcher: ingest.NewService(st, queue),
		Logger: slog.New(slog.DiscardHandler),
	}
	rctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.Run(rctx)
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		lf.mu.Lock()
		listed := len(lf.sinces)
		lf.mu.Unlock()
		if listed > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the forge was not listed within 10s of the poller starting at a 1h interval")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done
}
