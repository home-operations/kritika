package configfile

import (
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strings"
)

// ForgejoIntegration is the authorized integration a Forgejo connection
// calls the instance as: the instance's host, the key kritika signs the
// integration's tokens with, and the audience Forgejo gave the
// integration, which is known only once it exists; until it is set the
// connection serves its key and nothing else. RunnerAudience names a
// second integration with read access alone that runners fetch as, so
// the pod that reads untrusted content never holds a token that can
// write; without one they fetch as the first. PreviousKey is a key being
// rotated out: served for Forgejo to verify with, signing nothing, until
// the last token it signed has expired and Forgejo's cache of the key set
// has turned over.
type ForgejoIntegration struct {
	Host           string     `yaml:"host"`
	Audience       ValueOrRef `yaml:"audience,omitempty"`
	RunnerAudience ValueOrRef `yaml:"runnerAudience,omitempty"`
	PrivateKey     SecretRef  `yaml:"privateKey"`
	PreviousKey    SecretRef  `yaml:"previousKey,omitempty"`
	WebhookSecret  SecretRef  `yaml:"webhookSecret"`

	audience, runnerAudience               string
	privateKey, previousKey, webhookSecret Secret
}

// AudienceValue returns the integration's audience, inline or resolved,
// "" until the configuration names one.
func (b ForgejoIntegration) AudienceValue() string { return b.audience }

// RunnerAudienceValue returns the runners' integration's audience, "" for
// none.
func (b ForgejoIntegration) RunnerAudienceValue() string { return b.runnerAudience }

// PrivateKeyValue returns the resolved private key PEM.
func (b ForgejoIntegration) PrivateKeyValue() Secret { return b.privateKey }

// PreviousKeyValue returns the resolved PEM of the key being rotated out,
// empty for none.
func (b ForgejoIntegration) PreviousKeyValue() Secret { return b.previousKey }

// WebhookSecretValue returns the resolved webhook secret.
func (i *Connection) WebhookSecretValue() Secret {
	switch i.Forge {
	case ForgeForgejo:
		return i.Forgejo.webhookSecret
	case ForgeGitLab:
		return i.GitLab.webhookSecret
	case ForgeGitea:
		return i.Gitea.webhookSecret
	}
	return i.App.webhookSecret
}

// GitLabBot is the service account a GitLab connection calls the instance
// as: the instance's host, a personal access token of the account with
// the api scope, and RunnerToken, a second token of the account with the
// read_repository scope alone that runners fetch with, so the pod that
// reads untrusted content never holds a token that can write; without
// one they fetch with the first. GitLab 19 or later is assumed.
type GitLabBot struct {
	Host          string    `yaml:"host"`
	Token         SecretRef `yaml:"token"`
	RunnerToken   SecretRef `yaml:"runnerToken,omitempty"`
	WebhookSecret SecretRef `yaml:"webhookSecret"`

	token, runnerToken, webhookSecret Secret
}

// TokenValue returns the resolved API token.
func (b GitLabBot) TokenValue() Secret { return b.token }

// RunnerTokenValue returns the resolved runners' token, empty for none.
func (b GitLabBot) RunnerTokenValue() Secret { return b.runnerToken }

// GiteaBot is the bot user a Gitea connection calls the instance as: the
// instance's host, an access token of the user with the read:user,
// write:repository and write:issue scopes, and RunnerToken, a second
// token of the user with read:repository alone that runners fetch with,
// so the pod that reads untrusted content never holds a token that can
// write; without one they fetch with the first. Gitea 28 or later is
// assumed.
type GiteaBot struct {
	Host          string    `yaml:"host"`
	Token         SecretRef `yaml:"token"`
	RunnerToken   SecretRef `yaml:"runnerToken,omitempty"`
	WebhookSecret SecretRef `yaml:"webhookSecret"`

	token, runnerToken, webhookSecret Secret
}

// TokenValue returns the resolved API token.
func (b GiteaBot) TokenValue() Secret { return b.token }

// RunnerTokenValue returns the resolved runners' token, empty for none.
func (b GiteaBot) RunnerTokenValue() Secret { return b.runnerToken }

// readTokens reads the secrets a GitLab or Gitea bot holds: its token,
// its runners' token when one is given, and its webhook secret.
func readTokens(where string, s *secrets, token, runnerToken, webhookSecret SecretRef) (Secret, Secret, Secret, error) {
	var tok, runner, hook Secret
	var err error
	if tok, err = s.read(token); err != nil {
		return tok, runner, hook, fmt.Errorf("configfile: %s.token: %w", where, err)
	}
	if !runnerToken.empty() {
		if runner, err = s.read(runnerToken); err != nil {
			return tok, runner, hook, fmt.Errorf("configfile: %s.runnerToken: %w", where, err)
		}
	}
	if hook, err = s.read(webhookSecret); err != nil {
		return tok, runner, hook, fmt.Errorf("configfile: %s.webhookSecret: %w", where, err)
	}
	return tok, runner, hook, nil
}

func (b *GiteaBot) validate(where string) error {
	if b.Host == "" {
		return fmt.Errorf("configfile: %s.host is required", where)
	}
	if err := checkHost(b.Host); err != nil || strings.HasPrefix(b.Host, "*") {
		return fmt.Errorf("configfile: %s.host: %q must be a lowercase hostname, without a scheme, port or path", where, b.Host)
	}
	if b.token.Value() == "" {
		return fmt.Errorf("configfile: %s.token is required", where)
	}
	if b.webhookSecret.Value() == "" {
		return fmt.Errorf("configfile: %s.webhookSecret is required", where)
	}
	return nil
}

