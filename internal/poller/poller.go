// Package poller backstops missed webhooks. On the leader, every interval and
// per account, it lists the open pull requests. It hands those updated since
// the last poll to the ingest dispatcher as if a webhook delivered them. It
// also closes the ones that kritika holds open and that the forge no longer
// lists. Review jobs are unique on the head SHA, so a head that the webhook
// already enqueued is skipped as a duplicate, never reviewed twice.
//
// An account's first poll records the pull requests last updated before
// kritika knew the account as a baseline instead of reviewing them. No
// webhook for them was missed. On a large install, a review of them all is
// one burst of model calls that nobody asked for.
package poller

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/forge"
	"github.com/home-operations/kritika/internal/ingest"
	"github.com/home-operations/kritika/internal/metrics"
	"github.com/home-operations/kritika/internal/store"
	"github.com/home-operations/kritika/internal/webhook"
)

// Poller lists open pull requests on a schedule.
type Poller struct {
	Store      *store.Store
	Current    *configfile.Current
	Forges     forge.Clients
	Dispatcher ingest.Dispatcher
	Logger     *slog.Logger
	// Metrics can be nil.
	Metrics *metrics.Metrics
	// Reach lists, by lowercased account login, the repositories that a
	// connection's App reaches. A nil Reach registers none.
	Reach func(ctx context.Context, in *configfile.Connection) (map[string]store.Listing, error)

	// unknown are the pull requests held open that the forge did not know
	// when asked, by "owner/name#number". They are asked once per process, so
	// they neither cost a request every poll nor use up the poll's checks.
	mu      sync.Mutex
	unknown map[string]bool
}

// pollOffRecheck is how often Run looks again at a file that turns
// polling off, so turning it back on takes effect without a restart.
const pollOffRecheck = time.Minute

// reachTimeout bounds one connection's repository listing: the leader
// lists right after applying the configuration and applies no change
// until it is done, so a hung forge must not hold that up.
const reachTimeout = time.Minute

// Run polls until ctx ends, every polling.interval of the file current at
// the time, and once as it starts. A leader is elected when the last one
// died or on a restart. The first poll is for the events of that gap, and a
// pull request's close is among them. The jobs that the rescuer hands back
// shortly after read what that poll records. The caller has the repositories
// listed already, so the first poll does not list them again.
//
// A poll is given the interval. A poll that outlasts it, on a forge that
// answers slowly for every repository of a large account, is cut there. Its
// accounts' poll state is left where it was for the next poll.
func (p *Poller) Run(ctx context.Context) {
	p.poll(ctx, p.pollAccounts)
	for {
		t := time.NewTimer(cmp.Or(p.Current.Get().PollInterval(), pollOffRecheck))
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
			p.poll(ctx, p.PollAll)
		}
	}
}

// poll runs one poll, cut at the interval current as it starts. It runs
// none while polling is off.
func (p *Poller) poll(ctx context.Context, poll func(context.Context)) {
	interval := p.Current.Get().PollInterval()
	if interval <= 0 {
		return
	}
	pctx, cancel := context.WithTimeout(ctx, interval)
	defer cancel()
	poll(pctx)
	if pctx.Err() != nil && ctx.Err() == nil {
		p.Logger.Warn("poll cut at its interval", "interval", interval)
	}
}

