package configfile

import (
	"slices"
	"strings"
	"testing"
)

// forgejoMinimal is one Forgejo connection serving acme on forge.example.com,
// with fields spliced into its entry.
func forgejoMinimal(fields string) string {
	return `
forgejo:
  acme-forge: { host: forge.example.com, accounts: [acme], ` + fields +
		`privateKey: { env: TEST_PRIVATE_KEY }, webhookSecret: { env: TEST_WEBHOOK_SECRET } }
`
}

func forgejoEnv(t *testing.T) {
	t.Helper()
	t.Setenv("TEST_PRIVATE_KEY", "pem")
	t.Setenv("TEST_AUDIENCE", "aud-from-env")
	t.Setenv("TEST_WEBHOOK_SECRET", "whsec")
	t.Setenv("TEST_EMPTY", "")
}

func TestParseForgejoConnection(t *testing.T) {
	forgejoEnv(t)
	f := mustLoad(t, forgejoMinimal("audience: aud-1, runnerAudience: { env: TEST_AUDIENCE }, ")+
		"accounts:\n  acme: { limits: { reviewsPerDay: 3 } }\nrepositories:\n  acme/*: { trigger: { limit: 2 } }\n")
	in, ok := f.Connection("acme-forge")
	if !ok || in.Forge != ForgeForgejo || in.Forgejo.Host != "forge.example.com" || !slices.Equal(in.Accounts, []string{"acme"}) {
		t.Fatalf("connection = %+v", in)
	}
	if in.Forgejo.AudienceValue() != "aud-1" || in.Forgejo.RunnerAudienceValue() != "aud-from-env" ||
		in.Forgejo.PrivateKeyValue().Value() != "pem" || in.WebhookSecretValue().Value() != "whsec" {
		t.Fatalf("credentials = %q %q %q %q", in.Forgejo.AudienceValue(), in.Forgejo.RunnerAudienceValue(), in.Forgejo.PrivateKeyValue(), in.WebhookSecretValue())
	}
	if got := in.ForgeHosts(); !slices.Equal(got, []string{"forge.example.com"}) {
		t.Fatalf("ForgeHosts = %q", got)
	}
	a, ok := f.Account(ForgeForgejo, "acme")
	if !ok || a.Key() != "forgejo/acme" || a.Limits.ReviewsPerDay == nil || *a.Limits.ReviewsPerDay != 3 || a.Trigger.Limit == nil {
		t.Fatalf("the account's entries must be the Forgejo account's: %+v", a)
	}
	if _, ok := f.Account(ForgeGitHub, "acme"); ok {
		t.Fatal("no GitHub account named acme is served")
	}
	if f.ConnectionFor(a) != in {
		t.Fatal("ConnectionFor must find the Forgejo connection")
	}
	rules := f.EgressRules()
	if !rules.Allows("forge.example.com") || rules.Allows("github.com") {
		t.Fatalf("a Forgejo connection allows its host alone; allow = %v", rules.Allow.Hosts)
	}
	if got := f.SecretEnv(); !slices.Equal(got, []string{"TEST_AUDIENCE", "TEST_PRIVATE_KEY", "TEST_WEBHOOK_SECRET"}) {
		t.Fatalf("SecretEnv = %q", got)
	}
}

// TestParseForgejoPreviousKey: a key being rotated out resolves beside
// the signing key.
func TestParseForgejoPreviousKey(t *testing.T) {
	forgejoEnv(t)
	t.Setenv("TEST_OLD_KEY", "old-pem")
	f := mustLoad(t, forgejoMinimal("previousKey: { env: TEST_OLD_KEY }, "))
	in, _ := f.Connection("acme-forge")
	if in.Forgejo.PreviousKeyValue().Value() != "old-pem" || in.Forgejo.PrivateKeyValue().Value() != "pem" {
		t.Fatalf("keys = %q %q", in.Forgejo.PrivateKeyValue(), in.Forgejo.PreviousKeyValue())
	}
}

// TestParseForgejoNeedsHTTPS: Forgejo reads kritika's keys over HTTPS
// alone, so a Forgejo connection needs an https web URL; a GitHub App
// does not.
func TestParseForgejoNeedsHTTPS(t *testing.T) {
	forgejoEnv(t)
	for _, u := range []string{"http://kritika.example.com", "HTTP://kritika.example.com", "Http://kritika.example.com/base"} {
		if _, err := load(t, "webUrl: "+u+"\n"+forgejoMinimal("")); err == nil || !strings.Contains(err.Error(), "webUrl must be https") {
			t.Fatalf("web URL %s = %v, want it refused", u, err)
		}
	}
	mustLoad(t, "webUrl: https://kritika.example.com\n"+forgejoMinimal(""))
	mustLoad(t, "webUrl: http://kritika.example.com\n"+minimal)
}