// where names the connection's entry in errors.
func (i *Connection) where() string {
	switch i.Forge {
	case ForgeForgejo:
		return "forgejo." + i.Name
	case ForgeGitLab:
		return "gitlab." + i.Name
	case ForgeGitea:
		return "gitea." + i.Name
	}
	return "apps." + i.Name
}

// ForgeHosts are the hosts the connection's forge is reached at, which
// every runner of its accounts may reach: GitHub and its API, or the
// Forgejo, GitLab or Gitea instance, whose API and clone URLs share its
// host.
func (i *Connection) ForgeHosts() []string {
	switch i.Forge {
	case ForgeForgejo:
		return []string{i.Forgejo.Host}
	case ForgeGitLab:
		return []string{i.GitLab.Host}
	case ForgeGitea:
		return []string{i.Gitea.Host}
	}
	return []string{GitHubHost, GitHubAPIHost}
}

// forgejoDoc is an entry of the file's forgejo map: the accounts served
// and the integration that serves them.
type forgejoDoc struct {
	Accounts           []string `yaml:"accounts"`
	ForgejoIntegration `yaml:",inline"`
}

// gitlabDoc is an entry of the file's gitlab map: the accounts served and
// the service account that serves them.
type gitlabDoc struct {
	Accounts  []string `yaml:"accounts"`
	GitLabBot `yaml:",inline"`
}

// giteaDoc is an entry of the file's gitea map: the accounts served and
// the bot user that serves them.
type giteaDoc struct {
	Accounts []string `yaml:"accounts"`
	GiteaBot `yaml:",inline"`
}

// connectionsOf lists the file's apps, forgejo, gitlab and gitea entries
// in the order of their names, each carrying its key as its name. A name
// in two maps is an error: the name is the hook path, so it names one
// connection.
func connectionsOf(
	apps map[string]Connection, bots map[string]forgejoDoc, gitlabs map[string]gitlabDoc, giteas map[string]giteaDoc,
) ([]Connection, error) {
	byName := make(map[string]Connection, len(apps)+len(bots)+len(gitlabs)+len(giteas))
	for name, in := range apps {
		in.Name, in.Forge = name, ForgeGitHub
		byName[name] = in
	}
	for name, doc := range bots {
		if other, dup := byName[name]; dup {
			return nil, fmt.Errorf("configfile: forgejo.%s: %s has the name already; a name is one connection's", name, other.where())
		}
		byName[name] = Connection{Name: name, Forge: ForgeForgejo, Accounts: doc.Accounts, Forgejo: doc.ForgejoIntegration}
	}
	for name, doc := range gitlabs {
		if other, dup := byName[name]; dup {
			return nil, fmt.Errorf("configfile: gitlab.%s: %s has the name already; a name is one connection's", name, other.where())
		}
		byName[name] = Connection{Name: name, Forge: ForgeGitLab, Accounts: doc.Accounts, GitLab: doc.GitLabBot}
	}
	for name, doc := range giteas {
		if other, dup := byName[name]; dup {
			return nil, fmt.Errorf("configfile: gitea.%s: %s has the name already; a name is one connection's", name, other.where())
		}
		byName[name] = Connection{Name: name, Forge: ForgeGitea, Accounts: doc.Accounts, Gitea: doc.GiteaBot}
	}
	conns := make([]Connection, 0, len(byName))
	for _, name := range slices.Sorted(maps.Keys(byName)) {
		conns = append(conns, byName[name])
	}
	return conns, nil
}

// forgesServing maps each account name conns serve, lowercased, to the
// forges serving it, so an account entry can be given the forge of its
// connection.
func forgesServing(conns []Connection) map[string][]Forge {
	out := map[string][]Forge{}
	for _, in := range conns {
		for _, a := range in.Accounts {
			key := strings.ToLower(a)
			if !slices.Contains(out[key], in.Forge) {
				out[key] = append(out[key], in.Forge)
			}
		}
	}
	return out
}

// validateConnections checks one layer's connections: names that are hook
// paths; accounts listed; credentials that resolved; and every account
// served by one connection alone.
func validateConnections(conns []Connection) error {
	served := map[string]string{}
	for i := range conns {
		in := &conns[i]
		where := in.where()
		if !nameRe.MatchString(in.Name) {
			return fmt.Errorf("configfile: %s: a connection's name must be lowercase alphanumerics and hyphens, 1 to 63 characters", where)
		}
		if err := validateAccountNames(in.Accounts, where); err != nil {
			return err
		}
		if err := in.validate(where); err != nil {
			return err
		}
		for ai, a := range in.Accounts {
			key := AccountKey(in.Forge, a)
			if other, dup := served[key]; dup {
				return fmt.Errorf("configfile: %s.accounts[%d] %q is served by %q already; an account is served by one connection",
					where, ai, a, other)
			}
			served[key] = in.Name
		}
	}
	return nil
}

// validateIssuer requires the dashboard's URL, which kritika's Forgejo
// tokens name as their issuer and Forgejo reads the signing keys from, to
// be HTTPS once a Forgejo connection exists: Forgejo fetches an issuer's
// documents over HTTPS alone. A file parsed without the settings has no
// URL to check.
func (f *File) validateIssuer() error {
	if f.Run.WebURL == "" {
		return nil
	}
	if u, err := url.Parse(f.Run.WebURL); err == nil && strings.EqualFold(u.Scheme, "https") {
		return nil
	}
	for i := range f.Connections {
		if f.Connections[i].Forge == ForgeForgejo {
			return fmt.Errorf("configfile: %s: webUrl must be https for Forgejo to verify kritika's tokens, got %q",
				f.Connections[i].where(), f.Run.WebURL)
		}
	}
	return nil
}
