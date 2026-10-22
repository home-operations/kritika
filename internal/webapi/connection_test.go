package webapi

import (
	"testing"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/configfile/configfiletest"
)

// TestConnectionGitLab: a GitLab connection shows its instance and which
// of its tokens are set, never their values.
func TestConnectionGitLab(t *testing.T) {
	t.Setenv("TEST_GL_TOKEN", "glpat-api")
	t.Setenv("TEST_GL_HOOK", "whsec_c2lnbmluZy1rZXk=")
	f := configfiletest.Load(t, `
gitlab:
  lab:
    host: gitlab.example.com
    accounts: [acme]
    token: { env: TEST_GL_TOKEN }
    webhookSecret: { env: TEST_GL_HOOK }
`)
	in, _ := f.Connection("lab")
	c := connection(in)
	want := CredentialsSet{Token: true, WebhookSecret: true}
	if c.Forge != configfile.ForgeGitLab || c.Host != "gitlab.example.com" || c.HookPath != "/hooks/lab" || c.Credentials != want {
		t.Fatalf("connection = %+v", c)
	}
}