// SyncRepositories registers the repositories that each running connection's
// App reaches on the accounts it serves, as a webhook from each does. So they
// appear in the dashboard without a webhook, because an App installed before
// kritika started sends no installation event. New repositories wait for an
// admin to turn them on before polling or indexing. The forge-reported
// repositories that an account's listing no longer has are disabled, in case
// the webhook that said so was missed. A connection whose listing fails, or
// takes longer than reachTimeout, is logged and left for the next poll. An
// account whose listing is empty, or that Reach left out, is left as it is.
// A partial listing registers what it has and disables nothing.
func (p *Poller) SyncRepositories(ctx context.Context) {
	if p.Reach == nil {
		return
	}
	file := p.Current.Get()
	for i := range file.Connections {
		in := &file.Connections[i]
		if ctx.Err() != nil {
			return
		}
		// The listing's time is the database's, which stamps the rows that a
		// webhook names meanwhile. This clock can differ from it.
		listedAt, err := p.Store.Now(ctx)
		if err != nil {
			p.Logger.Warn("repositories not synced", "connection", in.Name, "error", err)
			continue
		}
		reachCtx, cancel := context.WithTimeout(ctx, reachTimeout)
		reach, err := p.Reach(reachCtx, in)
		cancel()
		if err != nil {
			p.Logger.Warn("repositories not synced", "connection", in.Name, "error", err)
			continue
		}
		for j := range file.Accounts {
			account := &file.Accounts[j]
			if c := file.ConnectionFor(account); c == nil || c.Name != in.Name {
				continue
			}
			listing := reach[strings.ToLower(account.Name)]
			if len(listing.Repos) == 0 {
				continue
			}
			added, disabled, err := p.Store.RegisterRepositories(ctx, account.ID(), listing, listedAt)
			if err != nil {
				p.Logger.Warn("repositories not synced", "connection", in.Name, "account", account.Key(), "error", err)
				continue
			}
			if added > 0 {
				p.Logger.Info("repositories registered", "connection", in.Name, "account", account.Key(), "added", added)
			}
			if disabled > 0 {
				p.Logger.Info("repositories no longer listed disabled", "connection", in.Name, "account", account.Key(), "disabled", disabled)
			}
		}
	}
}

// PollAll registers the repositories the Apps reach, then polls every
// account in the current configuration through the connection serving it.
func (p *Poller) PollAll(ctx context.Context) {
	p.SyncRepositories(ctx)
	p.pollAccounts(ctx)
}

// pollAccounts polls every account of the current file through the
// connection serving it.
func (p *Poller) pollAccounts(ctx context.Context) {
	file := p.Current.Get()
	for i := range file.Accounts {
		account := &file.Accounts[i]
		in := file.ConnectionFor(account)
		if in == nil || ctx.Err() != nil {
			continue
		}
		n, err := p.Poll(ctx, file, account, in)
		switch {
		case err != nil:
			p.Logger.Warn("poll failed", "connection", in.Name, "account", account.Key(), "error", err)
			p.Metrics.Poll(in.Name, "error", 0)
		default:
			p.Metrics.Poll(in.Name, "ok", n)
		}
	}
}

