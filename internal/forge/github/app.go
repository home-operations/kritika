// Package github is kritika's forge. It holds the App's credentials and a
// transport that signs requests as the App with a short-lived JWT. It also
// holds a transport that mints, caches, and refreshes an installation token,
// the forge.Client over an installation, and the App manifest flow. The
// installation token authenticates API calls and git fetches alike, so an
// App-only installation needs no personal token.
package github

import (
	"cmp"
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	gh "github.com/google/go-github/v93/github"

	"github.com/home-operations/kritika/internal/tuning"
)

// App is a GitHub App identity: the client id and private key of one App.
type App struct {
	clientID string
	apiBase  string // "" for api.github.com
	origin   *url.URL
	apps     *gh.Client
	// base is under the authentication of every client of the App.
	base http.RoundTripper

	// OnRateLimit, when set, is told of every response GitHub refused for a
	// rate limit: the wait it asked for, and whether the request waited it
	// out and was sent again. Set it before the App is used.
	OnRateLimit func(wait time.Duration, waited bool)

	// tokens holds one InstallationTokens per installation, so every
	// client and listing of an installation shares its token instead of
	// minting one each.
	mu     sync.Mutex
	tokens map[int64]*InstallationTokens
}

// NewApp parses the App's private key and builds the App-level client used
// for installation lookup and token minting. apiBase is empty for
// api.github.com. Tests point it at a server of their own.
func NewApp(clientID, privateKeyPEM, apiBase string) (*App, error) {
	return newApp(clientID, privateKeyPEM, apiBase, baseTransport())
}

// newApp is NewApp over base, which tests replace.
func newApp(clientID, privateKeyPEM, apiBase string, base http.RoundTripper) (*App, error) {
	key, err := parseRSAKey(privateKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("github: parse App private key: %w", err)
	}
	origin, err := url.Parse(cmp.Or(apiBase, tuning.GitHubAPIURL))
	if err != nil {
		return nil, fmt.Errorf("github: parse API origin: %w", err)
	}
	a := &App{clientID: clientID, apiBase: apiBase, origin: origin, base: base}
	client, err := newClient(a.limit(&appJWTTransport{base: base, origin: origin, clientID: clientID, key: key}), apiBase)
	if err != nil {
		return nil, err
	}
	a.apps = client
	return a, nil
}

// baseTransport is under every App's clients: the default transport, which
// bounds the dial and the TLS handshake, with the wait for response headers
// bounded too, by tuning's GitHub.ResponseHeaderTimeout as it is on first
// use. One transport, so every App shares its connection pool.
var baseTransport = sync.OnceValue(func() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.ResponseHeaderTimeout = tuning.Get().GitHub.ResponseHeaderTimeout
	return t
})

// limit puts the wait for a rate limit that resets soon above rt, a client's
// authentication. So a request sent again after the wait is authorized again,
// with a token refreshed while it waited. Each answer's error body is bounded
// below the wait, so a request sent again has a bound of its own.
func (a *App) limit(rt http.RoundTripper) http.RoundTripper {
	return &rateLimitTransport{base: &errorBodyTransport{base: rt}, maxWait: tuning.Get().GitHub.RateLimitMaxWait, observe: a.rateLimited}
}

// errErrorBodyCut is what reading an error body fails with past tuning's
// GitHub.ErrorBodyWait.
var errErrorBodyCut = errors.New("github: error body not read in time")

// errorBodyTransport cuts off the body of an answer that is no success, or is
// a 202, tuning's GitHub.ErrorBodyWait after its headers arrive, whatever the
// request's context.
type errorBodyTransport struct {
	base http.RoundTripper
	// after overrides time.AfterFunc in tests.
	after func(d time.Duration, f func()) (stop func() bool)
}

func (t *errorBodyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Cancelling the request's own context is what stops a read in progress.
	ctx, cancel := context.WithCancel(req.Context())
	resp, err := t.base.RoundTrip(req.WithContext(ctx))
	if err != nil {
		cancel()
		return nil, err
	}
	body := &errorBody{ReadCloser: resp.Body, cancel: cancel}
	if resp.StatusCode < 200 || resp.StatusCode > 299 || resp.StatusCode == http.StatusAccepted {
		after := t.after
		if after == nil {
			after = func(d time.Duration, f func()) func() bool { return time.AfterFunc(d, f).Stop }
		}
		body.stop = after(tuning.Get().GitHub.ErrorBodyWait, func() {
			body.cut.Store(true)
			cancel()
		})
	}
	resp.Body = body
	return resp, nil
}

