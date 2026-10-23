package configfile

import (
	"slices"
	"strings"
	"testing"
)

// gitlabMinimal is one GitLab connection serving acme on gitlab.example.com,
// with fields spliced into its entry.
func gitlabMinimal(fields string) string {
	return `
gitlab:
  acme-lab: { host: gitlab.example.com, accounts: [acme], ` + fields +
		`token: { env: TEST_GITLAB_TOKEN }, webhookSecret: { env: TEST_GITLAB_WEBHOOK } }
`
}

func gitlabEnv(t *testing.T) {
	t.Helper()
	t.Setenv("TEST_GITLAB_TOKEN", "glpat-api")
	t.Setenv("TEST_RUNNER_TOKEN", "glpat-read")
	t.Setenv("TEST_GITLAB_WEBHOOK", "whsec_c2lnbmluZy1rZXk=")
	t.Setenv("TEST_BAD_WEBHOOK", "not-a-signing-token")
	t.Setenv("TEST_EMPTY", "")
}

func TestParseGitLabConnection(t *testing.T) {
	gitlabEnv(t)
	f := mustLoad(t, gitlabMinimal("runnerToken: { env: TEST_RUNNER_TOKEN }, ")+
		"accounts:\n  acme: { limits: { reviewsPerDay: 3 } }\n")
	in, ok := f.Connection("acme-lab")
	if !ok || in.Forge != ForgeGitLab || in.GitLab.Host != "gitlab.example.com" || !slices.Equal(in.Accounts, []string{"acme"}) {
		t.Fatalf("connection = %+v", in)
	}
	if in.GitLab.TokenValue().Value() != "glpat-api" || in.GitLab.RunnerTokenValue().Value() != "glpat-read" || in.WebhookSecretValue().Value() != "whsec_c2lnbmluZy1rZXk=" {
		t.Fatalf("credentials = %q %q %q", in.GitLab.TokenValue(), in.GitLab.RunnerTokenValue(), in.WebhookSecretValue())
	}
	if got := in.ForgeHosts(); !slices.Equal(got, []string{"gitlab.example.com"}) {
		t.Fatalf("ForgeHosts = %q", got)
	}
	a, ok := f.Account(ForgeGitLab, "acme")
	if !ok || a.Key() != "gitlab/acme" || a.Limits.ReviewsPerDay == nil || *a.Limits.ReviewsPerDay != 3 {
		t.Fatalf("the account's entry must be the GitLab account's: %+v", a)
	}
	if f.ConnectionFor(a) != in {
		t.Fatal("ConnectionFor must find the GitLab connection")
	}
	rules := f.EgressRules()
	if !rules.Allows("gitlab.example.com") || rules.Allows("github.com") {
		t.Fatalf("a GitLab connection allows its host alone; allow = %v", rules.Allow.Hosts)
	}
	if got := f.SecretEnv(); !slices.Equal(got, []string{"TEST_GITLAB_TOKEN", "TEST_GITLAB_WEBHOOK", "TEST_RUNNER_TOKEN"}) {
		t.Fatalf("SecretEnv = %q", got)
	}
}

// TestParseGitLabWithoutRunnerToken: runners fetch with the API token
// when no read-only one is given.
func TestParseGitLabWithoutRunnerToken(t *testing.T) {
	gitlabEnv(t)
	f := mustLoad(t, gitlabMinimal(""))
	in, _ := f.Connection("acme-lab")
	if in.GitLab.RunnerTokenValue().Value() != "" {
		t.Fatalf("runner token = %q, want none", in.GitLab.RunnerTokenValue())
	}
}

func TestParseThreeForges(t *testing.T) {
	forgejoEnv(t)
	gitlabEnv(t)
	f := mustLoad(t, minimal+forgejoMinimal("")+gitlabMinimal(""))
	if len(f.Connections) != 3 || len(f.Accounts) != 3 {
		t.Fatalf("connections = %d, accounts = %d", len(f.Connections), len(f.Accounts))
	}
	keys := make([]string, 0, len(f.Accounts))
	for _, a := range f.Accounts {
		keys = append(keys, a.Key())
	}
	slices.Sort(keys)
	if !slices.Equal(keys, []string{"forgejo/acme", "github/acme", "gitlab/acme"}) {
		t.Fatalf("accounts = %q", keys)
	}
	if rules := f.EgressRules(); !rules.Allows("gitlab.example.com") || !rules.Allows("forge.example.com") {
		t.Fatalf("allow = %v", rules.Allow.Hosts)
	}
}

func TestParseGitLabRejects(t *testing.T) {
	forgejoEnv(t)
	gitlabEnv(t)
	tests := []struct {
		name string
		yaml string
		want string
	}{
		{"a name apps uses", strings.Replace(minimal, "acme-bot:", "acme-lab:", 1) + gitlabMinimal(""), "gitlab.acme-lab: apps.acme-lab has the name already"},
		{"a name forgejo uses", strings.Replace(forgejoMinimal(""), "acme-forge:", "acme-lab:", 1) + gitlabMinimal(""), "gitlab.acme-lab: forgejo.acme-lab has the name already"},
		{"the same account on one forge twice", gitlabMinimal("") + "  other: { host: gitlab.example.com, accounts: [ACME], token: { env: TEST_GITLAB_TOKEN }, webhookSecret: { env: TEST_GITLAB_WEBHOOK } }\n",
			"served by \"acme-lab\" already"},
		{"no host", strings.Replace(gitlabMinimal(""), "host: gitlab.example.com, ", "", 1), "gitlab.acme-lab.host is required"},
		{"a host with a scheme", strings.Replace(gitlabMinimal(""), "gitlab.example.com", "https://gitlab.example.com", 1), "gitlab.acme-lab.host"},
		{"a host with a path", strings.Replace(gitlabMinimal(""), "gitlab.example.com", "gitlab.example.com/api", 1), "gitlab.acme-lab.host"},
		{"no token", strings.Replace(gitlabMinimal(""), "token: { env: TEST_GITLAB_TOKEN }, ", "", 1), "gitlab.acme-lab.token: reference must set env"},
		{"an empty token", strings.Replace(gitlabMinimal(""), "TEST_GITLAB_TOKEN", "TEST_EMPTY", 1), "gitlab.acme-lab.token is required"},
		{"a runner token unset", gitlabMinimal("runnerToken: { env: TEST_DOES_NOT_EXIST }, "), "gitlab.acme-lab.runnerToken: environment variable TEST_DOES_NOT_EXIST is not set"},
		{"no webhook secret", strings.Replace(gitlabMinimal(""), ", webhookSecret: { env: TEST_GITLAB_WEBHOOK }", "", 1), "gitlab.acme-lab.webhookSecret: reference must set env"},
		{"a secret token for a signing token", strings.Replace(gitlabMinimal(""), "TEST_GITLAB_WEBHOOK", "TEST_BAD_WEBHOOK", 1), "gitlab.acme-lab.webhookSecret must be the webhook's signing token"},
		{"a Forgejo field", gitlabMinimal("audience: x, "), "field audience not found"},
		{"a bad name", strings.Replace(gitlabMinimal(""), "acme-lab:", "Acme Lab:", 1), "gitlab.Acme Lab: a connection's name"},
		{"the host denied", gitlabMinimal("") + "egress:\n  deny: [gitlab.example.com]\n", "gitlab.example.com is denied, which a connection needs"},
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
