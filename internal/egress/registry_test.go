package egress

import (
	"cmp"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/home-operations/kritika/internal/egresspolicy"
)

// TestRegistryCredential: a run of an account with a registry's oci
// credential reads through the gateway's answer to the registry's challenge.
// The token is fetched once and kept, sent to the registry's own host alone,
// and never shown to the runner. A write, another account's run, a token
// endpoint elsewhere and a redirect are each kept to what the runner could
// do without the credential.
func TestRegistryCredential(t *testing.T) {
	const challenge = `Bearer realm="https://registry.example.com/v2/token",service="registry.example.com",scope="repository:app:pull"`
	for _, tt := range []struct {
		name    string
		method  string
		account string
		// path is what the runner asks for, the manifest when it is empty.
		path string
		// realm is the challenge the registry answers an unsigned request with.
		realm string
		// manifest is what the registry answers a signed read with.
		manifest func(w http.ResponseWriter)
		// reads is how many times the request is sent.
		reads      int
		wantStatus int
		wantBody   string
		// wantCalls are what reached the registry, in order.
		wantCalls []string
		// wantHeaders are response headers the runner gets.
		wantHeaders http.Header
	}{
		{
			name: "signed read", method: http.MethodGet, account: "acme", realm: challenge, reads: 2,
			wantStatus: http.StatusOK, wantBody: "manifest",
			wantCalls: []string{
				"GET registry.example.com/v2/app/manifests/1 ",
				"GET registry.example.com/v2/token?scope=repository%3Aapp%3Apull&service=registry.example.com Basic Ym90OnB3",
				"GET registry.example.com/v2/app/manifests/1 Bearer tok-1",
				"GET registry.example.com/v2/app/manifests/1 Bearer tok-1",
			},
		},
		{
			name: "a write", method: http.MethodPut, account: "acme", realm: challenge, reads: 1,
			wantStatus: http.StatusForbidden, wantBody: "a registry with a credential takes GET and HEAD alone",
		},
		{
			name: "another account's run", method: http.MethodGet, account: "other", realm: challenge, reads: 1,
			wantStatus: http.StatusUnauthorized,
			wantCalls:  []string{"GET registry.example.com/v2/app/manifests/1 "},
		},
		{
			name: "a token endpoint elsewhere", method: http.MethodGet, account: "acme", reads: 1,
			realm:      `Bearer realm="https://auth.example.com/token",service="registry.example.com",scope="repository:app:pull"`,
			wantStatus: http.StatusBadGateway, wantBody: "its credential goes to https://registry.example.com alone",
			wantCalls: []string{"GET registry.example.com/v2/app/manifests/1 "},
		},
		{
			name: "the token endpoint", method: http.MethodGet, account: "acme", realm: challenge, reads: 1,
			path:       "/v2/token?scope=repository%3Aapp%3Apull&service=registry.example.com",
			wantStatus: http.StatusUnauthorized,
			wantCalls:  []string{"GET registry.example.com/v2/token?scope=repository%3Aapp%3Apull&service=registry.example.com "},
		},
		{
			name: "a token endpoint among the reads", method: http.MethodGet, account: "acme", reads: 2,
			path:       "/v2/app/manifests/token",
			realm:      `Bearer realm="https://registry.example.com/v2/app/manifests/token",service="registry.example.com"`,
			wantStatus: http.StatusBadGateway, wantBody: "names a token endpoint among the reads that the gateway signs in",
			wantCalls: []string{"GET registry.example.com/v2/app/manifests/token ", "GET registry.example.com/v2/app/manifests/token "},
		},
		{
			name: "a token endpoint among the reads, written otherwise", method: http.MethodGet, account: "acme", reads: 1,
			path:       "/v2/app/manifests/token",
			realm:      `Bearer realm = "https://registry.example.com/v2/APP/manifests/./%74oken;x",service="registry.example.com"`,
			wantStatus: http.StatusBadGateway, wantBody: "names a token endpoint among the reads that the gateway signs in",
			wantCalls: []string{"GET registry.example.com/v2/app/manifests/token "},
		},
		{
			name: "a read that asks for the credential", method: http.MethodGet, account: "acme", reads: 1,
			path:       "/v2/app/manifests/token",
			realm:      `Basic realm="registry token service"`,
			wantStatus: http.StatusBadGateway, wantBody: "asks for its credential on a read",
			wantCalls: []string{"GET registry.example.com/v2/app/manifests/token "},
		},
		{
			name: "dot segments to the token endpoint", method: http.MethodGet, account: "acme", realm: challenge, reads: 1,
			path:       "/v2/x/manifests/../../token",
			wantStatus: http.StatusUnauthorized,
			wantCalls:  []string{"GET registry.example.com/v2/x/manifests/../../token "},
		},
		{
			name: "the registry's own URLs", method: http.MethodGet, account: "acme", realm: challenge, reads: 1,
			manifest: func(w http.ResponseWriter) {
				w.Header().Set("Location", "https://registry.example.com:443/v2/app/blobs/sha256:a")
				w.Header().Set("Link", `<https://registry.example.com/v2/app/tags/list?last=1>; rel="next"`)
				w.WriteHeader(http.StatusTemporaryRedirect)
			},
			wantStatus: http.StatusTemporaryRedirect,
			wantCalls: []string{
				"GET registry.example.com/v2/app/manifests/1 ",
				"GET registry.example.com/v2/token?scope=repository%3Aapp%3Apull&service=registry.example.com Basic Ym90OnB3",
				"GET registry.example.com/v2/app/manifests/1 Bearer tok-1",
			},
			wantHeaders: http.Header{
				"Location": {"http://registry.example.com/v2/app/blobs/sha256:a"},
				"Link":     {`<http://registry.example.com/v2/app/tags/list?last=1>; rel="next"`},
			},
		},
		{
			name: "a redirect", method: http.MethodGet, account: "acme", realm: challenge, reads: 1,
			manifest: func(w http.ResponseWriter) {
				w.Header().Set("Location", "https://blobs.example.com/app")
				w.WriteHeader(http.StatusTemporaryRedirect)
			},
			wantStatus:  http.StatusTemporaryRedirect,
			wantHeaders: http.Header{"Location": {"https://blobs.example.com/app"}},
			wantCalls: []string{
				"GET registry.example.com/v2/app/manifests/1 ",
				"GET registry.example.com/v2/token?scope=repository%3Aapp%3Apull&service=registry.example.com Basic Ym90OnB3",
				"GET registry.example.com/v2/app/manifests/1 Bearer tok-1",
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var mu sync.Mutex
			var calls []string
			registry := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				calls = append(calls, r.Method+" "+r.Host+r.URL.RequestURI()+" "+r.Header.Get("Authorization"))
				mu.Unlock()
				switch {
				case r.URL.Path == "/v2/token" && r.Header.Get("Authorization") != "Basic Ym90OnB3":
					w.Header().Set("WWW-Authenticate", `Basic realm="registry.example.com"`)
					w.WriteHeader(http.StatusUnauthorized)
				case r.URL.Path == "/v2/token":
					_, _ = io.WriteString(w, `{"token":"tok-1"}`)
				case r.Header.Get("Authorization") != "Bearer tok-1":
					w.Header().Set("WWW-Authenticate", tt.realm)
					w.WriteHeader(http.StatusUnauthorized)
				case tt.manifest != nil:
					tt.manifest(w)
				default:
					_, _ = io.WriteString(w, "manifest")
				}
			})
			var refused []string
			p := &Proxy{
				Rules: func() egresspolicy.Rules {
					return egresspolicy.Rules{Allow: hosts("registry.example.com", "auth.example.com"), Credentials: map[string]egresspolicy.Credential{
						"registry.example.com": {Scheme: egresspolicy.SchemeOCI, Username: "bot", Secret: "pw", Accounts: []string{"acme"}},
					}}
				},
				Logger:  slog.New(slog.DiscardHandler),
				Refused: func(_ context.Context, _, _, reason string) { refused = append(refused, reason) },
				Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
					w := httptest.NewRecorder()
					registry.ServeHTTP(w, r)
					resp := w.Result()
					resp.Request = r
					return resp, nil
				}),
			}
			for range tt.reads {
				r := httptest.NewRequest(tt.method, "http://registry.example.com"+cmp.Or(tt.path, "/v2/app/manifests/1"), nil)
				r.Header.Set("Authorization", "Bearer from-the-runner")
				r = r.WithContext(ForAccount(r.Context(), tt.account))
				w := httptest.NewRecorder()
				p.ServeHTTP(w, r)
				if w.Code != tt.wantStatus || !strings.Contains(w.Body.String(), tt.wantBody) {
					t.Fatalf("status %d, body %q; want %d, %q", w.Code, w.Body.String(), tt.wantStatus, tt.wantBody)
				}
				if strings.Contains(w.Body.String(), "tok-1") || strings.Contains(w.Header().Get("Authorization"), "tok-1") {
					t.Fatalf("the runner was shown the token: %q", w.Body.String())
				}
				for name, want := range tt.wantHeaders {
					if got := w.Header().Values(name); !slices.Equal(got, want) {
						t.Errorf("%s = %q, want %q", name, got, want)
					}
				}
			}
			if !slices.Equal(calls, tt.wantCalls) {
				t.Errorf("calls = %q, want %q", calls, tt.wantCalls)
			}
			if tt.method != http.MethodGet && len(refused) != 1 {
				t.Errorf("refused = %q, want the write", refused)
			}
		})
	}
}