// errorBody is an answer's body whose request's context ends as it is
// closed, or, with stop set, when its bound cuts it off first.
type errorBody struct {
	io.ReadCloser
	cancel context.CancelFunc
	stop   func() bool
	cut    atomic.Bool
}

func (b *errorBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil && !errors.Is(err, io.EOF) && b.cut.Load() {
		err = errErrorBodyCut
	}
	return n, err
}

func (b *errorBody) Close() error {
	if b.stop != nil {
		b.stop()
	}
	err := b.ReadCloser.Close()
	b.cancel()
	return err
}

func (a *App) rateLimited(wait time.Duration, waited bool) {
	if a.OnRateLimit != nil {
		a.OnRateLimit(wait, waited)
	}
}

// Slug returns the App's URL slug, which names the bot user its comments
// are posted as.
func (a *App) Slug(ctx context.Context) (string, error) {
	app, _, err := a.apps.Apps.Get(ctx, "")
	if err != nil {
		return "", fmt.Errorf("github: read App: %w", err)
	}
	if app.GetSlug() == "" {
		return "", fmt.Errorf("github: App %s has no slug", a.clientID)
	}
	return app.GetSlug(), nil
}

// InstallationTokens returns the token source for one installation, as
// DiscoverInstallation finds it: the same one on every call, so the token
// is minted once an hour however often the installation is used.
func (a *App) InstallationTokens(installationID int64) *InstallationTokens {
	a.mu.Lock()
	defer a.mu.Unlock()
	if t, ok := a.tokens[installationID]; ok {
		return t
	}
	if a.tokens == nil {
		a.tokens = map[int64]*InstallationTokens{}
	}
	t := &InstallationTokens{apps: a.apps, instID: installationID}
	a.tokens[installationID] = t
	return t
}

// DiscoverInstallation returns the id of the App's installation on the
// account that owns the repository.
func (a *App) DiscoverInstallation(ctx context.Context, owner, repo string) (int64, error) {
	inst, _, err := a.apps.Apps.GetRepositoryInstallation(ctx, owner, repo)
	if err != nil {
		return 0, fmt.Errorf("github: find App installation for %s/%s: %w", owner, repo, err)
	}
	return inst.GetID(), nil
}

// Installation is one account the App is installed on.
type Installation struct {
	ID int64
	// Account is the login of the account, AccountType "User" or
	// "Organization".
	Account, AccountType string
	// AllRepositories is whether it covers every repository of the account
	// rather than those selected.
	AllRepositories bool
	Suspended       bool
	// HTMLURL is the installation's settings page on GitHub.
	HTMLURL string
}

// Installations lists every account the App is installed on.
func (a *App) Installations(ctx context.Context) ([]Installation, error) {
	var out []Installation
	for inst, err := range a.apps.Apps.ListInstallationsIter(ctx, &gh.ListOptions{PerPage: gitHubMaxPage}) {
		if err != nil {
			return nil, fmt.Errorf("github: list App installations: %w", err)
		}
		out = append(out, Installation{
			ID: inst.GetID(), Account: inst.GetAccount().GetLogin(), AccountType: inst.GetAccount().GetType(),
			AllRepositories: inst.GetRepositorySelection() == "all", Suspended: inst.SuspendedAt != nil, HTMLURL: inst.GetHTMLURL(),
		})
	}
	return out, nil
}

// Repository is one repository an installation reaches.
type Repository struct {
	// Name is the repository's name, FullName "owner/name".
	Name, FullName, DefaultBranch string
	Archived, Fork                bool
	// Visibility is public, private or internal, as GitHub reports it.
	Visibility string
}

// Repositories lists every repository installation id reaches, with a
// token of that installation.
func (a *App) Repositories(ctx context.Context, id int64) ([]Repository, error) {
	client, err := a.Client(a.InstallationTokens(id))
	if err != nil {
		return nil, err
	}
	var out []Repository
	for r, err := range client.Apps.ListReposIter(ctx, &gh.ListOptions{PerPage: gitHubMaxPage}) {
		if err != nil {
			return nil, fmt.Errorf("github: list installation %d repositories: %w", id, err)
		}
		out = append(out, Repository{
			Name: r.GetName(), FullName: r.GetFullName(), DefaultBranch: r.GetDefaultBranch(),
			Archived: r.GetArchived(), Fork: r.GetFork(), Visibility: r.GetVisibility(),
		})
	}
	return out, nil
}

