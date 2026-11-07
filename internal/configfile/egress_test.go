package configfile

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/home-operations/kritika/internal/egresspolicy"
)

// minimalEnv sets what the minimal fixture's secret references resolve.
func minimalEnv(t *testing.T) {
	t.Helper()
	t.Setenv("TEST_OPENROUTER_API_KEY", "sk-or-test")
	t.Setenv("TEST_WEBHOOK_SECRET", "whsec")
	t.Setenv("TEST_PRIVATE_KEY", "key")
	t.Setenv("TEST_CLIENT_ID", "Iv1.fromenv")
}

func TestEgressRules(t *testing.T) {
	minimalEnv(t)
	t.Setenv("TEST_GH_TOKEN", "ghp_x\n")
	f, err := load(t, fixture(t))
	if err != nil {
		t.Fatal(err)
	}
	// The fixture names a GitHub connection, whose forge must be allowed
	// without being listed. It also names an OpenRouter provider, which runners
	// reach only through the gateway's model endpoint.
	rules := f.EgressRules()
	if !rules.Allows("github.com") || !rules.Allows("api.github.com") {
		t.Errorf("implicit GitHub hosts not allowed; allow = %v", rules.Allow.Hosts)
	}
	if rules.Allows("openrouter.ai") {
		t.Errorf("a provider's host must not be allowed; allow = %v", rules.Allow.Hosts)
	}
	if rules.Allows("ghcr.io") {
		t.Fatal("ghcr.io must not be allowed until configured")
	}

	raw := "egress:\n  allow: [ghcr.io, \"*.githubusercontent.com\", api.github.com]\n" +
		"  credentials:\n    api.github.com: { token: { env: TEST_GH_TOKEN }, accounts: [\"*\"] }\n"
	g, err := loadBytes(t, []byte(raw+minimal))
	if err != nil {
		t.Fatal(err)
	}
	rules = g.EgressRules()
	for host, want := range map[string]bool{"ghcr.io": true, "raw.githubusercontent.com": true, "api.github.com": true, "evil.example": false} {
		if rules.Allows(host) != want {
			t.Errorf("Allows(%s) = %v, want %v", host, !want, want)
		}
	}
	if cred := rules.Credentials["api.github.com"]; cred.Header() != "Bearer ghp_x" || !slices.Equal(cred.Accounts, []string{"*"}) {
		t.Fatalf("credentials = %v", rules.Credentials)
	}
	// "*" allows every host but the denied ones, and a credential can name
	// any host under it. Addresses and CIDRs are prefixes, not hosts.
	any, err := loadBytes(t, []byte("egress:\n  allow: [\"*\", 10.0.0.0/8, \"2001:db8::1\", \"::ffff:198.51.100.0/120\"]\n  deny: [\"*.pastebin.com\", 10.0.5.0/24, 10.0.0.9, \"::ffff:203.0.113.7\"]\n  credentials:\n    registry.example.com: { scheme: oci, username: bot, token: { env: TEST_GH_TOKEN }, accounts: [ACME] }\n"+minimal))
	if err != nil {
		t.Fatal(err)
	}
	rules = any.EgressRules()
	want := egresspolicy.Credential{Scheme: egresspolicy.SchemeOCI, Username: "bot", Secret: "ghp_x", Accounts: []string{AccountID(ForgeGitHub, "acme")}}
	if cred := rules.Credentials["registry.example.com"]; !rules.Allows("evil.example") || rules.Allows("paste.pastebin.com") ||
		cred.Scheme != want.Scheme || cred.Username != want.Username || cred.Secret != want.Secret || !slices.Equal(cred.Accounts, want.Accounts) {
		t.Fatalf("any host: allow = %v, deny = %v, credentials = %v", rules.Allow, rules.Deny, rules.Credentials)
	}
	if got := fmt.Sprint(rules.Allow.Prefixes, rules.Deny.Prefixes, rules.Deny.Hosts); got != "[10.0.0.0/8 2001:db8::1/128 198.51.100.0/24] [10.0.5.0/24 10.0.0.9/32 203.0.113.7/32] [*.pastebin.com]" {
		t.Fatalf("prefixes and hosts = %s", got)
	}
	// A connection's forge is allowed implicitly, a provider's baseUrl
	// is not, and a credential's value never leaks into the host list.
	h, err := loadBytes(t, []byte("providers:\n  p:\n    type: openai\n    baseUrl: https://llm.example:8443/v1\n    apiKey: { env: TEST_GH_TOKEN }\n"+minimal))
	if err != nil {
		t.Fatal(err)
	}
	if allow := h.EgressRules().Allow.Hosts; !slices.Contains(allow, GitHubHost) || slices.Contains(allow, "llm.example") ||
		slices.Contains(allow, "api.openai.com") || slices.ContainsFunc(allow, func(h string) bool { return strings.Contains(h, "ghp_") }) {
		t.Fatalf("allow = %v", allow)
	}
}

