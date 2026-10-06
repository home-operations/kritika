package configfile

import (
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
		t.Errorf("implicit GitHub hosts not allowed; allow = %v", rules.Allow)
	}
	if rules.Allows("openrouter.ai") {
		t.Errorf("a provider's host must not be allowed; allow = %v", rules.Allow)
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
	// any host under it.
	any, err := loadBytes(t, []byte("egress:\n  allow: [\"*\"]\n  deny: [\"*.pastebin.com\"]\n  credentials:\n    registry.example.com: { env: TEST_GH_TOKEN }\n"+minimal))
	if err != nil {
		t.Fatal(err)
	}
	rules = any.EgressRules()
	if !rules.Allows("evil.example") || rules.Allows("paste.pastebin.com") || rules.Credentials["registry.example.com"] != "Bearer ghp_x" {
		t.Fatalf("any host: allow = %v, deny = %v, credentials = %v", rules.Allow, rules.Deny, rules.Credentials)
	}
	// A connection's forge is allowed implicitly, a provider's baseUrl
	// is not, and a credential's value never leaks into the host list.
	h, err := loadBytes(t, []byte("providers:\n  p:\n    type: openai\n    baseUrl: https://llm.example:8443/v1\n    apiKey: { env: TEST_GH_TOKEN }\n"+minimal))
	if err != nil {
		t.Fatal(err)
	}
	if allow := h.EgressRules().Allow; !slices.Contains(allow, GitHubHost) || slices.Contains(allow, "llm.example") ||
		slices.Contains(allow, "api.openai.com") || slices.ContainsFunc(allow, func(h string) bool { return strings.Contains(h, "ghp_") }) {
		t.Fatalf("allow = %v", allow)
	}
}

func TestEgressRejects(t *testing.T) {
	minimalEnv(t)
	t.Setenv("TEST_GH_TOKEN", "ghp_x")
	t.Setenv("TEST_EMPTY", "")
	for name, tt := range map[string]struct{ yaml, want string }{
		"scheme in host":         {"egress:\n  allow: [\"https://ghcr.io\"]\n", "egress.allow[0]"},
		"port in host":           {"egress:\n  allow: [\"ghcr.io:443\"]\n", "lowercase hostname"},
		"uppercase host":         {"egress:\n  allow: [GHCR.io]\n", "lowercase hostname"},
		"scheme in deny":         {"egress:\n  deny: [\"https://ghcr.io\"]\n", "egress.deny[0]"},
		"trailing dot in deny":   {"egress:\n  deny: [\"transfer.sh.\"]\n", "egress.deny[0]"},
		"dot after wildcard":     {"egress:\n  allow: [\"*..example.com\"]\n", "egress.allow[0]"},
		"forge denied":           {"egress:\n  deny: [\"*.github.com\"]\n", "api.github.com is denied"},
		"credential not allowed": {"egress:\n  credentials:\n    registry.example.com: { env: TEST_GH_TOKEN }\n", "not allowed by egress.allow and egress.deny"},
		"credential denied":      {"egress:\n  allow: [\"*\"]\n  deny: [registry.example.com]\n  credentials:\n    registry.example.com: { env: TEST_GH_TOKEN }\n", "not allowed by egress.allow and egress.deny"},
		"credential pattern":     {"egress:\n  allow: [\"*\"]\n  credentials:\n    \"*\": { env: TEST_GH_TOKEN }\n", "one host, not a pattern"},
		"credential suffix":      {"egress:\n  allow: [\"*.example.com\"]\n  credentials:\n    \"*.example.com\": { env: TEST_GH_TOKEN }\n", "one host, not a pattern"},
		"empty credential":       {"egress:\n  allow: [api.github.com]\n  credentials:\n    api.github.com: { env: TEST_EMPTY }\n", "empty value"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := loadBytes(t, []byte(tt.yaml+minimal))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
		})
	}
}
