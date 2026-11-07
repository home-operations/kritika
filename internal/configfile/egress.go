package configfile

import (
	"cmp"
	"fmt"
	"maps"
	"net/netip"
	"slices"
	"strings"

	"github.com/home-operations/kritika/internal/egresspolicy"
)

// GitHubHost is the forge a GitHub App and GitHub sign-in talk to, and
// GitHubAPIHost its API, which a runner's gh calls.
const (
	GitHubHost    = "github.com"
	GitHubAPIHost = "api.github.com"
)

// EgressCredential is one host's credential. Scheme is how the gateway
// applies it, egresspolicy.SchemeBearer when it is empty. Username goes
// with the token for the basic and oci schemes. Accounts are the names of
// the accounts whose runs get it, or "*" alone for every account: what a
// review reads with it, it can publish on a pull request.
type EgressCredential struct {
	Scheme   string    `yaml:"scheme,omitempty"`
	Username string    `yaml:"username,omitempty"`
	Token    SecretRef `yaml:"token"`
	Accounts []string  `yaml:"accounts"`
}

// forgeHosts are the hosts the connections' forges are reached at, each
// once, in the connections' order.
func (f *File) forgeHosts() []string {
	var hosts []string
	for i := range f.Connections {
		for _, h := range f.Connections[i].ForgeHosts() {
			if !slices.Contains(hosts, h) {
				hosts = append(hosts, h)
			}
		}
	}
	return hosts
}

// validateEgress parses the allow and deny entries. It makes sure that no
// deny entry cuts off the forge that a connection needs. It also makes sure
// that each credential names one host that is allowed, explicitly or
// implicitly. The gateway looks a credential up by exact host, so a pattern
// will never apply. Last, it makes the credentials that the gateway adds.
func (f *File) validateEgress() error {
	var err error
	if f.Egress.allow, err = parseList(f.Egress.Allow); err != nil {
		return fmt.Errorf("configfile: egress.allow%w", err)
	}
	if f.Egress.deny, err = parseList(f.Egress.Deny); err != nil {
		return fmt.Errorf("configfile: egress.deny%w", err)
	}
	rules := f.EgressRules()
	for _, host := range f.forgeHosts() {
		if !rules.Allows(host) {
			return fmt.Errorf("configfile: egress.deny: %s is denied, which a connection needs", host)
		}
	}
	f.Egress.credentials = make(map[string]egresspolicy.Credential, len(f.Egress.Credentials))
	for _, host := range slices.Sorted(maps.Keys(f.Egress.Credentials)) {
		if err := checkHost(host); err != nil {
			return fmt.Errorf("configfile: egress.credentials.%s: %w", host, err)
		}
		if strings.HasPrefix(host, "*") {
			return fmt.Errorf("configfile: egress.credentials.%s: a credential names one host, not a pattern", host)
		}
		if !rules.Allows(host) {
			return fmt.Errorf("configfile: egress.credentials.%s: host is not allowed by egress.allow and egress.deny", host)
		}
		cred, err := f.egressCredential(host)
		if err != nil {
			return err
		}
		f.Egress.credentials[host] = cred
	}
	return nil
}

// egressCredential is the credential that the file gives host, with the
// ids of the accounts whose runs get it.
func (f *File) egressCredential(host string) (egresspolicy.Credential, error) {
	where := "egress.credentials." + host
	spec := f.Egress.Credentials[host]
	cred := egresspolicy.Credential{
		Scheme: cmp.Or(spec.Scheme, egresspolicy.SchemeBearer), Username: spec.Username, Secret: f.Egress.tokens[host].Value(),
	}
	switch cred.Scheme {
	case egresspolicy.SchemeBearer:
		if cred.Username != "" {
			return cred, fmt.Errorf("configfile: %s.username: a bearer credential has no username", where)
		}
	case egresspolicy.SchemeBasic, egresspolicy.SchemeOCI:
		if cred.Username == "" {
			return cred, fmt.Errorf("configfile: %s.username: the %s scheme needs one", where, cred.Scheme)
		}
	default:
		return cred, fmt.Errorf("configfile: %s.scheme %q must be bearer, basic or oci", where, cred.Scheme)
	}
	if cred.Secret == "" {
		return cred, fmt.Errorf("configfile: %s.token resolved to an empty value", where)
	}
	if slices.Contains(spec.Accounts, egresspolicy.AllAccounts) {
		if len(spec.Accounts) > 1 {
			return cred, fmt.Errorf(`configfile: %s.accounts: "*" gives the credential to every account, so it stands alone`, where)
		}
		cred.Accounts = []string{egresspolicy.AllAccounts}
		return cred, nil
	}
	if err := validateAccountNames(spec.Accounts, where); err != nil {
		return cred, err
	}
	forges := forgesServing(f.Connections)
	for i, name := range spec.Accounts {
		switch served := forges[strings.ToLower(name)]; len(served) {
		case 0:
			return cred, fmt.Errorf("configfile: %s.accounts[%d]: no connection serves %s", where, i, name)
		case 1:
			cred.Accounts = append(cred.Accounts, AccountID(served[0], name))
		default:
			return cred, fmt.Errorf("configfile: %s.accounts[%d]: %s is served on %s and %s; a credential names an account on one forge",
				where, i, name, served[0], served[1])
		}
	}
	return cred, nil
}

