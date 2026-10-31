//go:build integration

package webapi

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/auth"
	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/configfile/configfiletest"
	"github.com/home-operations/kritika/internal/store"
	"github.com/home-operations/kritika/internal/store/storetest"
)

const resyncConfig = `
auth:
  oidc:
    issuer: https://idp.example
    clientId: kritika
    clientSecret: { env: KRITIKA_TEST_TOKEN }
    roleMappingExpr: '"kritika-admin" in roles ? "admin" : ""'
apps:
  webapi-resync-bot:
    accounts: [wr]
    clientId: Iv1.resync
    privateKey: { env: KRITIKA_TEST_RESYNC_KEY }
    webhookSecret: { env: KRITIKA_TEST_TOKEN }
`

// TestRegisterReachedJudgesByTheDatabaseClock: the dashboard's resync tells
// the rows that a webhook wrote since its listing by the database's clock, as
// the leader's sync does. So an application clock behind the database's does
// not leave aside what a fresh listing says of a repository.
func TestRegisterReachedJudgesByTheDatabaseClock(t *testing.T) {
	ctx := t.Context()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("KRITIKA_TEST_TOKEN", "tok")
	t.Setenv("KRITIKA_TEST_RESYNC_KEY", string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})))
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v3/app/installations":
			_, _ = w.Write([]byte(`[{"id":7,"account":{"login":"wr","type":"Organization"}}]`))
		case "/api/v3/app/installations/7/access_tokens":
			_, _ = w.Write([]byte(`{"token":"ghs_7","expires_at":"` + time.Now().Add(time.Hour).UTC().Format(time.RFC3339) + `"}`))
		case "/api/v3/installation/repositories":
			_, _ = w.Write([]byte(`{"total_count":1,"repositories":[{"name":"one","full_name":"wr/one","default_branch":"main","visibility":"public"}]}`))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(gh.Close)

	st := storetest.Open(t)
	file := configfiletest.Load(t, resyncConfig)
	if err := st.ApplyConfig(ctx, file); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}
	account, _ := file.Account(configfile.ForgeGitHub, "wr")
	id := configfile.RepositoryID(account.ID(), "wr/one")
	// A webhook named it private before the listing below was taken.
	if err := st.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
		_, _, err := store.EnsureRepository(ctx, tx, account.ID(), store.ReachedRepository{
			FullName: "wr/one", DefaultBranch: "main", Traits: &configfile.RepoTraits{Visibility: "private"},
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}

	cur := configfile.NewCurrent(file)
	webURL, _ := url.Parse("https://kritika.example")
	logger := slog.New(slog.DiscardHandler)
	h, err := auth.New(auth.Config{Store: st, Current: cur, WebURL: webURL, Logger: logger})
	if err != nil {
		t.Fatalf("auth.New: %v", err)
	}
	// The application's clock is an hour behind the database's.
	srv := New(Config{
		Store: st, Current: cur, Auth: h, WebURL: webURL, Logger: logger, GitHubAPI: gh.URL + "/api/v3",
		Now: func() time.Time { return time.Now().Add(-time.Hour) },
	})
	api := httptest.NewServer(srv.Handler())
	t.Cleanup(api.Close)

	now, origin := time.Now(), "oidc:https://idp.example"
	user, err := st.UpsertIdentity(ctx, store.SignInIdentity{Provider: "oidc", Origin: origin, Subject: "resync-op", DisplayName: "op"})
	if err != nil {
		t.Fatal(err)
	}
	grant := store.SessionGrant{Role: store.RoleAdmin}
	grant.Key, _ = auth.GrantKey(file, "oidc")
	token, err := st.CreateSession(ctx, user.ID, "oidc", origin, grant, now, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, api.URL+"/api/v1/admin/connections/webapi-resync-bot/repositories", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName(webURL), Value: token})
	req.Header.Set("Origin", "https://kritika.example")
	req.Header.Set("X-Kritika", "1")
	resp, err := api.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("resync status = %d, want 200", resp.StatusCode)
	}

	var visibility string
	if err := st.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT visibility FROM repositories WHERE id = $1`, id).Scan(&visibility)
	}); err != nil {
		t.Fatal(err)
	}
	if visibility != "public" {
		t.Fatalf("visibility = %q after a resync whose listing says public, want public", visibility)
	}
}
