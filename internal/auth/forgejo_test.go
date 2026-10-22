package auth

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/configfile/configfiletest"
)

// forgejoInstances are two Forgejo connections on different instances,
// each serving an account the other's instance also has.
const forgejoInstances = `
forgejo:
  home: { host: forge.example.com, accounts: [acme, alice], privateKey: { env: TEST_AUTH_SECRET }, webhookSecret: { env: TEST_AUTH_SECRET } }
  other: { host: other.example.com, accounts: [initech], privateKey: { env: TEST_AUTH_SECRET }, webhookSecret: { env: TEST_AUTH_SECRET } }
`

const forgejoSignIn = "auth:\n" + adminPassword +
	"  forgejo:\n    host: forge.example.com\n    clientId: fj\n    clientSecret: { env: TEST_AUTH_SECRET }\n"

func forgejoFile(t *testing.T) *configfile.File {
	t.Helper()
	t.Setenv("TEST_AUTH_SECRET", "s3cret")
	return configfiletest.Load(t, forgejoSignIn+forgejoInstances)
}

func TestForgejoProviderURLs(t *testing.T) {
	s, _ := forgejoFile(t).Auth.SignInByType(configfile.SignInForgejo)
	p, err := buildProvider(t.Context(), s, "https://kritika.example.com/auth/callback/forgejo", http.DefaultClient, nil)
	if err != nil {
		t.Fatal(err)
	}
	fp := p.(*forgeProvider)
	if fp.conf.Endpoint.TokenURL != "https://forge.example.com/login/oauth/access_token" || fp.apiBase != "https://forge.example.com/api/v1" {
		t.Fatalf("token URL %q, API %q", fp.conf.Endpoint.TokenURL, fp.apiBase)
	}
	u, _ := url.Parse(p.AuthCodeURL("st", "", "verifier-verifier-verifier-verifier-verifier"))
	if got := u.Scheme + "://" + u.Host + u.Path; got != "https://forge.example.com/login/oauth/authorize" {
		t.Fatalf("authorize URL %q", got)
	}
	if q := u.Query(); q.Get("scope") != "read:user read:organization" || q.Get("code_challenge_method") != "S256" || q.Get("client_id") != "fj" {
		t.Fatalf("query = %v", q)
	}
	if got := signInOrigin(s); got != "forgejo:https://forge.example.com" {
		t.Fatalf("origin = %q", got)
	}
}

func TestForgejoIdentity(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/user":
			_, _ = fmt.Fprint(w, `{"id":7,"login":"alice","full_name":"Alice","email":"profile@example.com","avatar_url":"https://forge.example.com/avatars/7"}`)
		case "/user/emails":
			_, _ = fmt.Fprint(w, `[{"email":"alice@example.com","primary":true,"verified":true}]`)
		default:
			t.Errorf("unexpected request %s", r.URL)
		}
	}))
	defer srv.Close()
	id, err := forgejoAPI{}.identity(t.Context(), apiClient{base: srv.URL, token: "tok", client: srv.Client()})
	want := Identity{Subject: "7", Login: "alice", Email: "alice@example.com", EmailVerified: true, DisplayName: "Alice", AvatarURL: "https://forge.example.com/avatars/7"}
	if err != nil || id != want {
		t.Fatalf("identity = %+v, %v", id, err)
	}
}

func TestForgejoMember(t *testing.T) {
	for _, tt := range []struct {
		name    string
		status  int
		want    bool
		wantErr bool
	}{
		{"a member", http.StatusNoContent, true, false},
		{"not a member", http.StatusNotFound, false, false},
		{"refused", http.StatusForbidden, false, false},
		{"unavailable", http.StatusBadGateway, false, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/orgs/acme/members/alice" || r.Header.Get("Authorization") != "Bearer tok" {
					t.Errorf("request %s with %q", r.URL.Path, r.Header.Get("Authorization"))
				}
				w.WriteHeader(tt.status)
			}))
			defer srv.Close()
			got, err := forgejoAPI{}.member(t.Context(), apiClient{base: srv.URL, token: "tok", client: srv.Client()}, Identity{Login: "alice"}, "acme")
			if got != tt.want || tt.wantErr != (err != nil) || (err != nil && !errors.Is(err, ErrForgeAPI)) {
				t.Fatalf("member = %v, %v", got, err)
			}
		})
	}
}

