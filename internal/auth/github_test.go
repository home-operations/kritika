package auth

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGitHubMember(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		header  map[string]string
		body    string
		want    bool
		wantErr bool
	}{
		{name: "active member", status: 200, body: `{"state":"active","role":"member"}`, want: true},
		{name: "active admin", status: 200, body: `{"state":"active","role":"admin"}`, want: true},
		{name: "pending", status: 200, body: `{"state":"pending","role":"admin"}`},
		{name: "billing manager", status: 200, body: `{"state":"active","role":"billing_manager"}`},
		{name: "not found", status: 404},
		{name: "forbidden: OAuth app access restricted", status: 403},
		{name: "forbidden: primary rate limit", status: 403, header: map[string]string{"X-RateLimit-Remaining": "0"}, wantErr: true},
		{name: "forbidden: secondary rate limit", status: 403, header: map[string]string{"Retry-After": "60"}, wantErr: true},
		{name: "too many requests", status: 429, wantErr: true},
		{name: "forbidden: secondary rate limit by its body", status: 403,
			body: `{"message":"You have exceeded a secondary rate limit.","documentation_url":"https://docs.github.com/rest/overview/rate-limits-for-the-rest-api#about-secondary-rate-limits"}`, wantErr: true},
		{name: "server error", status: 502, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/user/memberships/orgs/acme" || r.Header.Get("Authorization") != "Bearer tok" {
					t.Errorf("request %s with %q", r.URL.Path, r.Header.Get("Authorization"))
				}
				for k, v := range tt.header {
					w.Header().Set(k, v)
				}
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()
			got, err := githubAPI{}.member(t.Context(), apiClient{base: srv.URL, token: "tok", client: srv.Client()}, Identity{}, "acme")
			if tt.wantErr != (err != nil) || (err != nil && !errors.Is(err, ErrForgeAPI)) || got != tt.want {
				t.Fatalf("member = %v, %v; want %v, error %v", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

// TestGitHubGetOKNamesRateLimits: every call a sign-in cannot do without,
// not only the membership check, reports GitHub's rate limit as such.
func TestGitHubGetOKNamesRateLimits(t *testing.T) {
	for _, tt := range []struct {
		name    string
		status  int
		header  map[string]string
		body    string
		limited bool
	}{
		{"primary", 403, map[string]string{"X-RateLimit-Remaining": "0"}, "", true},
		{"secondary", 403, map[string]string{"Retry-After": "60"}, "", true},
		{"secondary by its body", 403, nil, `{"documentation_url":"https://docs.github.com/rest#abuse-rate-limits"}`, true},
		{"too many requests", 429, nil, "", true},
		{"forbidden", 403, nil, `{"message":"Resource not accessible"}`, false},
		{"server error", 502, map[string]string{"Retry-After": "60"}, "", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				for k, v := range tt.header {
					w.Header().Set(k, v)
				}
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()
			err := githubGetOK(t.Context(), apiClient{base: srv.URL, token: "tok", client: srv.Client()}, "/user", nil)
			if !errors.Is(err, ErrForgeAPI) || strings.Contains(err.Error(), "rate limited") != tt.limited {
				t.Fatalf("githubGetOK = %v, want a forge error that names a rate limit: %v", err, tt.limited)
			}
		})
	}
}

// TestGitHubMemberRefusalCutShort: a refusal whose body the connection cut
// cannot be told from a rate limit, so the membership check fails rather
// than read it as no membership.
func TestGitHubMemberRefusalCutShort(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "200")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"documentation_url":"https://docs.github.com/rest#secondary-rate`))
	}))
	defer srv.Close()
	got, err := githubAPI{}.member(t.Context(), apiClient{base: srv.URL, token: "tok", client: srv.Client()}, Identity{}, "acme")
	if got || !errors.Is(err, ErrForgeAPI) {
		t.Fatalf("member = %v, %v; want a forge error", got, err)
	}
}