func TestRepositoryOf(t *testing.T) {
	for raw, want := range map[string]string{
		"/v2/app/manifests/1.2.3":                  "app",
		"/v2/org/app/blobs/sha256:abc123":          "org/app",
		"/v2/org/blobs/app/manifests/1":            "org/blobs/app",
		"/v2/org/app/tags/list":                    "org/app",
		"/v2/org/app/referrers/sha256:abc123":      "org/app",
		"/v2/org/my-app__x.y/manifests/sha256:abc": "org/my-app__x.y",
		"/v2/":                                         "",
		"/v2/token":                                    "",
		"/v2/manifests/1":                              "",
		"/v2/org/app/blobs/uploads/":                   "",
		"/v2/org/app/tags/list/more":                   "",
		"/v2/Org/app/manifests/1":                      "",
		"/v2/x/manifests/../../token":                  "",
		"/v2/x/../token/manifests/1":                   "",
		"/v2/x/manifests/%2e%2e/%2e%2e/token":          "",
		"/v2/x%2Fy/manifests/1":                        "",
		"/v2/x/manifests/..;/..;/token":                "",
		"/v2/x/manifests/.hidden":                      "",
		"//v2/x/manifests/1":                           "",
		"/api/packages/org/helm/index.yaml":            "",
		"/v2/org/app/manifests/1?scope=x":              "org/app",
		"/v2/org//app/manifests/1":                     "",
		"/v2/org/app/blobs/sha256:abc/../../../../x/y": "",
	} {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if got := repositoryOf(u); got != want {
			t.Errorf("repositoryOf(%q) = %q, want %q", raw, got, want)
		}
	}
}

