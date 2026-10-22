package auth

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"testing"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/configfile/configfiletest"
)

// gitlabFile is a GitLab sign-in on gitlab.example.com beside GitLab
// connections on it and on another instance.
func gitlabFile(t *testing.T) *configfile.File {
	t.Helper()
	t.Setenv("TEST_AUTH_SECRET", "s3cret")
	t.Setenv("TEST_GL_HOOK", "whsec_c2lnbmluZy1rZXk=")
	return configfiletest.Load(t, "auth:\n"+adminPassword+
		"  gitlab:\n    host: gitlab.example.com\n    clientId: gl\n    clientSecret: { env: TEST_AUTH_SECRET }\n"+`
gitlab:
  work: { host: gitlab.example.com, accounts: [platform, alice], token: { env: TEST_AUTH_SECRET }, webhookSecret: { env: TEST_GL_HOOK } }
  other: { host: gitlab.other.com, accounts: [initech], token: { env: TEST_AUTH_SECRET }, webhookSecret: { env: TEST_GL_HOOK } }
`)
}

func TestGitLabProviderURLs(t *testing.T) {
	s, _ := gitlabFile(t).Auth.SignInByType(configfile.SignInGitLab)
	p, err := buildProvider(t.Context(), s, "https://kritika.example.com/auth/callback/gitlab", http.DefaultClient, nil)
	if err != nil {
		t.Fatal(err)
	}
	fp := p.(*forgeProvider)
	if fp.conf.Endpoint.TokenURL != "https://gitlab.example.com/oauth/token" || fp.apiBase != "https://gitlab.example.com" {
		t.Fatalf("token URL %q, API %q", fp.conf.Endpoint.TokenURL, fp.apiBase)
	}
	u, _ := url.Parse(p.AuthCodeURL("st", "", "verifier-verifier-verifier-verifier-verifier"))
	if got := u.Scheme + "://" + u.Host + u.Path; got != "https://gitlab.example.com/oauth/authorize" {
		t.Fatalf("authorize URL %q", got)
	}
	if q := u.Query(); q.Get("scope") != "openid email" || q.Get("code_challenge_method") != "S256" || q.Get("client_id") != "gl" {
		t.Fatalf("query = %v", q)
	}
	if got := signInOrigin(s); got != "gitlab:https://gitlab.example.com" {
		t.Fatalf("origin = %q", got)
	}
}

// userinfoServer answers GitLab's userinfo endpoint with body.
func userinfoServer(t *testing.T, status int, body string) apiClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/userinfo" || r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("request %s with %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		w.WriteHeader(status)
		_, _ = fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return apiClient{base: srv.URL, token: "tok", client: srv.Client()}
}

func TestGitLabIdentity(t *testing.T) {
	want := Identity{Subject: "7", Login: "alice", Email: "alice@example.com", EmailVerified: true, DisplayName: "Alice", AvatarURL: "https://gitlab.example.com/a.png"}
	for _, tt := range []struct {
		name    string
		status  int
		body    string
		want    Identity
		wantErr bool
	}{
		{"a string sub", 200, `{"sub":"7","nickname":"alice","name":"Alice","email":"alice@example.com","email_verified":true,"picture":"https://gitlab.example.com/a.png"}`, want, false},
		{"a numeric sub", 200, `{"sub":7,"nickname":"alice","name":"Alice","email":"alice@example.com","email_verified":true,"picture":"https://gitlab.example.com/a.png"}`, want, false},
		{"an unverified email", 200, `{"sub":"7","nickname":"alice","email":"alice@example.com","email_verified":false}`,
			Identity{Subject: "7", Login: "alice", Email: "alice@example.com"}, false},
		{"no nickname", 200, `{"sub":"7"}`, Identity{}, true},
		{"refused", 401, `{}`, Identity{}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := userinfoServer(t, tt.status, tt.body)
			got, err := gitlabAPI{}.identity(t.Context(), c)
			if got != tt.want || tt.wantErr != (err != nil) || (err != nil && !errors.Is(err, ErrForgeAPI)) {
				t.Fatalf("identity = %+v, %v; want %+v", got, err, tt.want)
			}
		})
	}
}

// TestGitLabMember: a top-level group is the user's when GitLab lists it,
// in any case; a subgroup's membership alone does not make its parent's.
func TestGitLabMember(t *testing.T) {
	c := userinfoServer(t, 200, `{"sub":"7","nickname":"alice","groups":["Platform","platform/backend","other/sub"]}`)
	for group, want := range map[string]bool{"platform": true, "other": false, "initech": false} {
		if got, err := (gitlabAPI{}).member(t.Context(), c, Identity{Login: "alice"}, group); err != nil || got != want {
			t.Fatalf("member(%s) = %v, %v; want %v", group, got, err, want)
		}
	}
	vars, err := gitlabAPI{}.mappingVars(t.Context(), c, Identity{Login: "alice", Email: "a@example.com"})
	if groups, _ := vars["groups"].([]string); err != nil || !slices.Equal(groups, []string{"Platform", "platform/backend", "other/sub"}) || vars["login"] != "alice" {
		t.Fatalf("vars = %v, %v", vars, err)
	}
	if vars["email"] != "" {
		t.Fatalf("email = %q, want none for an unverified address", vars["email"])
	}
	if vars, _ := (gitlabAPI{}).mappingVars(t.Context(), c, Identity{Login: "alice", Email: "a@example.com", EmailVerified: true}); vars["email"] != "a@example.com" {
		t.Fatalf("a verified address = %q", vars["email"])
	}
}

// TestGrantGitLabInstance: a GitLab sign-in places a person on the groups
// its own instance's connections serve, never another instance's.
func TestGrantGitLabInstance(t *testing.T) {
	file := gitlabFile(t)
	s, _ := file.Auth.SignInByType(configfile.SignInGitLab)
	o := &orgs{member: map[string]bool{"platform": true, "initech": true}}
	g, err := grant(t.Context(), file, s, Identity{Login: "alice"}, Facts{Membership: o.membership()})
	if err != nil || !slices.Equal(g.Accounts, []string{"gitlab/alice", "gitlab/platform"}) || slices.Contains(o.calls, "initech") {
		t.Fatalf("grant = %+v, %v; asked %v", g, err, o.calls)
	}
}
