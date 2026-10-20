package runner

import (
	"encoding/json"
	"net/url"
	"slices"
	"strings"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/egress"
)

// urlReplacement is one of mise's url_replacements rules: a download URL
// with the prefix From becomes To.
type urlReplacement struct{ From, To string }

// cacheReplacements are the rules that send a download from one of hosts
// to the gateway as plain HTTP, which it upgrades, so it can keep what it
// fetches.
func cacheReplacements(hosts []string) []urlReplacement {
	rules := make([]urlReplacement, 0, len(hosts))
	for _, host := range hosts {
		rules = append(rules, urlReplacement{From: "https://" + host + "/", To: "http://" + host + "/"})
	}
	return rules
}

// lockedInstallEnv is the environment of a repository's locked install:
// common, mise's environment before any rule, with the rules of the hosts
// the lock pins downloads on, urls, and the proxy's username saying it is
// locked.
func lockedInstallEnv(common, urls []string) []string {
	env := slices.Clone(common)
	if rules := cacheReplacements(lockHosts(urls)); len(rules) > 0 {
		env = append(env, "MISE_URL_REPLACEMENTS="+miseURLReplacements(rules))
	}
	return withProxyUser(env, egress.LockedProxyUser)
}

// miseTokenHosts are the hosts mise sends the run's GitHub token, which
// it refuses to send to a URL a rule rewrites to plain HTTP.
var miseTokenHosts = []string{configfile.GitHubAPIHost, "raw.githubusercontent.com"}

// lockHosts are the hosts of urls, the downloads a lock pins, whose
// downloads the gateway's cache can keep, sorted: those of the https://
// URLs on the default port, with no credentials or query, less the hosts
// mise sends the run's token and any named with a trailing dot. A rule's
// from is matched against the URL as mise writes it, with the host in
// lowercase.
func lockHosts(urls []string) []string {
	var hosts []string
	for _, raw := range urls {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.ForceQuery ||
			u.Port() != "" && u.Port() != "443" {
			continue
		}
		host := strings.ToLower(u.Hostname())
		// An IPv6 address would need its brackets in a rule's from, and a
		// name with a trailing dot is another name to mise.
		if host == "" || strings.Contains(host, ":") || strings.HasSuffix(host, ".") ||
			slices.Contains(miseTokenHosts, host) {
			continue
		}
		hosts = append(hosts, host)
	}
	slices.Sort(hosts)
	return slices.Compact(hosts)
}

// httpProxied reports whether env names an HTTP proxy.
func httpProxied(env []string) bool {
	return slices.ContainsFunc(env, func(kv string) bool {
		name, value, _ := strings.Cut(kv, "=")
		return strings.EqualFold(name, "HTTP_PROXY") && value != ""
	})
}

// withProxyUser is env with the username of each proxy URL, under either
// spelling, set to user, the password, which is the run token, kept.
func withProxyUser(env []string, user string) []string {
	out := slices.Clone(env)
	for i, kv := range out {
		name, value, _ := strings.Cut(kv, "=")
		if !strings.EqualFold(name, "HTTP_PROXY") && !strings.EqualFold(name, "HTTPS_PROXY") {
			continue
		}
		u, err := url.Parse(value)
		if err != nil || u.User == nil {
			continue
		}
		password, _ := u.User.Password()
		u.User = url.UserPassword(user, password)
		out[i] = name + "=" + u.String()
	}
	return out
}

// miseURLReplacements is MISE_URL_REPLACEMENTS for rules: the JSON object
// mise reads them from, keyed by from in the rules' order, which mise
// keeps, trying the rules in turn until one matches. A map would sort them.
func miseURLReplacements(rules []urlReplacement) string {
	var b strings.Builder
	b.WriteByte('{')
	for i, r := range rules {
		if i > 0 {
			b.WriteByte(',')
		}
		// Marshaling a string cannot fail.
		from, _ := json.Marshal(r.From)
		to, _ := json.Marshal(r.To)
		b.Write(from)
		b.WriteByte(':')
		b.Write(to)
	}
	b.WriteByte('}')
	return b.String()
}