// AccountRepositories is what an App reaches on one account: whether it
// is installed there, and the repositories its installation reaches.
type AccountRepositories struct {
	Account      string
	Installed    bool
	Repositories []Repository
}

// Reach lists, for each of accounts in order, whether the App is installed
// there and the repositories it reaches. A suspended installation reaches
// none: it can mint no token to list them with.
func (a *App) Reach(ctx context.Context, accounts []string) ([]AccountRepositories, error) {
	insts, err := a.Installations(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]AccountRepositories, 0, len(accounts))
	for _, account := range accounts {
		entry := AccountRepositories{Account: account, Repositories: []Repository{}}
		for _, inst := range insts {
			if inst.Suspended || !strings.EqualFold(inst.Account, account) {
				continue
			}
			repos, err := a.Repositories(ctx, inst.ID)
			if err != nil {
				return nil, err
			}
			entry.Installed = true
			entry.Repositories = append(entry.Repositories, repos...)
		}
		out = append(out, entry)
	}
	return out, nil
}

// Uninstall removes the App's installation id.
func (a *App) Uninstall(ctx context.Context, id int64) error {
	if _, err := a.apps.Apps.DeleteInstallation(ctx, id); err != nil {
		return fmt.Errorf("github: uninstall App installation %d: %w", id, err)
	}
	return nil
}

// Client returns a go-github client authenticated as the installation.
func (a *App) Client(tokens *InstallationTokens) (*gh.Client, error) {
	return newClient(a.limit(&installTransport{base: a.base, origin: a.origin, tokens: tokens}), a.apiBase)
}

// ErrRedirectRefused is what a GitHub client refuses to follow a redirect
// with: one off the API's origin, or past the tenth. Go wraps it in a
// *url.Error, which is no failed connection to retry.
var ErrRedirectRefused = errors.New("github: redirect refused")

// newClient answers a redirect off the API's origin with an error rather than
// following it: the transports withhold credentials from such a hop, but its
// answer would still be read as GitHub's.
func newClient(rt http.RoundTripper, apiBase string) (*gh.Client, error) {
	origin, err := url.Parse(cmp.Or(apiBase, tuning.GitHubAPIURL))
	if err != nil {
		return nil, fmt.Errorf("github: parse API origin: %w", err)
	}
	redirects := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if !sameOrigin(req.URL, origin) {
			return fmt.Errorf("%w: off %s to %s", ErrRedirectRefused, origin.Host, req.URL.Host)
		}
		if limit := tuning.Get().GitHub.MaxRedirects; len(via) >= limit {
			return fmt.Errorf("%w: stopped after %d redirects", ErrRedirectRefused, limit)
		}
		return nil
	}}
	opts := []gh.ClientOptionsFunc{gh.WithHTTPClient(redirects), gh.WithTransport(rt)}
	if apiBase != "" {
		opts = append(opts, gh.WithEnterpriseURLs(apiBase, apiBase))
	}
	c, err := gh.NewClient(opts...)
	if err != nil {
		return nil, fmt.Errorf("github: client: %w", err)
	}
	return c, nil
}

// appJWT mints a short-lived App JWT with the client id as issuer, which
// lives for tuning's GitHub.AppJWTLifetime, under GitHub's cap. Its iat,
// backdated by GitHub.AppJWTBackdate, absorbs minor clock skew.
func appJWT(clientID string, key *rsa.PrivateKey, now time.Time) (string, error) {
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithType("JWT"))
	if err != nil {
		return "", err
	}
	return jwt.Signed(signer).Claims(jwt.Claims{
		Issuer:   clientID,
		IssuedAt: jwt.NewNumericDate(now.Add(-tuning.Get().GitHub.AppJWTBackdate)),
		Expiry:   jwt.NewNumericDate(now.Add(tuning.Get().GitHub.AppJWTLifetime)),
	}).Serialize()
}

// parseRSAKey reads a PEM RSA private key, PKCS #1 as GitHub issues one or
// PKCS #8.
func parseRSAKey(privateKeyPEM string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(privateKeyPEM))
	if block == nil {
		return nil, errors.New("no PEM block")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	rsaKey, ok := key.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("a %T, not an RSA key", key)
	}
	return rsaKey, nil
}

