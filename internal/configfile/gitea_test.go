package configfile

import (
	"slices"
	"strings"
	"testing"
)

// giteaMinimal is one Gitea connection serving acme on gitea.example.com,
// with fields spliced into its entry.
func giteaMinimal(fields string) string {
	return `
gitea:
  acme-tea: { host: gitea.example.com, accounts: [acme], ` + fields +
		`token: { env: TEST_GITEA_TOKEN }, webhookSecret: { env: TEST_GITEA_WEBHOOK } }
`
}

func giteaEnv(t *testing.T) {
	t.Helper()
	t.Setenv("TEST_GITEA_TOKEN", "gitea-api")
	t.Setenv("TEST_GITEA_RUNNER_TOKEN", "gitea-read")
	t.Setenv("TEST_GITEA_WEBHOOK", "gitea-hook")
	t.Setenv("TEST_EMPTY", "")
}

func TestParseGiteaConnection(t *testing.T) {
	giteaEnv(t)
	f := mustLoad(t, giteaMinimal("runnerToken: { env: TEST_GITEA_RUNNER_TOKEN }, ")+
		"accounts:\n  acme: { limits: { reviewsPerDay: 3 } }\nrepositories:\n  acme/*: { trigger: { limit: 2 } }\n")
	in, ok := f.Connection("acme-tea")
	if !ok || in.Forge != ForgeGitea || in.Gitea.Host != "gitea.example.com" || !slices.Equal(in.Accounts, []string{"acme"}) {
		t.Fatalf("connection = %+v", in)
	}
	if in.Gitea.TokenValue().Value() != "gitea-api" || in.Gitea.RunnerTokenValue().Value() != "gitea-read" || in.WebhookSecretValue().Value() != "gitea-hook" {
		t.Fatalf("credentials = %q %q %q", in.Gitea.TokenValue(), in.Gitea.RunnerTokenValue(), in.WebhookSecretValue())
	}
	if got := in.ForgeHosts(); !slices.Equal(got, []string{"gitea.example.com"}) {
		t.Fatalf("ForgeHosts = %q", got)
	}
	a, ok := f.Account(ForgeGitea, "acme")
	if !ok || a.Key() != "gitea/acme" || a.Limits.ReviewsPerDay == nil || *a.Limits.ReviewsPerDay != 3 || a.Trigger.Limit == nil {
		t.Fatalf("the account's entries must be the Gitea account's: %+v", a)
	}
	if f.ConnectionFor(a) != in {
		t.Fatal("ConnectionFor must find the Gitea connection")
	}
	rules := f.EgressRules()
	if !rules.Allows("gitea.example.com") || rules.Allows("github.com") {
		t.Fatalf("a Gitea connection allows its host alone; allow = %v", rules.Allow.Hosts)
	}
	if got := f.SecretEnv(); !slices.Equal(got, []string{"TEST_GITEA_RUNNER_TOKEN", "TEST_GITEA_TOKEN", "TEST_GITEA_WEBHOOK"}) {
		t.Fatalf("SecretEnv = %q", got)
	}
}

// TestParseGiteaWithoutRunnerToken: runners fetch with the API token when
// no read-only one is given.
func TestParseGiteaWithoutRunnerToken(t *testing.T) {
	giteaEnv(t)
	f := mustLoad(t, giteaMinimal(""))
	in, _ := f.Connection("acme-tea")
	if in.Gitea.RunnerTokenValue().Value() != "" {
		t.Fatalf("runner token = %q, want none", in.Gitea.RunnerTokenValue())
	}
}

func TestParseFourForges(t *testing.T) {
	forgejoEnv(t)
	gitlabEnv(t)
	giteaEnv(t)
	f := mustLoad(t, minimal+forgejoMinimal("")+gitlabMinimal("")+giteaMinimal(""))
	keys := make([]string, 0, len(f.Accounts))
	for _, a := range f.Accounts {
		keys = append(keys, a.Key())
	}
	slices.Sort(keys)
	if len(f.Connections) != 4 || !slices.Equal(keys, []string{"forgejo/acme", "gitea/acme", "github/acme", "gitlab/acme"}) {
		t.Fatalf("connections = %d, accounts = %q", len(f.Connections), keys)
	}
	if rules := f.EgressRules(); !rules.Allows("gitea.example.com") || !rules.Allows("gitlab.example.com") || !rules.Allows("forge.example.com") {
		t.Fatalf("allow = %v", rules.Allow.Hosts)
	}
}

func TestParseGiteaRejects(t *testing.T) {
	forgejoEnv(t)
	gitlabEnv(t)
	giteaEnv(t)
	tests := []struct {
		name string
		yaml string
		want string
	}{
		{"a name apps uses", strings.Replace(minimal, "acme-bot:", "acme-tea:", 1) + giteaMinimal(""), "gitea.acme-tea: apps.acme-tea has the name already"},
		{"a name gitlab uses", strings.Replace(gitlabMinimal(""), "acme-lab:", "acme-tea:", 1) + giteaMinimal(""), "gitea.acme-tea: gitlab.acme-tea has the name already"},
		{"the same account on one forge twice", giteaMinimal("") + "  other: { host: gitea.example.com, accounts: [ACME], token: { env: TEST_GITEA_TOKEN }, webhookSecret: { env: TEST_GITEA_WEBHOOK } }\n",
			"served by \"acme-tea\" already"},
		{"no host", strings.Replace(giteaMinimal(""), "host: gitea.example.com, ", "", 1), "gitea.acme-tea.host is required"},
		{"a host with a scheme", strings.Replace(giteaMinimal(""), "gitea.example.com", "https://gitea.example.com", 1), "gitea.acme-tea.host"},
		{"a host with a path", strings.Replace(giteaMinimal(""), "gitea.example.com", "gitea.example.com/git", 1), "gitea.acme-tea.host"},
		{"no token", strings.Replace(giteaMinimal(""), "token: { env: TEST_GITEA_TOKEN }, ", "", 1), "gitea.acme-tea.token: reference must set env"},
		{"an empty token", strings.Replace(giteaMinimal(""), "TEST_GITEA_TOKEN", "TEST_EMPTY", 1), "gitea.acme-tea.token is required"},
		{"a runner token unset", giteaMinimal("runnerToken: { env: TEST_DOES_NOT_EXIST }, "), "gitea.acme-tea.runnerToken: environment variable TEST_DOES_NOT_EXIST is not set"},
		{"no webhook secret", strings.Replace(giteaMinimal(""), ", webhookSecret: { env: TEST_GITEA_WEBHOOK }", "", 1), "gitea.acme-tea.webhookSecret: reference must set env"},
		{"an empty webhook secret", strings.Replace(giteaMinimal(""), "TEST_GITEA_WEBHOOK", "TEST_EMPTY", 1), "gitea.acme-tea.webhookSecret is required"},
		{"a Forgejo field", giteaMinimal("audience: x, "), "field audience not found"},
		{"a subgroup key", giteaMinimal("") + "repositories:\n  acme/sub/widgets: {}\n", "keyed owner/* or owner/name"},
		{"the host denied", giteaMinimal("") + "egress:\n  deny: [gitea.example.com]\n", "gitea.example.com is denied, which a connection needs"},
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