// TestParseForgejoWithoutAudience: the audience exists only once the
// integration does, which needs kritika's keys served, so a connection
// loads without one.
func TestParseForgejoWithoutAudience(t *testing.T) {
	forgejoEnv(t)
	f := mustLoad(t, forgejoMinimal(""))
	in, _ := f.Connection("acme-forge")
	if in.Forgejo.AudienceValue() != "" || in.Forgejo.RunnerAudienceValue() != "" {
		t.Fatalf("audiences = %q %q, want none", in.Forgejo.AudienceValue(), in.Forgejo.RunnerAudienceValue())
	}
}

func TestParseBothForges(t *testing.T) {
	forgejoEnv(t)
	// The same login on both forges is two accounts, each its
	// connection's; an entry keyed by the name alone cannot say which.
	both := minimal + forgejoMinimal("")
	f := mustLoad(t, both)
	if len(f.Connections) != 2 || len(f.Accounts) != 2 {
		t.Fatalf("connections = %d, accounts = %d", len(f.Connections), len(f.Accounts))
	}
	keys := []string{f.Accounts[0].Key(), f.Accounts[1].Key()}
	slices.Sort(keys)
	if !slices.Equal(keys, []string{"forgejo/acme", "github/acme"}) {
		t.Fatalf("accounts = %q", keys)
	}
	rules := f.EgressRules()
	for _, h := range []string{"github.com", "api.github.com", "forge.example.com"} {
		if !rules.Allows(h) {
			t.Errorf("Allows(%s) = false", h)
		}
	}
	for _, entry := range []string{"accounts:\n  acme: {}\n", "repositories:\n  acme/*: {}\n", "repositories:\n  acme/widgets: {}\n"} {
		if _, err := load(t, both+entry); err == nil || !strings.Contains(err.Error(), "served on") {
			t.Fatalf("entry %q: err = %v, want the ambiguity named", entry, err)
		}
	}
}

func TestParseForgejoRejects(t *testing.T) {
	forgejoEnv(t)
	tests := []struct {
		name string
		yaml string
		want string
	}{
		{"a name both maps use", strings.Replace(minimal, "acme-bot:", "acme-forge:", 1) + forgejoMinimal(""), "apps.acme-forge has the name already"},
		{"the same account on one forge twice", forgejoMinimal("") + "  other: { host: forge.example.com, accounts: [ACME], privateKey: { env: TEST_PRIVATE_KEY }, webhookSecret: { env: TEST_WEBHOOK_SECRET } }\n",
			"served by \"acme-forge\" already"},
		{"no host", strings.Replace(forgejoMinimal(""), "host: forge.example.com, ", "", 1), "forgejo.acme-forge.host is required"},
		{"a host with a scheme", strings.Replace(forgejoMinimal(""), "forge.example.com", "https://forge.example.com", 1), "forgejo.acme-forge.host"},
		{"a host with a path", strings.Replace(forgejoMinimal(""), "forge.example.com", "forge.example.com/api", 1), "forgejo.acme-forge.host"},
		{"an uppercase host", strings.Replace(forgejoMinimal(""), "forge.example.com", "Forge.example.com", 1), "forgejo.acme-forge.host"},
		{"no private key", strings.Replace(forgejoMinimal(""), "privateKey: { env: TEST_PRIVATE_KEY }, ", "", 1), "forgejo.acme-forge.privateKey: reference must set env"},
		{"an empty private key", strings.Replace(forgejoMinimal(""), "TEST_PRIVATE_KEY", "TEST_EMPTY", 1), "forgejo.acme-forge.privateKey is required"},
		{"an audience reference unset", forgejoMinimal("audience: { env: TEST_DOES_NOT_EXIST }, "), "forgejo.acme-forge.audience: environment variable TEST_DOES_NOT_EXIST is not set"},
		{"a runner audience alone", forgejoMinimal("runnerAudience: aud-read, "), "forgejo.acme-forge.runnerAudience needs audience set"},
		{"no webhook secret", strings.Replace(forgejoMinimal(""), ", webhookSecret: { env: TEST_WEBHOOK_SECRET }", "", 1), "forgejo.acme-forge.webhookSecret: reference must set env"},
		{"a GitHub App field", forgejoMinimal("clientId: x, "), "field clientId not found"},
		{"a token field", forgejoMinimal("token: { env: TEST_WEBHOOK_SECRET }, "), "field token not found"},
		{"a bad name", strings.Replace(forgejoMinimal(""), "acme-forge:", "Acme Forge:", 1), "forgejo.Acme Forge: a connection's name"},
		{"the host denied", forgejoMinimal("") + "egress:\n  deny: [forge.example.com]\n", "forge.example.com is denied, which a connection needs"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := load(t, tt.yaml)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}
