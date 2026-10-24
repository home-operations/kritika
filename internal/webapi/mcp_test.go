package webapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/home-operations/kritika/internal/auth"
	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/configfile/configfiletest"
)

// TestScopeAccount: a tool's repository or account names an account by its
// name alone, and forge settles a name the principal reads on two forges.
func TestScopeAccount(t *testing.T) {
	t.Setenv("KRITIKA_TEST_TOKEN", "tok")
	f := configfiletest.Load(t, testConfig+`
gitea:
  tea: { host: gitea.example.com, accounts: [alpha, gamma], token: { env: KRITIKA_TEST_TOKEN }, webhookSecret: { env: KRITIKA_TEST_TOKEN } }
`)
	member := &auth.Principal{Accounts: map[string]bool{configfile.AccountID(configfile.ForgeGitHub, "alpha"): true}}
	everyone := &auth.Principal{AllAccounts: true}
	tests := []struct {
		name    string
		p       *auth.Principal
		args    scopeArgs
		want    string
		wantErr ErrorCode
	}{
		{"a repository names its owner", member, scopeArgs{Repository: "alpha/x"}, "github/alpha", ""},
		{"whatever its case", member, scopeArgs{Repository: "ALPHA/x"}, "github/alpha", ""},
		{"an account by name", everyone, scopeArgs{Account: "gamma"}, "gitea/gamma", ""},
		{"a name read on two forges", everyone, scopeArgs{Repository: "alpha/x"}, "", CodeAmbiguous},
		{"forge settles it", everyone, scopeArgs{Repository: "alpha/x", Forge: "gitea"}, "gitea/alpha", ""},
		{"an account on a forge not read", member, scopeArgs{Account: "alpha", Forge: "gitea"}, "", CodeNotFound},
		{"an account not read", member, scopeArgs{Account: "beta"}, "", CodeNotFound},
		{"an unknown account", everyone, scopeArgs{Account: "nope"}, "", CodeNotFound},
		{"a repository of another account", everyone, scopeArgs{Repository: "alpha/x", Account: "gamma"}, "", CodeBadRequest},
		{"nothing named", everyone, scopeArgs{Forge: "github"}, "", CodeBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := scopeAccount(f, tt.p, tt.args)
			if tt.wantErr != "" {
				if e, ok := errors.AsType[*apiError](err); !ok || e.code != tt.wantErr {
					t.Fatalf("err = %v, want %s", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("scopeAccount: %v", err)
			}
			if got.account.Slug() != tt.want {
				t.Errorf("account = %s, want %s", got.account.Slug(), tt.want)
			}
		})
	}
}

// TestToolEndsWithItsRequest: a tool's read stops once the request that
// called it ends, though its context, as the SDK hands it over, never does.
func TestToolEndsWithItsRequest(t *testing.T) {
	ts := newTestServer(t, "https://kritika.example")
	request, cancel := context.WithCancel(t.Context())
	defer cancel()
	ctx := auth.WithPrincipal(context.WithoutCancel(context.WithValue(request, requestKey{}, request)), &auth.Principal{AllAccounts: true})
	read := func(ctx context.Context, t *accountScope, _ scopeArgs) (string, error) {
		cancel()
		<-ctx.Done()
		return t.account.Name, ctx.Err()
	}
	done := make(chan error, 1)
	go func() {
		_, _, err := tool(ts.srv, read)(ctx, &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: "list_rules"}}, scopeArgs{Account: "alpha"})
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || err.Error() != "internal error" {
			t.Fatalf("err = %v, want an internal error", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the read outlived its request")
	}
}

// TestMCPNeedsToken: the endpoint, under the base path too, answers a
// signed-in request without a token with a bearer challenge.
func TestMCPNeedsToken(t *testing.T) {
	for _, tt := range []struct{ webURL, path string }{
		{"https://kritika.example", "/mcp"},
		{"https://kritika.example/dash/", "/dash/mcp"},
	} {
		t.Run(tt.path, func(t *testing.T) {
			ts := newTestServer(t, tt.webURL)
			r := httptest.NewRequest(http.MethodPost, tt.path, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
			w := ts.as(&auth.Principal{Admin: true}, r)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401: %s", w.Code, w.Body)
			}
			if got := w.Header().Get("WWW-Authenticate"); got != `Bearer realm="kritika"` {
				t.Errorf("WWW-Authenticate = %q", got)
			}
			if got := w.Header().Get("Cache-Control"); got != "no-store" {
				t.Errorf("Cache-Control = %q, want no-store", got)
			}
		})
	}
}
