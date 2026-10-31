package github

import (
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

type authRoundTripper func(*http.Request) (*http.Response, error)

func (f authRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestAuthenticationOrigins(t *testing.T) {
	_, pemKey := testKeyPEM(t)
	for _, kind := range []string{"app", "installation"} {
		for _, tt := range []struct {
			name, base, target string
			allowed            bool
		}{
			{"GitHub REST", "", "https://api.github.com/app", true},
			{"GitHub GraphQL", "", "https://api.github.com/graphql", true},
			{"explicit default port and host case", "", "https://API.GITHUB.COM:443/graphql", true},
			{"different port", "", "https://api.github.com:444/graphql", false},
			{"downgrade", "", "http://api.github.com/graphql", false},
			{"subdomain", "", "https://other.api.github.com/graphql", false},
			{"host suffix", "", "https://api.github.com.example.org/graphql", false},
			{"unconfigured upload origin", "", "https://uploads.github.com/repos/o/r/releases/1/assets", false},
			{"Enterprise REST", "https://ghe.example.org:8443/api/v3", "https://ghe.example.org:8443/api/v3/app", true},
			{"Enterprise GraphQL", "https://ghe.example.org:8443/api/v3", "https://ghe.example.org:8443/api/graphql", true},
			{"Enterprise different port", "https://ghe.example.org:8443", "https://ghe.example.org/api/graphql", false},
			{"Enterprise to public GitHub", "https://ghe.example.org", "https://api.github.com/graphql", false},
			{"HTTP default port", "http://ghe.example.org:80", "http://ghe.example.org/api/graphql", true},
		} {
			t.Run(kind+"/"+tt.name, func(t *testing.T) {
				var auth string
				calls := 0
				app, err := newApp("Iv1.test", pemKey, tt.base, authRoundTripper(func(r *http.Request) (*http.Response, error) {
					calls++
					auth = r.Header.Get("Authorization")
					return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{}`)), Request: r}, nil
				}))
				if err != nil {
					t.Fatal(err)
				}
				api := app.apps
				if kind == "installation" {
					tokens := app.InstallationTokens(7)
					if tt.allowed {
						tokens.tok, tokens.exp = "ghs_test", time.Now().Add(time.Hour)
					}
					api, err = app.Client(tokens)
					if err != nil {
						t.Fatal(err)
					}
				}
				req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, tt.target, nil)
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Authorization", "Bearer inherited")
				resp, err := api.Client().Do(req)
				if err != nil {
					t.Fatal(err)
				}
				_ = resp.Body.Close()
				if calls != 1 {
					t.Fatalf("sent %d requests, want one without token minting", calls)
				}
				if !tt.allowed && auth != "" || tt.allowed && !strings.HasPrefix(auth, "Bearer ") || auth == "Bearer inherited" {
					t.Errorf("Authorization = %q, allowed = %v", auth, tt.allowed)
				}
				if req.Header.Get("Authorization") != "Bearer inherited" {
					t.Error("transport mutated the caller's headers")
				}
			})
		}
	}
}

func TestAuthenticationRedirects(t *testing.T) {
	_, pemKey := testKeyPEM(t)
	for _, kind := range []string{"app", "installation"} {
		for _, same := range []bool{true, false} {
			name := "cross origin"
			if same {
				name = "same origin"
			}
			t.Run(kind+"/"+name, func(t *testing.T) {
				var initial, redirected string
				destination := func(w http.ResponseWriter, r *http.Request) {
					redirected = r.Header.Get("Authorization")
					_, _ = io.WriteString(w, `{"data":{"repository":null}}`)
				}
				foreign := httptest.NewServer(http.HandlerFunc(destination))
				defer foreign.Close()
				mux := http.NewServeMux()
				mux.HandleFunc("/api/graphql", func(w http.ResponseWriter, r *http.Request) {
					initial = r.Header.Get("Authorization")
					target := "/redirected"
					if !same {
						target = foreign.URL + target
					}
					http.Redirect(w, r, target, http.StatusTemporaryRedirect)
				})
				mux.HandleFunc("/redirected", destination)
				origin := httptest.NewServer(mux)
				defer origin.Close()
				app, err := NewApp("Iv1.test", pemKey, origin.URL+"/api/v3")
				if err != nil {
					t.Fatal(err)
				}
				if kind == "installation" {
					tokens := app.InstallationTokens(7)
					tokens.tok, tokens.exp = "ghs_test", time.Now().Add(time.Hour)
					client, err := NewClient(app, 7)
					if err != nil {
						t.Fatal(err)
					}
					if _, _, err := client.ThreadResolved(t.Context(), "o", "r", 7, 11); err != nil {
						t.Fatal(err)
					}
				} else {
					req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, origin.URL+"/api/graphql", nil)
					if err != nil {
						t.Fatal(err)
					}
					resp, err := app.apps.Client().Do(req)
					if err != nil {
						t.Fatal(err)
					}
					_ = resp.Body.Close()
				}
				if !strings.HasPrefix(initial, "Bearer ") {
					t.Fatal("configured origin did not receive credentials")
				}
				if same && redirected != initial || !same && redirected != "" {
					t.Fatalf("redirect Authorization = %q, same origin = %v", redirected, same)
				}
			})
		}
	}
}

// A request sent again after a rate limit is authorized again. So it carries
// the token that the installation holds after the wait, not the one it went
// out with.
func TestRateLimitRetryIsAuthorizedAgain(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, pemKey := testKeyPEM(t)
		var tokens *InstallationTokens
		var sent []string
		app, err := newApp("Iv1.test", pemKey, "", authRoundTripper(func(r *http.Request) (*http.Response, error) {
			sent = append(sent, r.Header.Get("Authorization"))
			if len(sent) > 1 {
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{}`)), Request: r}, nil
			}
			tokens.mu.Lock()
			tokens.tok = "ghs_refreshed"
			tokens.mu.Unlock()
			return &http.Response{
				StatusCode: http.StatusTooManyRequests, Header: http.Header{"Retry-After": {"30"}}, Body: http.NoBody, Request: r,
			}, nil
		}))
		if err != nil {
			t.Fatal(err)
		}
		tokens = app.InstallationTokens(7)
		tokens.tok, tokens.exp = "ghs_first", time.Now().Add(time.Hour)
		api, err := app.Client(tokens)
		if err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://api.github.com/repos/o/r", nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := api.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if want := []string{"Bearer ghs_first", "Bearer ghs_refreshed"}; !slices.Equal(sent, want) {
			t.Fatalf("sent %q, want %q", sent, want)
		}
	})
}