// parseList sorts entries into hosts and address prefixes. An address alone
// becomes a prefix of its own length. An IPv4-mapped IPv6 entry becomes the
// IPv4 it maps, which is how the gateway sees such a destination. An error
// names the entry's index.
func parseList(entries []string) (egresspolicy.List, error) {
	var l egresspolicy.List
	for i, e := range entries {
		if p, err := netip.ParsePrefix(e); err == nil {
			if a := p.Addr(); a.Is4In6() && p.Bits() >= 96 {
				p = netip.PrefixFrom(a.Unmap(), p.Bits()-96)
			}
			l.Prefixes = append(l.Prefixes, p.Masked())
			continue
		}
		if a, err := netip.ParseAddr(e); err == nil {
			a = a.Unmap()
			l.Prefixes = append(l.Prefixes, netip.PrefixFrom(a, a.BitLen()))
			continue
		}
		if err := checkHost(e); err != nil {
			return egresspolicy.List{}, fmt.Errorf("[%d]: %w", i, err)
		}
		l.Hosts = append(l.Hosts, e)
	}
	return l, nil
}

// checkHost accepts a lowercase hostname, optionally with a leading "*.",
// or "*" alone, and nothing else: no scheme, port, path or trailing dot,
// which the gateway strips from a requested host, so an entry carrying one
// will never match.
func checkHost(h string) error {
	if h == "*" {
		return nil
	}
	bare := strings.TrimPrefix(h, "*.")
	if bare == "" || strings.ContainsAny(bare, "/:@ ") || strings.HasPrefix(bare, "*") ||
		strings.HasPrefix(bare, ".") || strings.HasSuffix(bare, ".") || h != strings.ToLower(h) {
		return fmt.Errorf("%q must be a lowercase hostname, optionally prefixed with \"*.\", \"*\" for every host, or an IP address or CIDR", h)
	}
	return nil
}

// EgressRules is what the gateway allows for this file. It has the
// configured entries and the connections' forges. The forges are GitHub and
// its API for an App, because runners fetch from the one and gh reads the
// other. They also include a Forgejo instance's host for a bot. It also has
// the denied entries, the credentials, and what its cache never keeps. The
// cache never keeps the GitHub API, to which the runner and gh send the
// run's token, not even for a locked install.
//
// Provider endpoints are not among them: a runner reaches its model
// through the gateway's model endpoint, and the worker calls the provider.
func (f *File) EgressRules() egresspolicy.Rules {
	allow := egresspolicy.List{Hosts: slices.Clone(f.Egress.allow.Hosts), Prefixes: f.Egress.allow.Prefixes}
	for _, h := range f.forgeHosts() {
		if !slices.Contains(allow.Hosts, h) {
			allow.Hosts = append(allow.Hosts, h)
		}
	}
	return egresspolicy.Rules{
		Allow: allow, Deny: f.Egress.deny, Credentials: maps.Clone(f.Egress.credentials), Uncached: []string{GitHubAPIHost},
	}
}

// Registries are the hosts of the oci credentials that a run of account
// gets, sorted.
func (f *File) Registries(account *Account) []string {
	var hosts []string
	for host, cred := range f.Egress.credentials {
		if cred.Scheme == egresspolicy.SchemeOCI && cred.For(account.ID()) {
			hosts = append(hosts, host)
		}
	}
	slices.Sort(hosts)
	return hosts
}

// GatewayCaches reports whether the gateway keeps downloads, which a
// runner then sends what its repository's lock pins through, from
// whichever host.
func (f *File) GatewayCaches() bool { return f.Run.GatewayCacheDir != "" }

// EgressHosts names the hosts the gateway lets a runner reach, for the
// agent's tool descriptions: the allowed hosts, the forges' among them, less
// an exact host a deny entry refuses. A pattern stays as written, and
// "*" says every host, since the agent is told what it can try, not
// what the rules refuse.
func (f *File) EgressHosts() []string {
	rules := f.EgressRules()
	var hosts []string
	for _, h := range rules.Allow.Hosts {
		if strings.HasPrefix(h, "*") || rules.Allows(h) {
			hosts = append(hosts, h)
		}
	}
	return hosts
}
