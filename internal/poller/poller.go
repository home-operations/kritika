// Package poller backstops missed webhooks. On the leader, every interval and
// per account, it lists the open pull requests. It hands those updated since
// the last poll to the ingest dispatcher as if a webhook delivered them. It
// also closes the ones that kritika holds open and that the forge no longer
// lists. Review jobs are unique on the head SHA, so a head that the webhook
// already enqueued is skipped as a duplicate, never reviewed twice. It also
// hands over a push for each indexed repository whose default branch moved
// past its index.
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
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/forge"
	"github.com/home-operations/kritika/internal/ingest"
	"github.com/home-operations/kritika/internal/metrics"
	"github.com/home-operations/kritika/internal/store"
	"github.com/home-operations/kritika/internal/tuning"
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
	// when asked, by "<connection id>:owner/name#number". They are asked once
	// per process, so they neither cost a request every poll nor use up the
	// poll's checks. It also holds the default branches, by
	// "<connection id>:owner/name@branch", that the forge did not know, so
	// each is logged once for each connection that serves it.
	mu      sync.Mutex
	unknown map[string]bool
	// tips is how far the polls got through each account's default
	// branches, by account ID.
	tips map[string]*tipScan
	// tipsFirst is the account, by ID, that the next look at the default
	// branches starts at, "" for the first.
	tipsFirst string
	// tipsLimited is when the forge's rate limit lets a look at the default
	// branches ask again, by the credential that it limits, as limitScope
	// names it.
	tipsLimited map[string]time.Time
	// tipsRefused is the account, by ID, whose lookup the rate limit last
	// refused, by the credential that it limits, until that account's look
	// goes ahead.
	tipsRefused map[string]string
	// now is the clock that schedules the looks at the default branches,
	// time.Now when nil: a field for the tests.
	now func() time.Time
}

// tipScan is how far the polls got through an account's default branches.
type tipScan struct {
	// swept is when a look last went through every repository.
	swept time.Time
	// next is the repository that a look cut short resumes at, "" for the
	// first.
	next string
}