// TestRegistryBasicChallenge: a read that the registry answers with a
// Basic challenge fails alone, and the reads after it keep their tokens.
func TestRegistryBasicChallenge(t *testing.T) {
	var calls []string
	registry := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.Path+" "+r.Header.Get("Authorization"))
		switch {
		case r.URL.Path == "/v2/token":
			_, _ = io.WriteString(w, `{"token":"tok-1"}`)
		case r.URL.Path == "/v2/other/manifests/1":
			w.Header().Set("WWW-Authenticate", `Basic realm="registry"`)
			w.WriteHeader(http.StatusUnauthorized)
		case r.Header.Get("Authorization") != "Bearer tok-1":
			w.Header().Set("WWW-Authenticate", `Bearer realm="https://registry.example.com/v2/token",service="registry.example.com"`)
			w.WriteHeader(http.StatusUnauthorized)
		default:
			_, _ = io.WriteString(w, "manifest")
		}
	})
	p := &Proxy{
		Rules: func() egresspolicy.Rules {
			return egresspolicy.Rules{Allow: hosts("registry.example.com"), Credentials: map[string]egresspolicy.Credential{
				"registry.example.com": {Scheme: egresspolicy.SchemeOCI, Username: "bot", Secret: "pw", Accounts: []string{egresspolicy.AllAccounts}},
			}}
		},
		Logger: slog.New(slog.DiscardHandler),
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			w := httptest.NewRecorder()
			registry.ServeHTTP(w, r)
			resp := w.Result()
			resp.Request = r
			return resp, nil
		}),
	}
	for _, step := range []struct {
		path string
		want int
	}{
		{"/v2/app/manifests/1", http.StatusOK},
		{"/v2/other/manifests/1", http.StatusBadGateway},
		{"/v2/app/manifests/1", http.StatusOK},
	} {
		w := httptest.NewRecorder()
		p.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://registry.example.com"+step.path, nil))
		if w.Code != step.want {
			t.Fatalf("%s: status %d, body %q; want %d", step.path, w.Code, w.Body.String(), step.want)
		}
	}
	for _, call := range calls {
		if strings.Contains(call, "Basic") && !strings.HasPrefix(call, "/v2/token ") {
			t.Errorf("the credential went with a read: %q", calls)
		}
	}
}