// TestForgejoMappingVars: the role mapping sees every organization and
// team, across pages, as long as the total the instance counts says.
func TestForgejoMappingVars(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		if r.URL.Query().Get("limit") != "50" {
			t.Errorf("limit = %q", r.URL.Query().Get("limit"))
		}
		switch {
		case r.URL.Path == "/user/orgs" && page == "1":
			// An instance capping pages at 2 sends fewer than asked.
			w.Header().Set("X-Total-Count", "3")
			_, _ = fmt.Fprint(w, `[{"name":"org-1"},{"name":"org-2"}]`)
		case r.URL.Path == "/user/orgs" && page == "2":
			w.Header().Set("X-Total-Count", "3")
			_, _ = fmt.Fprint(w, `[{"name":"org-3"}]`)
		case r.URL.Path == "/user/teams" && page == "1":
			_, _ = fmt.Fprint(w, `[{"name":"Owners","organization":{"name":"org-1"}}]`)
		default:
			t.Errorf("unexpected request %s", r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	vars, err := forgejoAPI{}.mappingVars(t.Context(), apiClient{base: srv.URL, token: "tok", client: srv.Client()},
		Identity{Login: "alice", Email: "a@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	orgs, _ := vars["orgs"].([]string)
	teams, _ := vars["teams"].([]string)
	if strings.Join(orgs, ",") != "org-1,org-2,org-3" || strings.Join(teams, ",") != "org-1/Owners" || vars["login"] != "alice" {
		t.Fatalf("vars = %v", vars)
	}
	// An address the instance has not verified is not the mapping's to
	// grant on.
	if vars["email"] != "" {
		t.Fatalf("email = %q, want none for an unverified address", vars["email"])
	}
}

// TestForgejoMappingVarsTooMany: a list longer than a sign-in reads fails
// it, rather than mapping on part of it.
func TestForgejoMappingVarsTooMany(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Total-Count", "5000")
		_, _ = fmt.Fprint(w, "["+strings.TrimSuffix(strings.Repeat(`{"name":"o"},`, forgejoPageSize), ",")+"]")
	}))
	defer srv.Close()
	_, err := forgejoAPI{}.mappingVars(t.Context(), apiClient{base: srv.URL, token: "tok", client: srv.Client()}, Identity{Login: "alice"})
	if !errors.Is(err, ErrForgeAPI) || !strings.Contains(err.Error(), "more than") {
		t.Fatalf("mappingVars = %v, want a refusal", err)
	}
	if got := verifiedOnly(Identity{Email: "a@example.com", EmailVerified: true}); got != "a@example.com" {
		t.Fatalf("a verified address = %q", got)
	}
}

// TestForgejoSessionEndsWhenAnAccountMoves: a session of a Forgejo sign-in
// rests on the accounts its instance serves, so moving one to another
// instance ends it; a change on another instance does not.
func TestForgejoSessionEndsWhenAnAccountMoves(t *testing.T) {
	key, _ := GrantKey(forgejoFile(t), "forgejo")
	moved := configfiletest.Load(t, forgejoSignIn+strings.NewReplacer("[acme, alice]", "[alice]", "[initech]", "[initech, acme]").Replace(forgejoInstances))
	if k, _ := GrantKey(moved, "forgejo"); k == key {
		t.Fatal("moving acme to another instance kept the grant key")
	}
	elsewhere := configfiletest.Load(t, forgejoSignIn+strings.Replace(forgejoInstances, "[initech]", "[initech, globex]", 1))
	if k, _ := GrantKey(elsewhere, "forgejo"); k != key {
		t.Fatal("an account added on another instance changed the grant key")
	}
}

// TestGrantForgejoInstance: a Forgejo sign-in places a person on the
// accounts its own instance's connections serve, never on another
// instance's account of a name they also belong to.
func TestGrantForgejoInstance(t *testing.T) {
	file := forgejoFile(t)
	s, _ := file.Auth.SignInByType(configfile.SignInForgejo)
	o := &orgs{member: map[string]bool{"acme": true, "initech": true}}
	g, err := grant(t.Context(), file, s, Identity{Login: "alice"}, Facts{Membership: o.membership()})
	if err != nil {
		t.Fatal(err)
	}
	if g.Role != RoleMember || g.AllAccounts || !slices.Equal(g.Accounts, []string{"forgejo/acme", "forgejo/alice"}) {
		t.Fatalf("grant = %+v", g)
	}
	if slices.Contains(o.calls, "initech") {
		t.Fatalf("asked about another instance's account: %v", o.calls)
	}
}

// TestGiteaSignIn: a Gitea sign-in is Forgejo's at its own instance, and
// places people on the accounts Gitea connections serve there.
func TestGiteaSignIn(t *testing.T) {
	t.Setenv("TEST_AUTH_SECRET", "s3cret")
	file := configfiletest.Load(t, "auth:\n"+adminPassword+
		"  gitea:\n    host: gitea.example.com\n    clientId: gt\n    clientSecret: { env: TEST_AUTH_SECRET }\n"+
		"gitea:\n  tea: { host: gitea.example.com, accounts: [acme], token: { env: TEST_AUTH_SECRET }, webhookSecret: { env: TEST_AUTH_SECRET } }\n"+
		forgejoInstances)
	s, _ := file.Auth.SignInByType(configfile.SignInGitea)
	p, err := buildProvider(t.Context(), s, "https://kritika.example.com/auth/callback/gitea", http.DefaultClient, nil)
	if err != nil {
		t.Fatal(err)
	}
	if fp := p.(*forgeProvider); fp.conf.Endpoint.AuthURL != "https://gitea.example.com/login/oauth/authorize" || fp.apiBase != "https://gitea.example.com/api/v1" {
		t.Fatalf("auth URL %q, API %q", fp.conf.Endpoint.AuthURL, fp.apiBase)
	}
	if got := signInOrigin(s); got != "gitea:https://gitea.example.com" {
		t.Fatalf("origin = %q", got)
	}
	o := &orgs{member: map[string]bool{"acme": true}}
	g, err := grant(t.Context(), file, s, Identity{Login: "alice"}, Facts{Membership: o.membership()})
	if err != nil || !slices.Equal(g.Accounts, []string{"gitea/acme"}) {
		t.Fatalf("grant = %+v, %v; want the Gitea account alone", g, err)
	}
}