func TestEgressHosts(t *testing.T) {
	minimalEnv(t)
	for _, tc := range []struct {
		name, raw string
		want      []string
	}{
		{name: "github alone", want: []string{GitHubHost, GitHubAPIHost}},
		{name: "configured hosts, not addresses", raw: "egress:\n  allow: [ghcr.io, \"*.githubusercontent.com\", 10.0.0.0/8, github.com]\n",
			want: []string{"ghcr.io", "*.githubusercontent.com", "github.com", GitHubAPIHost}},
		{name: "a denied host is left out, a pattern kept", raw: "egress:\n  allow: [ghcr.io, quay.io, \"*.example.com\"]\n  deny: [quay.io, \"*.example.com\"]\n",
			want: []string{"ghcr.io", "*.example.com", GitHubHost, GitHubAPIHost}},
		{name: "every host", raw: "egress:\n  allow: [\"*\"]\n  deny: [\"*.pastebin.com\"]\n", want: []string{"*", GitHubHost, GitHubAPIHost}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, err := loadBytes(t, []byte(tc.raw+minimal))
			if err != nil {
				t.Fatal(err)
			}
			if got := f.EgressHosts(); !slices.Equal(got, tc.want) {
				t.Fatalf("EgressHosts() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestEgressRejects(t *testing.T) {
	minimalEnv(t)
	t.Setenv("TEST_GH_TOKEN", "ghp_x")
	t.Setenv("TEST_EMPTY", "")
	const every = `{ token: { env: TEST_GH_TOKEN }, accounts: ["*"] }`
	for name, tt := range map[string]struct{ yaml, want string }{
		"scheme in host":         {"egress:\n  allow: [\"https://ghcr.io\"]\n", "egress.allow[0]"},
		"port in host":           {"egress:\n  allow: [\"ghcr.io:443\"]\n", "lowercase hostname"},
		"uppercase host":         {"egress:\n  allow: [GHCR.io]\n", "lowercase hostname"},
		"scheme in deny":         {"egress:\n  deny: [\"https://ghcr.io\"]\n", "egress.deny[0]"},
		"trailing dot in deny":   {"egress:\n  deny: [\"transfer.sh.\"]\n", "egress.deny[0]"},
		"address with port":      {"egress:\n  allow: [\"10.0.0.5:443\"]\n", "egress.allow[0]"},
		"bad prefix":             {"egress:\n  allow: [10.0.0.0/33]\n", "egress.allow[0]"},
		"dot after wildcard":     {"egress:\n  allow: [\"*..example.com\"]\n", "egress.allow[0]"},
		"forge denied":           {"egress:\n  deny: [\"*.github.com\"]\n", "api.github.com is denied"},
		"credential not allowed": {"egress:\n  credentials:\n    registry.example.com: " + every + "\n", "not allowed by egress.allow and egress.deny"},
		"credential denied":      {"egress:\n  allow: [\"*\"]\n  deny: [registry.example.com]\n  credentials:\n    registry.example.com: " + every + "\n", "not allowed by egress.allow and egress.deny"},
		"credential pattern":     {"egress:\n  allow: [\"*\"]\n  credentials:\n    \"*\": " + every + "\n", "one host, not a pattern"},
		"credential suffix":      {"egress:\n  allow: [\"*.example.com\"]\n  credentials:\n    \"*.example.com\": " + every + "\n", "one host, not a pattern"},
		"uppercase credential":   {"egress:\n  allow: [\"*\"]\n  credentials:\n    Registry.example.com: " + every + "\n", "lowercase hostname"},
		"empty credential":       {"egress:\n  allow: [api.github.com]\n  credentials:\n    api.github.com: { token: { env: TEST_EMPTY }, accounts: [acme] }\n", "token resolved to an empty value"},
		"a bare reference":       {"egress:\n  allow: [api.github.com]\n  credentials:\n    api.github.com: { env: TEST_GH_TOKEN }\n", "field env not found"},
		"unknown scheme":         {"egress:\n  allow: [api.github.com]\n  credentials:\n    api.github.com: { scheme: token, token: { env: TEST_GH_TOKEN }, accounts: [acme] }\n", `scheme "token" must be bearer, basic or oci`},
		"bearer username":        {"egress:\n  allow: [api.github.com]\n  credentials:\n    api.github.com: { username: bot, token: { env: TEST_GH_TOKEN }, accounts: [acme] }\n", "a bearer credential has no username"},
		"oci without username":   {"egress:\n  allow: [ghcr.io]\n  credentials:\n    ghcr.io: { scheme: oci, token: { env: TEST_GH_TOKEN }, accounts: [acme] }\n", "the oci scheme needs one"},
		"no accounts":            {"egress:\n  allow: [ghcr.io]\n  credentials:\n    ghcr.io: { token: { env: TEST_GH_TOKEN } }\n", "accounts must list at least one account"},
		"every account and one":  {"egress:\n  allow: [ghcr.io]\n  credentials:\n    ghcr.io: { token: { env: TEST_GH_TOKEN }, accounts: [\"*\", acme] }\n", "stands alone"},
		"an unserved account":    {"egress:\n  allow: [ghcr.io]\n  credentials:\n    ghcr.io: { token: { env: TEST_GH_TOKEN }, accounts: [acme, other] }\n", "accounts[1]: no connection serves other"},
		"an account twice":       {"egress:\n  allow: [ghcr.io]\n  credentials:\n    ghcr.io: { token: { env: TEST_GH_TOKEN }, accounts: [acme, ACME] }\n", "listed twice"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := loadBytes(t, []byte(tt.yaml+minimal))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
		})
	}
}

// TestRegistries: a run gets the registries of the oci credentials given to
// its account and to every account, and neither another account's nor a
// host with another scheme.
func TestRegistries(t *testing.T) {
	minimalEnv(t)
	t.Setenv("TEST_GH_TOKEN", "ghp_x")
	raw := `apps:
  acme-bot: { accounts: [acme], clientId: Iv1.acme, privateKey: { env: TEST_PRIVATE_KEY }, webhookSecret: { env: TEST_WEBHOOK_SECRET } }
  other-bot: { accounts: [other], clientId: Iv1.other, privateKey: { env: TEST_PRIVATE_KEY }, webhookSecret: { env: TEST_WEBHOOK_SECRET } }
egress:
  allow: ["*"]
  credentials:
    api.github.com: { token: { env: TEST_GH_TOKEN }, accounts: ["*"] }
    ghcr.io: { scheme: oci, username: bot, token: { env: TEST_GH_TOKEN }, accounts: ["*"] }
    git.example.com: { scheme: oci, username: bot, token: { env: TEST_GH_TOKEN }, accounts: [other] }
    registry.example.com: { scheme: basic, username: bot, token: { env: TEST_GH_TOKEN }, accounts: [acme] }
`
	f, err := loadBytes(t, []byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string][]string{
		"acme":  {"ghcr.io"},
		"other": {"ghcr.io", "git.example.com"},
	} {
		account := &Account{Forge: ForgeGitHub, Name: name}
		if got := f.Registries(account); !slices.Equal(got, want) {
			t.Errorf("Registries(%s) = %v, want %v", name, got, want)
		}
	}
}

// TestEgressCache: the cache's configuration and what it never keeps come
// from the same file, and a cache directory alone keeps what a lock pins.
func TestEgressCache(t *testing.T) {
	minimalEnv(t)
	f, err := loadBytes(t, []byte("gatewayCacheDir: /var/cache/kritika\ngatewayCacheMaxSize: 512Mi\n"+minimal))
	if err != nil {
		t.Fatal(err)
	}
	if f.Run.GatewayCacheDir != "/var/cache/kritika" || f.Run.GatewayCacheMaxSize != 512<<20 {
		t.Errorf("cache settings = %q, %d", f.Run.GatewayCacheDir, f.Run.GatewayCacheMaxSize)
	}
	if got := f.EgressRules().Uncached; !slices.Equal(got, []string{"api.github.com"}) {
		t.Errorf("rules never cache %v, want the GitHub API", got)
	}
	if !f.GatewayCaches() {
		t.Error("GatewayCaches = false with a cache directory")
	}
	none, err := loadBytes(t, []byte(minimal))
	if err != nil {
		t.Fatal(err)
	}
	if none.GatewayCaches() {
		t.Error("GatewayCaches = true with no cache directory")
	}
}
