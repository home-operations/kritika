package worker

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/forge"
	"github.com/home-operations/kritika/internal/forge/forgejo"
	"github.com/home-operations/kritika/internal/forge/github"
	"github.com/home-operations/kritika/internal/forge/gitlab"
	"github.com/home-operations/kritika/internal/metrics"
	"github.com/home-operations/kritika/internal/store"
)

// Forges holds one GitHub App, Forgejo, Gitea or GitLab client per connection, built on
// first use and kept for the life of the process, as the configuration
// is, so the clients and the listings of a connection share its
// credentials: an App's installation tokens are minted once an hour each,
// however often the connection is polled.
type Forges struct {
	// Metrics may be nil. Rate limits the forge answers with are logged
	// and counted on it.
	Metrics *metrics.Metrics
	// Issuer is the URL kritika's Forgejo tokens name as their issuer,
	// forgejo.IssuerURL of the dashboard's.
	Issuer string

	mu   sync.Mutex
	apps map[string]*github.App
	// bots are the Forgejo and Gitea clients, which share a package.
	bots map[string]*forgejo.Client
	labs map[string]*gitlab.Client
}

// app is connection in's App.
func (a *Forges) app(in *configfile.Connection) (*github.App, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if app, ok := a.apps[in.Name]; ok {
		return app, nil
	}
	app, err := github.NewApp(in.App.ClientIDValue(), in.App.PrivateKeyValue().Value(), "")
	if err != nil {
		return nil, err
	}
	name := in.Name
	app.OnRateLimit = func(wait time.Duration, waited bool) {
		outcome := "waited"
		if !waited {
			outcome = "refused"
		}
		slog.Warn("github rate limit hit", "connection", name, "wait", wait.Round(time.Second), "outcome", outcome)
		a.Metrics.ForgeRateLimited(name, outcome)
	}
	if a.apps == nil {
		a.apps = map[string]*github.App{}
	}
	a.apps[name] = app
	return app, nil
}

// bot is connection in's Forgejo or Gitea client. The integration, or bot
// user, is the connection's, so one client serves every account it lists.
func (a *Forges) bot(in *configfile.Connection) (*forgejo.Client, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if c, ok := a.bots[in.Name]; ok {
		return c, nil
	}
	c, err := a.newBot(in)
	if err != nil {
		return nil, err
	}
	if a.bots == nil {
		a.bots = map[string]*forgejo.Client{}
	}
	a.bots[in.Name] = c
	return c, nil
}

func (a *Forges) newBot(in *configfile.Connection) (*forgejo.Client, error) {
	if in.Forge == configfile.ForgeGitea {
		return forgejo.NewGiteaClient(in.Gitea.Host, in.Gitea.TokenValue().Value(), in.Gitea.RunnerTokenValue().Value(), nil)
	}
	issuer, err := forgejo.NewIssuer(a.Issuer, in.Forgejo.PrivateKeyValue().Value())
	if err != nil {
		return nil, err
	}
	return forgejo.NewClient(in.Forgejo.Host, issuer, in.Forgejo.AudienceValue(), in.Forgejo.RunnerAudienceValue(), nil)
}

// lab is connection in's GitLab client. The service account is the
// connection's, so one client serves every account it lists.
func (a *Forges) lab(in *configfile.Connection) (*gitlab.Client, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if c, ok := a.labs[in.Name]; ok {
		return c, nil
	}
	c, err := gitlab.NewClient(in.GitLab.Host, in.GitLab.TokenValue().Value(), in.GitLab.RunnerTokenValue().Value(), nil)
	if err != nil {
		return nil, err
	}
	if a.labs == nil {
		a.labs = map[string]*gitlab.Client{}
	}
	a.labs[in.Name] = c
	return c, nil
}

// Build constructs the forge client for a connection, for repositories of
// repo's owner: ForgeCache's Build.
func (a *Forges) Build(ctx context.Context, in *configfile.Connection, repo string) (forge.Client, error) {
	switch in.Forge {
	case configfile.ForgeGitHub:
		app, err := a.app(in)
		if err != nil {
			return nil, err
		}
		// An App is installed, and mints tokens, once per account: the
		// installation that sees repo is its owner's.
		owner, name, _ := strings.Cut(repo, "/")
		id, err := app.DiscoverInstallation(ctx, owner, name)
		if err != nil {
			return nil, err
		}
		return github.NewClient(app, id)
	case configfile.ForgeForgejo, configfile.ForgeGitea:
		return a.bot(in)
	case configfile.ForgeGitLab:
		return a.lab(in)
	default:
		return nil, fmt.Errorf("worker: forge %s is not implemented", in.Forge)
	}
}

