package configfile

import (
	"fmt"
	"slices"
	"strings"
	"testing"
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
	// without being listed, and an OpenRouter provider, which runners reach
	// only through the gateway's model endpoint.
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
		"  credentials:\n    api.github.com: { env: TEST_GH_TOKEN }\n"
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
	if rules.Credentials["api.github.com"] != "Bearer ghp_x" {
		t.Fatalf("credentials = %v", rules.Credentials)
	}
	// "*" allows every host but the denied ones, and a credential may name
	// any host under it. Addresses and CIDRs are prefixes, not hosts.
	any, err := loadBytes(t, []byte("egress:\n  allow: [\"*\", 10.0.0.0/8, \"2001:db8::1\", \"::ffff:198.51.100.0/120\"]\n  deny: [\"*.pastebin.com\", 10.0.5.0/24, 10.0.0.9, \"::ffff:203.0.113.7\"]\n  credentials:\n    registry.example.com: { env: TEST_GH_TOKEN }\n"+minimal))
	if err != nil {
		t.Fatal(err)
	}
	rules = any.EgressRules()
	if !rules.Allows("evil.example") || rules.Allows("paste.pastebin.com") || rules.Credentials["registry.example.com"] != "Bearer ghp_x" {
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
	for name, tt := range map[string]struct{ yaml, want string }{
		"scheme in host":            {"egress:\n  allow: [\"https://ghcr.io\"]\n", "egress.allow[0]"},
		"port in host":              {"egress:\n  allow: [\"ghcr.io:443\"]\n", "lowercase hostname"},
		"uppercase host":            {"egress:\n  allow: [GHCR.io]\n", "lowercase hostname"},
		"scheme in deny":            {"egress:\n  deny: [\"https://ghcr.io\"]\n", "egress.deny[0]"},
		"trailing dot in deny":      {"egress:\n  deny: [\"transfer.sh.\"]\n", "egress.deny[0]"},
		"address with port":         {"egress:\n  allow: [\"10.0.0.5:443\"]\n", "egress.allow[0]"},
		"bad prefix":                {"egress:\n  allow: [10.0.0.0/33]\n", "egress.allow[0]"},
		"dot after wildcard":        {"egress:\n  allow: [\"*..example.com\"]\n", "egress.allow[0]"},
		"forge denied":              {"egress:\n  deny: [\"*.github.com\"]\n", "api.github.com is denied"},
		"credential not allowed":    {"egress:\n  credentials:\n    registry.example.com: { env: TEST_GH_TOKEN }\n", "not allowed by egress.allow and egress.deny"},
		"credential denied":         {"egress:\n  allow: [\"*\"]\n  deny: [registry.example.com]\n  credentials:\n    registry.example.com: { env: TEST_GH_TOKEN }\n", "not allowed by egress.allow and egress.deny"},
		"credential pattern":        {"egress:\n  allow: [\"*\"]\n  credentials:\n    \"*\": { env: TEST_GH_TOKEN }\n", "one host, not a pattern"},
		"credential suffix":         {"egress:\n  allow: [\"*.example.com\"]\n  credentials:\n    \"*.example.com\": { env: TEST_GH_TOKEN }\n", "one host, not a pattern"},
		"empty credential":          {"egress:\n  allow: [api.github.com]\n  credentials:\n    api.github.com: { env: TEST_EMPTY }\n", "empty value"},
		"cache without a directory": {"egress:\n  allow: [get.helm.sh]\n  cache: [get.helm.sh]\n", "egress.cache needs gatewayCacheDir"},
		"cache pattern":             {"gatewayCacheDir: /c\negress:\n  allow: [\"*.example.com\"]\n  cache: [\"*.example.com\"]\n", "one host, not a pattern"},
		"cache every host":          {"gatewayCacheDir: /c\negress:\n  allow: [\"*\"]\n  cache: [\"*\"]\n", "one host, not a pattern"},
		"cache uppercase host":      {"gatewayCacheDir: /c\negress:\n  allow: [\"*\"]\n  cache: [GET.helm.sh]\n", "egress.cache[0]: \"GET.helm.sh\" must be a lowercase hostname"},
		"cache listed twice":        {"gatewayCacheDir: /c\negress:\n  allow: [get.helm.sh]\n  cache: [get.helm.sh, get.helm.sh]\n", "egress.cache[1]: get.helm.sh is listed twice"},
		"cache not allowed":         {"gatewayCacheDir: /c\negress:\n  allow: [ghcr.io]\n  cache: [get.helm.sh]\n", "egress.cache[0]: get.helm.sh is not allowed"},
		"cache denied":              {"gatewayCacheDir: /c\negress:\n  allow: [\"*\"]\n  deny: [get.helm.sh]\n  cache: [get.helm.sh]\n", "egress.cache[0]: get.helm.sh is not allowed"},
		"cache with a credential": {"gatewayCacheDir: /c\negress:\n  allow: [registry.example.com]\n  credentials:\n    registry.example.com: { env: TEST_GH_TOKEN }\n" +
			"  cache: [registry.example.com]\n", "registry.example.com has a credential"},
		"cache the GitHub API": {"gatewayCacheDir: /c\negress:\n  allow: [api.github.com]\n  cache: [api.github.com]\n", "api.github.com is sent the run's token"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := loadBytes(t, []byte(tt.yaml+minimal))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
		})
	}
}

// TestEgressCache: the cached hosts reach the gateway's rules and the
// runner Jobs as written, and the cache's settings come from the same file.
func TestEgressCache(t *testing.T) {
	minimalEnv(t)
	raw := "gatewayCacheDir: /var/cache/kritika\ngatewayCacheMaxSize: 512Mi\n" +
		"egress:\n  allow: [get.helm.sh, \"*.githubusercontent.com\"]\n  cache: [get.helm.sh, github.com]\n"
	f, err := loadBytes(t, []byte(raw+minimal))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"get.helm.sh", "github.com"}
	if got := f.EgressRules().Cached; !slices.Equal(got, want) {
		t.Errorf("rules cache %v, want %v", got, want)
	}
	if got := f.EgressCached(); !slices.Equal(got, want) {
		t.Errorf("EgressCached = %v, want %v", got, want)
	}
	if f.Run.GatewayCacheDir != "/var/cache/kritika" || f.Run.GatewayCacheMaxSize != 512<<20 {
		t.Errorf("cache settings = %q, %d", f.Run.GatewayCacheDir, f.Run.GatewayCacheMaxSize)
	}
	none, err := loadBytes(t, []byte(minimal))
	if err != nil {
		t.Fatal(err)
	}
	if none.EgressRules().Cached != nil || none.EgressCached() != nil {
		t.Errorf("no cache configured: rules %v, hosts %v", none.EgressRules().Cached, none.EgressCached())
	}
}
