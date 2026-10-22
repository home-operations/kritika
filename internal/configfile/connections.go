package configfile

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

// ForgejoBot is the bot account a Forgejo connection posts as: the
// instance's host and the account's tokens. Token is what kritika calls
// the API with. GitToken, when set, is what a runner fetches with instead,
// so the pod that reads untrusted content never holds a token that can
// write to the forge; without one the runner fetches with Token.
type ForgejoBot struct {
	Host          string    `yaml:"host"`
	Token         SecretRef `yaml:"token"`
	GitToken      SecretRef `yaml:"gitToken,omitempty"`
	WebhookSecret SecretRef `yaml:"webhookSecret"`

	token, gitToken, webhookSecret Secret
}

// TokenValue returns the resolved API token.
func (b ForgejoBot) TokenValue() Secret { return b.token }

// GitTokenValue returns the token a runner fetches with: the read-only
// one when set, else the API token.
func (b ForgejoBot) GitTokenValue() Secret {
	if b.gitToken.Value() != "" {
		return b.gitToken
	}
	return b.token
}

// WebhookSecretValue returns the resolved webhook secret.
func (i *Connection) WebhookSecretValue() Secret {
	if i.Forge == ForgeForgejo {
		return i.Forgejo.webhookSecret
	}
	return i.App.webhookSecret
}

// where names the connection's entry in errors.
func (i *Connection) where() string {
	if i.Forge == ForgeForgejo {
		return "forgejo." + i.Name
	}
	return "apps." + i.Name
}

// ForgeHosts are the hosts the connection's forge is reached at, which
// every runner of its accounts may reach: GitHub and its API, or the
// Forgejo instance, whose API and clone URLs share its host.
func (i *Connection) ForgeHosts() []string {
	if i.Forge == ForgeForgejo {
		return []string{i.Forgejo.Host}
	}
	return []string{GitHubHost, GitHubAPIHost}
}

// forgejoDoc is an entry of the file's forgejo map: the accounts served
// and the bot that serves them.
type forgejoDoc struct {
	Accounts   []string `yaml:"accounts"`
	ForgejoBot `yaml:",inline"`
}

// connectionsOf lists the file's apps and forgejo entries in the order of
// their names, each carrying its key as its name. A name in both maps is
// an error: the name is the hook path, so it names one connection.
func connectionsOf(apps map[string]Connection, bots map[string]forgejoDoc) ([]Connection, error) {
	byName := make(map[string]Connection, len(apps)+len(bots))
	for name, in := range apps {
		in.Name, in.Forge = name, ForgeGitHub
		byName[name] = in
	}
	for name, doc := range bots {
		if _, dup := byName[name]; dup {
			return nil, fmt.Errorf("configfile: forgejo.%s: apps.%s has the name already; a name is one connection's", name, name)
		}
		byName[name] = Connection{Name: name, Forge: ForgeForgejo, Accounts: doc.Accounts, Forgejo: doc.ForgejoBot}
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