// Reach lists the repositories connection in reaches, those its App is
// installed on or its bot can see, by the lowercased login of the account
// each is under, as the store registers them: the poller's Reach.
func (a *Forges) Reach(ctx context.Context, in *configfile.Connection) (map[string][]store.ReachedRepository, error) {
	out := make(map[string][]store.ReachedRepository, len(in.Accounts))
	switch in.Forge {
	case configfile.ForgeGitHub:
		app, err := a.app(in)
		if err != nil {
			return nil, err
		}
		reach, err := app.Reach(ctx, in.Accounts)
		if err != nil {
			return nil, err
		}
		for _, r := range reach {
			repos := make([]store.ReachedRepository, 0, len(r.Repositories))
			for _, repo := range r.Repositories {
				repos = append(repos, reached(repo.FullName, repo.DefaultBranch, repo.Archived, repo.Fork, repo.Visibility))
			}
			out[strings.ToLower(r.Account)] = repos
		}
	case configfile.ForgeForgejo, configfile.ForgeGitea:
		c, err := a.bot(in)
		if err != nil {
			return nil, err
		}
		for _, account := range in.Accounts {
			listed, err := c.Repositories(ctx, account)
			if err != nil {
				return nil, err
			}
			repos := make([]store.ReachedRepository, 0, len(listed))
			for _, repo := range listed {
				repos = append(repos, reached(repo.FullName, repo.DefaultBranch, repo.Archived, repo.Fork, repo.Visibility))
			}
			out[strings.ToLower(account)] = repos
		}
	case configfile.ForgeGitLab:
		c, err := a.lab(in)
		if err != nil {
			return nil, err
		}
		for _, account := range in.Accounts {
			listed, err := c.Repositories(ctx, account)
			if err != nil {
				return nil, err
			}
			repos := make([]store.ReachedRepository, 0, len(listed))
			for _, repo := range listed {
				repos = append(repos, reached(repo.FullName, repo.DefaultBranch, repo.Archived, repo.Fork, repo.Visibility))
			}
			out[strings.ToLower(account)] = repos
		}
	default:
		return nil, fmt.Errorf("worker: forge %s is not implemented", in.Forge)
	}
	return out, nil
}

func reached(fullName, defaultBranch string, archived, fork bool, visibility string) store.ReachedRepository {
	return store.ReachedRepository{
		FullName: fullName, DefaultBranch: defaultBranch,
		Traits: &configfile.RepoTraits{Archived: archived, Fork: fork, Visibility: visibility},
	}
}

// ForgeCache is the forge.Clients over the configured connections. One
// client per connection and repository owner, built on first use and kept
// for the life of the process, as the configuration is.
type ForgeCache struct {
	Build func(ctx context.Context, in *configfile.Connection, repo string) (forge.Client, error)

	mu      sync.Mutex
	clients map[string]forge.Client
	// building shares one build per key among the callers that find none
	// cached, outside mu: a build asks the forge, and a slow answer must
	// not hold up the callers of every other connection and owner.
	building singleflight.Group
}

// forgeBuildTimeout bounds a client's build, which runs on no caller's ctx.
const forgeBuildTimeout = time.Minute

// For implements forge.Clients. A caller whose ctx ends while a build is
// under way returns with ctx's error; the build goes on for the others,
// whichever caller started it.
func (c *ForgeCache) For(ctx context.Context, in *configfile.Connection, repo string) (forge.Client, error) {
	owner, _, _ := strings.Cut(repo, "/")
	key := in.Name + "/" + strings.ToLower(owner)
	c.mu.Lock()
	client, ok := c.clients[key]
	c.mu.Unlock()
	if ok {
		return client, nil
	}
	results := c.building.DoChan(key, func() (any, error) {
		// A caller that missed the cache may only get here once the build
		// it would have joined is over and forgotten: it takes that
		// build's client instead of building another.
		c.mu.Lock()
		client, ok := c.clients[key]
		c.mu.Unlock()
		if ok {
			return client, nil
		}
		bctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), forgeBuildTimeout)
		defer cancel()
		client, err := c.Build(bctx, in, repo)
		if err != nil {
			return nil, err
		}
		c.mu.Lock()
		if c.clients == nil {
			c.clients = map[string]forge.Client{}
		}
		c.clients[key] = client
		c.mu.Unlock()
		return client, nil
	})
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("worker: forge client for %s: %w", key, ctx.Err())
	case res := <-results:
		if res.Err != nil {
			return nil, res.Err
		}
		client, _ := res.Val.(forge.Client)
		return client, nil
	}
}

// PullRequestAuthor reads who opened pull request number of repo, and
// whether the forge marks them a bot: ingest's Authors.
func (c *ForgeCache) PullRequestAuthor(ctx context.Context, in *configfile.Connection, repo string, number int) (string, bool, error) {
	client, err := c.For(ctx, in, repo)
	if err != nil {
		return "", false, err
	}
	owner, name, _ := strings.Cut(repo, "/")
	pr, err := client.PullRequest(ctx, owner, name, number)
	if err != nil {
		return "", false, err
	}
	return pr.Author, pr.AuthorIsBot, nil
}