// Run polls until ctx ends, every polling.interval of the file current at
// the time, and once as it starts. A leader is elected when the last one
// died or on a restart. The first poll is for the events of that gap, and a
// pull request's close is among them. The jobs that the rescuer hands back
// shortly after read what that poll records. The caller has the repositories
// listed already, so the first poll does not list them again.
//
// A poll is given the interval. A poll that outlasts it, on a forge that
// answers slowly for every repository of a large account, is cut there. Its
// accounts' poll state is left where it was for the next poll. The looks at
// the default branches run beside the polls, on a schedule of their own, so
// that neither a slow branch lookup nor a slow listing delays the other's
// next start. Run returns once both have stopped.
func (p *Poller) Run(ctx context.Context) {
	var tips sync.WaitGroup
	defer tips.Wait()
	tips.Go(func() { p.runTips(ctx) })
	p.poll(ctx, p.pollAccounts)
	for {
		t := time.NewTimer(cmp.Or(p.Current.Get().PollInterval(), tuning.Get().Poller.PollOffRecheck))
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

// runTips looks at the default branches until ctx ends: once as it starts,
// then an interval after each look started, with the interval current at
// the time. A look is cut at its interval, so one look ends before the
// next starts. It looks at none while polling is off.
func (p *Poller) runTips(ctx context.Context) {
	for {
		started := time.Now()
		if interval := p.Current.Get().PollInterval(); interval > 0 {
			p.sweepTips(ctx, interval)
		}
		t := time.NewTimer(cmp.Or(p.Current.Get().PollInterval(), tuning.Get().Poller.PollOffRecheck) - time.Since(started))
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
}

// sweepTips looks at the default branches once, cut at interval, and warns
// when the cut stopped it.
func (p *Poller) sweepTips(ctx context.Context, interval time.Duration) {
	tctx, cancel := context.WithTimeout(ctx, interval)
	defer cancel()
	p.pollTipsAll(tctx)
	if errors.Is(tctx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
		p.Logger.Warn("default branches not all looked at within the poll interval, the next look resumes", "interval", interval)
	}
}

// SyncRepositories registers the repositories that each running connection's
// App reaches on the accounts it serves, as a webhook from each does. So they
// appear in the dashboard without a webhook, because an App installed before
// kritika started sends no installation event. New repositories wait for an
// admin to turn them on before polling or indexing. The forge-reported
// repositories that an account's listing no longer has are disabled, in case
// the webhook that said so was missed. A connection whose listing fails, or
// takes longer than tuning's Poller.ReachTimeout, is logged and left for the
// next poll. An account whose listing is empty, or that Reach left out, is
// left as it is. A partial listing registers what it has and disables
// nothing.
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
		reachCtx, cancel := context.WithTimeout(ctx, tuning.Get().Poller.ReachTimeout)
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
// and returns how many pull requests were handed to the dispatcher. A
// repository whose pull requests the forge will not list is skipped, and the
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
		if repos, err = store.PollPullRepositories(ctx, tx); err != nil {
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
		key := fmt.Sprintf("%s:%s#%d", in.ID(), r.Name, number)
		if open[number] || p.isUnknown(key) {
			continue
		}
		if checks++; checks > tuning.Get().Poller.MaxCloseChecks {
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

// branchUnknown logs, once per process, a default branch of r that the forge
// does not have: r was deleted, or its default branch renamed, and no event
// said so. The branch is asked for again at the next look at the account's
// branches anyway, after the repository sync has recorded a renamed one or
// disabled a deleted one.
func (p *Poller) branchUnknown(in *configfile.Connection, r store.PollRepo, err error) {
	key := in.ID() + ":" + r.Name + "@" + r.DefaultBranch
	if p.isUnknown(key) {
		return
	}
	p.markUnknown(key)
	p.Logger.Warn("default branch unknown to the forge", "connection", in.Name, "repository", r.Name, "branch", r.DefaultBranch,
		"error", err)
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

// pollReactions reads the 👍 and 👎 on the inline comments kritika posted
// on the account's recently reviewed pull requests, in the repositories
// that run, into their findings. GitHub sends no webhook for a reaction.
func (p *Poller) pollReactions(ctx context.Context, account *configfile.Account, in *configfile.Connection, runs map[string]bool) error {
	t := tuning.Get().Poller
	var pulls []store.ReviewedPull
	err := p.Store.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
		var err error
		pulls, err = store.RecentlyReviewedPulls(ctx, tx, time.Now().Add(-t.ReactionWindow), t.ReactionPulls)
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

// pollTipsAll looks at the default branches of every account of the current
// file through the connection serving it, with pollTips, in tipsOrder. Each
// account's own sweep resumes where it stopped, whatever the order. Without
// an embedder it looks at none: no index job runs, and the one that a moved
// branch queued would only be cancelled, and queued again at the next look
// past its backoff, while the indexes wait for an embedder.
func (p *Poller) pollTipsAll(ctx context.Context) {
	file := p.Current.Get()
	if file.Embedding == nil {
		return
	}
	order := p.tipsOrder(file)
	for i, account := range order {
		in := file.ConnectionFor(account)
		if in == nil {
			continue
		}
		if ctx.Err() != nil {
			p.setTipsFirst(account.ID())
			return
		}
		if err := p.pollTips(ctx, file, account, in); err != nil {
			p.Logger.Warn("default branches not polled", "connection", in.Name, "account", account.Key(), "error", err)
		}
		if ctx.Err() != nil {
			p.setTipsFirst(order[(i+1)%len(order)].ID())
			return
		}
	}
}

// setTipsFirst records the account that the next look at the default
// branches starts at.
func (p *Poller) setTipsFirst(accountID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tipsFirst = accountID
}

// pollTips looks at the default branch of each of the account's indexed
// repositories with no index job queued or running, whatever webhooks
// arrive: one push webhook missed, or one index job that failed, leaves the
// index behind until the next push otherwise. It looks at every interval while
// the connection's webhooks have been silent for the poll lookback, and every
// tuning's Poller.TipCheckInterval while they arrive. A repository whose last
// index job ended without moving its index waits tuning's
// Jobs.OnboardRetryAfter for its next look, as the onboarding feeder waits to
// offer a repository whose first index failed again. A branch that the forge
// will not give is logged and asked for again at the next look.
//
// A sweep looks at the repositories in name order, from the first to the
// last. A look that ctx or the forge's rate limit cuts short leaves the
// sweep at the repository it stopped at, and the next look past the limit's
// wait goes on from there to the last name, not round again. The wait holds
// back the looks of every account whose lookups the limit counts, as
// limitScope names them. A sweep that ends, in one look or over several,
// counts toward that interval. So looks cut short every time still
// come round to each repository, and stop asking once they have. A repository
// named before where a sweep was cut, added meanwhile, waits for the next
// sweep.
func (p *Poller) pollTips(ctx context.Context, file *configfile.File, account *configfile.Account, in *configfile.Connection) error {
	// Whether the look is due needs only when the webhook last arrived, so a
	// look that is not due lists no repository and reads no job.
	var state store.PollState
	err := p.Store.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
		var err error
		state, err = store.ReadPollState(ctx, tx, account.ID(), in.ID())
		return err
	})
	if err != nil {
		return err
	}
	scope := limitScope(in, account)
	next, due := p.tipsDue(account.ID(), scope, state.Delivered, file.PollLookback())
	if !due {
		return nil
	}
	started, err := p.Store.Now(ctx)
	if err != nil {
		return err
	}
	var repos []store.PollRepo
	err = p.Store.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
		var err error
		repos, err = store.PollRepositories(ctx, tx, tuning.Get().Jobs.OnboardRetryAfter)
		return err
	})
	if err != nil {
		return err
	}
	// In Go's order, which the database's collation need not be.
	slices.SortFunc(repos, func(a, b store.PollRepo) int { return strings.Compare(a.Name, b.Name) })
	first, _ := slices.BinarySearchFunc(repos, next, func(r store.PollRepo, name string) int { return strings.Compare(r.Name, name) })
	for _, r := range repos[first:] {
		if ctx.Err() != nil {
			p.tipsCut(account.ID(), r.Name)
			return nil
		}
		// A queued or running index job reads the tip itself, and a push
		// dedupes onto it: the forge is not asked again. An index job that
		// ended lately without moving the index will end so again, as when
		// it fails on the repository or its .kritika.yaml turns indexing
		// off: one every poll would cost a job and its forge calls each
		// time, and a runner pod for one that fails in its run.
		if !file.Runs(account, r.Traits) || r.IndexedCommit == "" || r.Indexing || r.IndexUnmoved {
			continue
		}
		// The client is the account's, not the repository's: without it
		// no other branch can be looked at either. But a GitHub client is
		// built for the installation that it finds by the repository, and a
		// repository that the App no longer reaches has none until the
		// repository sync disables it, as it has no branch for a client
		// already built.
		client, err := p.Forges.For(ctx, in, r.Name)
		if forgeStatus(err) == http.StatusNotFound {
			p.branchUnknown(in, r, err)
			continue
		}
		built := err == nil
		if built {
			err = p.pollTip(ctx, file, account, in, client, r, started)
		}
		// Past the rate limit, any forge's, every other branch would only be
		// refused too. The limit of the GitHub App, which finds the
		// installation that a client is built for, holds back the account's
		// looks alone, as scope names them: the App's other accounts with a
		// client built look with their installations' tokens, and go-github
		// refuses the App's next discovery until the reset without asking
		// GitHub.
		if wait, limited := rateLimited(err, p.clock()); limited {
			p.Logger.Warn("default branches not polled past the rate limit", "connection", in.Name, "account", account.Key(),
				"repository", r.Name, "error", err)
			p.tipsCut(account.ID(), r.Name)
			p.tipsLimit(scope, account.ID(), p.clock().Add(wait))
			return nil
		}
		if !built {
			p.tipsCut(account.ID(), r.Name)
			return err
		}
		switch {
		case ctx.Err() != nil:
			p.tipsCut(account.ID(), r.Name)
			return nil
		case errors.Is(err, forge.ErrNotFound):
			p.branchUnknown(in, r, err)
		case err != nil:
			p.Logger.Warn("default branch not polled", "connection", in.Name, "repository", r.Name, "error", err)
		}
	}
	p.tipsSwept(account.ID())
	return nil
}

// tipsDue reports whether this look asks for the account's default branches,
// and the repository to start at, "" for the first. It does not look while
// the rate limit of the credential that scope names, as limitScope does, has
// not passed, whichever of its accounts' looks the limit refused. Past it,
// the account whose lookup the limit refused takes the first turn that
// tipsOrder kept for it. It looks when a look was cut short, as the refused
// one was. It also looks when the connection's webhook, which last
// delivered at delivered, has been silent for lookback, and otherwise when
// tuning's Poller.TipCheckInterval has passed since a look last went through
// every repository in this process: a newly elected leader looks at once.
func (p *Poller) tipsDue(accountID, scope string, delivered *time.Time, lookback time.Duration) (string, bool) {
	now := p.clock()
	p.mu.Lock()
	defer p.mu.Unlock()
	if now.Before(p.tipsLimited[scope]) {
		return "", false
	}
	if p.tipsRefused[scope] == accountID {
		delete(p.tipsRefused, scope)
	}
	s := p.tips[accountID]
	if s == nil {
		return "", true
	}
	arriving := delivered != nil && delivered.After(now.Add(-lookback))
	if s.next == "" && arriving && now.Sub(s.swept) < tuning.Get().Poller.TipCheckInterval {
		return "", false
	}
	return s.next, true
}

// tipsCut records that a look at the account's default branches stopped
// before the repository next, which the next look starts at.
func (p *Poller) tipsCut(accountID, next string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tipScan(accountID).next = next
}

// tipsLimit records that the rate limit of the credential that scope names
// refuses every look that uses it until then, and that it refused the
// account's.
func (p *Poller) tipsLimit(scope, accountID string, until time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.tipsLimited == nil {
		p.tipsLimited = map[string]time.Time{}
	}
	if p.tipsRefused == nil {
		p.tipsRefused = map[string]string{}
	}
	p.tipsLimited[scope] = until
	p.tipsRefused[scope] = accountID
}

// limitScope names the credential that the account's branch lookups use,
// which a forge's rate limit counts against. A GitHub App has a token, and a
// limit, for each installation, which is one account's. A Forgejo, Gitea or
// GitLab connection has one client, with its integration's, bot's or
// service account's credential, for every account that it serves.
func limitScope(in *configfile.Connection, account *configfile.Account) string {
	if in.Forge == configfile.ForgeGitHub {
		return in.ID() + ":" + account.ID()
	}
	return in.ID()
}

// tipsSwept records that a look went through every one of the account's
// default branches.
func (p *Poller) tipsSwept(accountID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	*p.tipScan(accountID) = tipScan{swept: p.clock()}
}

// clock is the time on the clock that schedules the looks at the default
// branches.
func (p *Poller) clock() time.Time {
	if p.now != nil {
		return p.now()
	}
	return time.Now()
}

// tipScan returns the account's tipScan, which it creates. p.mu is held.
func (p *Poller) tipScan(accountID string) *tipScan {
	if p.tips == nil {
		p.tips = map[string]*tipScan{}
	}
	s := p.tips[accountID]
	if s == nil {
		s = &tipScan{}
		p.tips[accountID] = s
	}
	return s
}

// pollTip hands the dispatcher a push to r's default branch when its tip is
// not the commit that r's index covers. So the index follows the branch as a
// push webhook makes it do.
func (p *Poller) pollTip(
	ctx context.Context, file *configfile.File, account *configfile.Account, in *configfile.Connection, client forge.Client, r store.PollRepo,
	started time.Time,
) error {
	owner, name, _ := strings.Cut(r.Name, "/")
	// The look waits out a rate limit itself, past its deadline if need be.
	tip, branch, err := client.BranchTip(ctx, owner, name, r.DefaultBranch)
	if err != nil {
		return err
	}
	// A repository marked off in its .kritika.yaml is read again at the
	// indexed commit too: a force push back to it, with no webhook, leaves
	// the mark, and retention drops an index that the file turns on.
	if tip == r.IndexedCommit && !r.RepoConfigOff {
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