// appJWTTransport signs each request as the App itself. It regenerates the
// JWT as it nears expiry.
type appJWTTransport struct {
	base     http.RoundTripper
	origin   *url.URL
	clientID string
	key      *rsa.PrivateKey

	mu  sync.Mutex
	tok string
	exp time.Time
}

func (t *appJWTTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	r.Header.Del("Authorization")
	if !sameOrigin(req.URL, t.origin) {
		return t.base.RoundTrip(r)
	}
	tok, err := t.jwt()
	if err != nil {
		return nil, err
	}
	r.Header.Set("Authorization", tuning.BearerPrefix+tok)
	return t.base.RoundTrip(r)
}

func (t *appJWTTransport) jwt() (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if time.Until(t.exp) > tuning.Get().GitHub.TokenRefreshLead {
		return t.tok, nil
	}
	now := time.Now()
	tok, err := appJWT(t.clientID, t.key, now)
	if err != nil {
		return "", fmt.Errorf("github: sign App JWT: %w", err)
	}
	t.tok, t.exp = tok, now.Add(tuning.Get().GitHub.AppJWTLifetime)
	return tok, nil
}

// InstallationTokens mints an installation token on first use, caches it,
// and refreshes it before expiry. One instance per installation.
type InstallationTokens struct {
	apps   *gh.Client
	instID int64

	mu  sync.Mutex
	tok string
	exp time.Time
	// contentsReadOnly is whether the cached token was minted without
	// write access to repository contents.
	contentsReadOnly bool
}

// Token returns a valid installation token. It mints one if needed.
func (t *InstallationTokens) Token(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if time.Until(t.exp) > tuning.Get().GitHub.TokenRefreshLead {
		return t.tok, nil
	}
	it, _, err := t.apps.Apps.CreateInstallationToken(ctx, t.instID, nil)
	if err != nil {
		return "", fmt.Errorf("github: mint installation token for %d: %w", t.instID, err)
	}
	t.tok, t.exp = it.GetToken(), it.GetExpiresAt().Time
	t.contentsReadOnly = it.Permissions != nil && it.Permissions.GetContents() != "write"
	return t.tok, nil
}

// CanWriteContents reports whether the installation was granted write access
// to repository contents. A token that GitHub minted without saying what it
// can do counts as one that can.
func (t *InstallationTokens) CanWriteContents(ctx context.Context) (bool, error) {
	if _, err := t.Token(ctx); err != nil {
		return false, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return !t.contentsReadOnly, nil
}

// ReadOnly mints a token that can only read repo's contents and metadata,
// uncached, for a runner: the commands an agent runs there can read it, so
// it must not carry the App's other permissions or reach its other
// repositories.
func (t *InstallationTokens) ReadOnly(ctx context.Context, repo string) (string, error) {
	it, _, err := t.apps.Apps.CreateInstallationToken(ctx, t.instID, &gh.InstallationTokenOptions{
		Repositories: []string{repo},
		Permissions:  &gh.InstallationPermissions{Contents: new("read"), Metadata: new("read")},
	})
	if err != nil {
		return "", fmt.Errorf("github: mint a read-only token for %s on installation %d: %w", repo, t.instID, err)
	}
	return it.GetToken(), nil
}

// installTransport injects the installation token as the bearer.
type installTransport struct {
	base   http.RoundTripper
	origin *url.URL
	tokens *InstallationTokens
}

func (t *installTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	r.Header.Del("Authorization")
	if !sameOrigin(req.URL, t.origin) {
		return t.base.RoundTrip(r)
	}
	tok, err := t.tokens.Token(req.Context())
	if err != nil {
		return nil, err
	}
	r.Header.Set("Authorization", tuning.BearerPrefix+tok)
	return t.base.RoundTrip(r)
}

// Redirects can retain Authorization across ports or subdomains, so a
// hostname alone cannot determine whether credentials can be sent.
func sameOrigin(u, origin *url.URL) bool {
	return origin != nil && strings.EqualFold(u.Scheme, origin.Scheme) &&
		strings.EqualFold(u.Hostname(), origin.Hostname()) && originPort(u) == originPort(origin)
}

func originPort(u *url.URL) string {
	if port := u.Port(); port != "" {
		return port
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return "443"
	case "http":
		return "80"
	default:
		return ""
	}
}