// Poll lists one account's repositories through the connection serving it
// and returns how many pull requests were handed to the dispatcher. For a
// connection that no webhook reached lately, it also looks at each indexed
// repository's default branch. No push webhook will say that the branch
// moved. A repository that the forge will not answer for is skipped, and the
// rest are polled. The poll state is then left where it was, so the next poll
// covers the skipped repository's window too. The error names each one.
func (p *Poller) Poll(ctx context.Context, file *configfile.File, account *configfile.Account, in *configfile.Connection) (int, error) {
	// Capture the boundary before reading enablement, on the same clock as
	// turned_at, so a concurrent enablement is caught by the next poll.
	started, err := p.Store.Now(ctx)
	if err != nil {
		return 0, err
	}
	var repos []store.PollRepo
	var state store.PollState
	err = p.Store.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
		var err error
		if repos, err = store.PollRepositories(ctx, tx); err != nil {
			return err
		}
		state, err = store.ReadPollState(ctx, tx, account.ID(), in.ID())
		return err
	})
	if err != nil {
		return 0, err
	}
	since := time.Now().Add(-file.PollLookback())
	if state.Polled != nil && state.Polled.After(since) {
		since = *state.Polled
	}
	checkTips := state.Delivered == nil || state.Delivered.Before(time.Now().Add(-file.PollLookback()))
	handled := 0
	runs := map[string]bool{}
	var skipped []error
	for _, r := range repos {
		if ctx.Err() != nil {
			return handled, ctx.Err()
		}
		repo := r.Name
		// The App can reach it, but it is archived, a fork not turned on,
		// or off in the configuration.
		if !file.Runs(account, r.Traits) {
			continue
		}
		runs[repo] = true
		// The client is the account's, not the repository's: without it
		// nothing else can be polled either.
		client, err := p.Forges.For(ctx, in, repo)
		if err != nil {
			return handled, err
		}
		owner, name, _ := strings.Cut(repo, "/")
		if checkTips && r.IndexedCommit != "" {
			if err := p.pollTip(ctx, file, account, in, client, r, started); err != nil {
				skipped = append(skipped, fmt.Errorf("%s: %w", repo, err))
			}
		}
		// Every open pull request is listed, not only those updated since
		// the last poll. The ones that kritika holds open and that the forge
		// no longer lists are the ones that closed.
		prs, err := client.ListOpenPullRequests(ctx, owner, name, time.Time{})
		if err != nil {
			skipped = append(skipped, fmt.Errorf("%s: %w", repo, err))
			continue
		}
		if err := p.closeMissed(ctx, file, account, in, client, r, prs); err != nil {
			skipped = append(skipped, fmt.Errorf("%s: %w", repo, err))
		}
		// Enabling a repository also picks up open pulls from while it was
		// off, even when the account's poll cursor passed their updates.
		catchUp := r.TurnedAt != nil && (state.Polled == nil || !r.TurnedAt.Before(*state.Polled))
		for _, pr := range prs {
			if !catchUp && pr.UpdatedAt.Before(since) {
				continue
			}
			action := ingest.ActionPoll
			if !catchUp && state.Polled == nil && !pr.UpdatedAt.After(state.Known) {
				action = ingest.ActionBaseline
			}
			ev := webhook.Event{
				Kind: webhook.KindPullRequest, Action: action, Delivery: fmt.Sprintf("poll-%s-%d", started.UTC().Format("20060102T150405"), pr.Number),
				Repository: &webhook.Repository{FullName: repo, DefaultBranch: pr.DefaultBranch, RepoTraits: r.Traits},
				Account:    owner, PullRequest: &pr.PullRequest,
			}
			out, err := p.Dispatcher.Dispatch(ctx, ingest.Request{File: file, Account: account, Event: ev})
			if err != nil {
				return handled, err
			}
			handled++
			p.Logger.Info("polled pull request", "status", out.Status, "connection", in.Name, "repository", repo,
				"pr", pr.Number, "reason", out.Reason)
		}
	}
	if err := p.pollReactions(ctx, account, in, runs); err != nil {
		p.Logger.Warn("reactions not read", "connection", in.Name, "account", account.Key(), "error", err)
	}
	if len(skipped) > 0 {
		return handled, fmt.Errorf("poller: %d of %d repositories not polled: %w", len(skipped), len(runs), errors.Join(skipped...))
	}
	err = p.Store.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
		return store.RecordPoll(ctx, tx, account.ID(), started)
	})
	return handled, err
}

// maxCloseChecks bounds how many pull requests one poll of a repository
// asks the forge about one by one. The rest wait for the next poll.
const maxCloseChecks = 50

// closeMissed records as closed the pull requests of repository r that
// kritika holds open and that listed, the forge's open ones, does not have.
// Otherwise a closed event that was never delivered leaves one open for good.
// closeMissed asks for each one by number. So one opened since the listing
// is left as it is. One that the forge no longer knows is left for a person,
// and closeMissed does not ask for it again while the process runs.
func (p *Poller) closeMissed(
	ctx context.Context, file *configfile.File, account *configfile.Account, in *configfile.Connection, client forge.Client,
	r store.PollRepo, listed []forge.OpenPullRequest,
) error {
	var held []int
	err := p.Store.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
		var err error
		held, err = store.OpenPullRequests(ctx, tx, configfile.RepositoryID(account.ID(), r.Name))
		return err
	})
	if err != nil {
		return err
	}
	open := make(map[int]bool, len(listed))
	for _, pr := range listed {
		open[pr.Number] = true
	}
	owner, name, _ := strings.Cut(r.Name, "/")
	checks := 0
	for _, number := range held {
		key := fmt.Sprintf("%s#%d", r.Name, number)
		if open[number] || p.isUnknown(key) {
			continue
		}
		if checks++; checks > maxCloseChecks {
			break
		}
		pr, err := client.PullRequest(ctx, owner, name, number)
		if errors.Is(err, forge.ErrNotFound) {
			p.Logger.Warn("pull request held open is unknown to the forge", "connection", in.Name, "repository", r.Name, "pr", number)
			p.markUnknown(key)
			continue
		}
		if err != nil {
			return err
		}
		if pr.State != "closed" {
			continue
		}
		ev := webhook.Event{
			Kind: webhook.KindPullRequest, Action: "closed", Delivery: fmt.Sprintf("poll-closed-%d", number),
			Repository: &webhook.Repository{FullName: r.Name, DefaultBranch: pr.DefaultBranch, RepoTraits: r.Traits},
			Account:    owner, PullRequest: &pr.PullRequest,
		}
		if _, err := p.Dispatcher.Dispatch(ctx, ingest.Request{File: file, Account: account, Event: ev}); err != nil {
			return err
		}
		p.Logger.Info("pull request closed without its event", "connection", in.Name, "repository", r.Name, "pr", number, "merged", pr.Merged)
	}
	return nil
}

