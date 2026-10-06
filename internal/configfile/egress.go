package configfile

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/home-operations/kritika/internal/egress"
)

// GitHubHost is the forge every connection and GitHub sign-in talks to, and
// GitHubAPIHost its API, which a runner's gh calls.
const (
	GitHubHost    = "github.com"
	GitHubAPIHost = "api.github.com"
)

// validateEgress checks the allow and deny entries are bare hostnames, that
// no deny entry cuts off the forge a connection needs, and that each
// credential names one host that is allowed, explicitly or implicitly; the
// gateway looks a credential up by exact host, so a pattern would never
// apply.
func (f *File) validateEgress() error {
	for i, h := range f.Egress.Allow {
		if err := checkHost(h); err != nil {
			return fmt.Errorf("configfile: egress.allow[%d]: %w", i, err)
		}
	}
	for i, h := range f.Egress.Deny {
		if err := checkHost(h); err != nil {
			return fmt.Errorf("configfile: egress.deny[%d]: %w", i, err)
		}
	}
	rules := f.EgressRules()
	if len(f.Connections) > 0 {
		for _, host := range []string{GitHubHost, GitHubAPIHost} {
			if !rules.Allows(host) {
				return fmt.Errorf("configfile: egress.deny: %s is denied, which every app needs", host)
			}
		}
	}
	for _, host := range slices.Sorted(maps.Keys(f.Egress.credentials)) {
		if err := checkHost(host); err != nil {
			return fmt.Errorf("configfile: egress.credentials.%s: %w", host, err)
		}
		if strings.HasPrefix(host, "*") {
			return fmt.Errorf("configfile: egress.credentials.%s: a credential names one host, not a pattern", host)
		}
		if !rules.Allows(host) {
			return fmt.Errorf("configfile: egress.credentials.%s: host is not allowed by egress.allow and egress.deny", host)
		}
		if f.Egress.credentials[host].Value() == "" {
			return fmt.Errorf("configfile: egress.credentials.%s resolved to an empty value", host)
		}
	}
	return nil
}

// checkHost accepts a lowercase hostname, optionally with a leading "*.",
// or "*" alone, and nothing else: no scheme, port, path or trailing dot,
// which the gateway strips from a requested host, so an entry carrying one
// would never match.
func checkHost(h string) error {
	if h == "*" {
		return nil
	}
	bare := strings.TrimPrefix(h, "*.")
	if bare == "" || strings.ContainsAny(bare, "/:@ ") || strings.HasPrefix(bare, "*") ||
		strings.HasPrefix(bare, ".") || strings.HasSuffix(bare, ".") || h != strings.ToLower(h) {
		return fmt.Errorf("%q must be a lowercase hostname, optionally prefixed with \"*.\", or \"*\" for every host", h)
	}
	return nil
}

// EgressRules is what the gateway allows for this file: the configured
// hosts and, once any connection exists, GitHub and its API, since runners
// fetch from the one and gh reads the other, the denied hosts, plus the
// credentials as Authorization header values. Provider endpoints are not
// among them: a runner reaches its model through the gateway's model
// endpoint, and the worker calls the provider.
func (f *File) EgressRules() egress.Rules {
	allow := slices.Clone(f.Egress.Allow)
	if len(f.Connections) > 0 {
		for _, h := range []string{GitHubHost, GitHubAPIHost} {
			if !slices.Contains(allow, h) {
				allow = append(allow, h)
			}
		}
	}
	creds := make(map[string]string, len(f.Egress.credentials))
	for host, secret := range f.Egress.credentials {
		creds[host] = "Bearer " + secret.Value()
	}
	return egress.Rules{Allow: allow, Deny: slices.Clone(f.Egress.Deny), Credentials: creds}
}