// TestRegistryCredentialChange: a credential that changes takes a new
// client, so a token granted to the old one is not sent for the new one.
func TestRegistryCredentialChange(t *testing.T) {
	p := &Proxy{Logger: slog.New(slog.DiscardHandler)}
	old := egresspolicy.Credential{Scheme: egresspolicy.SchemeOCI, Username: "bot", Secret: "pw"}
	first := p.registryTransport("registry.example.com", old)
	if again := p.registryTransport("registry.example.com", old); again != first {
		t.Fatal("the same credential took a new client")
	}
	changed := old
	changed.Secret = "pw-2"
	if next := p.registryTransport("registry.example.com", changed); next == first {
		t.Fatal("a changed credential kept the old client")
	}
}

func TestPlainOwnURLs(t *testing.T) {
	h := http.Header{
		"Location": {"https://REGISTRY.example.com/v2/app/blobs/sha256:a?x=1"},
		"Link": {
			`<https://registry.example.com/v2/a/tags/list?last=1>; rel="next", <https://registry.example.com:8443/v2/a>; rel="other"`,
			`</v2/a/tags/list?last=2>; rel="next"`,
			`<https://cdn.example.com/registry.example.com/x>; rel="next"`,
			`<http://registry.example.com/v2/a>; rel="next"`,
		},
	}
	plainOwnURLs(h, "registry.example.com")
	want := http.Header{
		"Location": {"http://registry.example.com/v2/app/blobs/sha256:a?x=1"},
		"Link": {
			`<http://registry.example.com/v2/a/tags/list?last=1>; rel="next", <https://registry.example.com:8443/v2/a>; rel="other"`,
			`</v2/a/tags/list?last=2>; rel="next"`,
			`<https://cdn.example.com/registry.example.com/x>; rel="next"`,
			`<http://registry.example.com/v2/a>; rel="next"`,
		},
	}
	for name := range want {
		if !slices.Equal(h[name], want[name]) {
			t.Errorf("%s = %q, want %q", name, h[name], want[name])
		}
	}
}