func (p *Poller) isUnknown(key string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.unknown[key]
}

func (p *Poller) markUnknown(key string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.unknown == nil {
		p.unknown = map[string]bool{}
	}
	p.unknown[key] = true
}

// reactionWindow is how long after its latest review a pull request's
// reactions are read again: a finding is reacted to soon after it is
// posted, if at all.
const reactionWindow = 7 * 24 * time.Hour

// reactionPulls bounds the pull requests that one poll reads the reactions
// of, per account, the most recently reviewed first. So an account with many
// open pull requests does not spend its API quota on them every poll.
const reactionPulls = 30

// pollReactions reads the 👍 and 👎 on the inline comments kritika posted
// on the account's recently reviewed pull requests, in the repositories
// that run, into their findings. GitHub sends no webhook for a reaction.
func (p *Poller) pollReactions(ctx context.Context, account *configfile.Account, in *configfile.Connection, runs map[string]bool) error {
	var pulls []store.ReviewedPull
	err := p.Store.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
		var err error
		pulls, err = store.RecentlyReviewedPulls(ctx, tx, time.Now().Add(-reactionWindow), reactionPulls)
		return err
	})
	if err != nil {
		return err
	}
	for _, pr := range pulls {
		if !runs[pr.Repository] || ctx.Err() != nil {
			continue
		}
		client, err := p.Forges.For(ctx, in, pr.Repository)
		if err != nil {
			return err
		}
		owner, name, _ := strings.Cut(pr.Repository, "/")
		comments, err := client.ListInline(ctx, owner, name, pr.Number)
		if err != nil {
			return err
		}
		reactions := make([]store.Reaction, len(comments))
		for i, c := range comments {
			reactions[i] = store.Reaction{CommentID: c.ID, Up: c.ReactionsUp, Down: c.ReactionsDown}
		}
		err = p.Store.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
			return store.RecordReactions(ctx, tx, pr.ID, reactions)
		})
		if err != nil {
			return fmt.Errorf("poller: write reactions of %s#%d: %w", pr.Repository, pr.Number, err)
		}
	}
	return nil
}

// pollTip hands the dispatcher a push to r's default branch when its tip is
// not the commit that r's index covers. So the index follows the branch as a
// push webhook makes it do.
func (p *Poller) pollTip(
	ctx context.Context, file *configfile.File, account *configfile.Account, in *configfile.Connection, client forge.Client, r store.PollRepo,
	started time.Time,
) error {
	owner, name, _ := strings.Cut(r.Name, "/")
	tip, branch, err := client.BranchTip(ctx, owner, name, r.DefaultBranch)
	if err != nil {
		return err
	}
	if tip == r.IndexedCommit {
		return nil
	}
	ev := webhook.Event{
		Kind: webhook.KindPush, Action: ingest.ActionPoll, Delivery: fmt.Sprintf("poll-%s-push", started.UTC().Format("20060102T150405")),
		Repository: &webhook.Repository{FullName: r.Name, DefaultBranch: branch, RepoTraits: r.Traits}, Account: owner,
		Push: &webhook.Push{Ref: "refs/heads/" + branch, After: tip},
	}
	out, err := p.Dispatcher.Dispatch(ctx, ingest.Request{File: file, Account: account, Event: ev})
	if err != nil {
		return err
	}
	p.Logger.Info("polled default branch", "status", out.Status, "connection", in.Name, "repository", r.Name, "tip", tip, "reason", out.Reason)
	return nil
}