// TestGitHubMappingVars: the role mapping sees every organization the user
// is an active member of and every team, across pages.
func TestGitHubMappingVars(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		switch {
		case r.URL.Path == "/user/memberships/orgs" && r.URL.Query().Get("state") == "active" && page == "1":
			items := make([]string, 0, 100)
			for i := range 100 {
				items = append(items, fmt.Sprintf(`{"state":"active","role":"member","organization":{"login":"org-%d"}}`, i))
			}
			_, _ = fmt.Fprint(w, "["+strings.Join(items, ",")+"]")
		case r.URL.Path == "/user/memberships/orgs" && page == "2":
			_, _ = fmt.Fprint(w, `[{"state":"active","role":"admin","organization":{"login":"last"}},`+
				`{"state":"active","role":"billing_manager","organization":{"login":"billing"}}]`)
		case r.URL.Path == "/user/teams" && page == "1":
			_, _ = fmt.Fprint(w, `[{"slug":"ops","organization":{"login":"org-1"}}]`)
		default:
			t.Errorf("unexpected request %s", r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	vars, err := githubAPI{}.mappingVars(t.Context(), apiClient{base: srv.URL, token: "tok", client: srv.Client()},
		Identity{Login: "alice", Email: "a@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	orgs, _ := vars["orgs"].([]string)
	teams, _ := vars["teams"].([]string)
	if len(orgs) != 101 || orgs[100] != "last" || strings.Join(teams, ",") != "org-1/ops" || vars["login"] != "alice" || vars["email"] != "a@example.com" {
		t.Fatalf("vars = %v", vars)
	}
}

func TestGitHubIdentity(t *testing.T) {
	const user = `{"id":7,"login":"alice","name":"Alice","email":"profile@example.com","avatar_url":"https://avatars.example.com/7"}`
	fromProfile := Identity{Subject: "7", Login: "alice", Email: "profile@example.com", DisplayName: "Alice", AvatarURL: "https://avatars.example.com/7"}
	verified := fromProfile
	verified.Email, verified.EmailVerified = "alice@example.com", true
	tests := []struct {
		name         string
		userStatus   int
		user         string
		emailsStatus int
		emails       string
		want         Identity
		wantErr      bool
	}{
		{name: "verified primary email", userStatus: 200, user: user, emailsStatus: 200,
			emails: `[{"email":"alice@example.com","primary":true,"verified":true}]`, want: verified},
		{name: "no scope to list addresses", userStatus: 200, user: user, emailsStatus: 404, want: fromProfile},
		{name: "no id", userStatus: 200, user: `{"id":0,"login":"alice"}`, emailsStatus: 200, emails: `[]`, wantErr: true},
		{name: "no login", userStatus: 200, user: `{"id":7}`, emailsStatus: 200, emails: `[]`, wantErr: true},
		{name: "profile unavailable", userStatus: 502, emailsStatus: 200, emails: `[]`, wantErr: true},
		{name: "unreadable addresses", userStatus: 200, user: user, emailsStatus: 200, emails: `{}`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer tok" {
					t.Errorf("request %s with %q", r.URL.Path, r.Header.Get("Authorization"))
				}
				switch r.URL.Path {
				case "/user":
					w.WriteHeader(tt.userStatus)
					_, _ = w.Write([]byte(tt.user))
				case "/user/emails":
					w.WriteHeader(tt.emailsStatus)
					_, _ = w.Write([]byte(tt.emails))
				default:
					t.Errorf("unexpected request %s", r.URL)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer srv.Close()
			got, err := githubAPI{}.identity(t.Context(), apiClient{base: srv.URL, token: "tok", client: srv.Client()})
			if tt.wantErr != (err != nil) || (err != nil && !errors.Is(err, ErrForgeAPI)) || got != tt.want {
				t.Fatalf("identity = %+v, %v; want %+v, error %v", got, err, tt.want, tt.wantErr)
			}
		})
	}
}
