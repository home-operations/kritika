//go:build integration

package gateway

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/configfile/configfiletest"
	"github.com/home-operations/kritika/internal/egress"
	"github.com/home-operations/kritika/internal/runspec"
	"github.com/home-operations/kritika/internal/store"
	"github.com/home-operations/kritika/internal/store/storetest"
)

// TestProxyAdmitsRunTokens sends requests through the gateway the way a
// runner's tools do, with the run token as the proxy URL's password: the
// proxy serves a live token alone, and a token without a budget admits
// its run to the proxy and to no model call.
func TestProxyAdmitsRunTokens(t *testing.T) {
	ctx := t.Context()
	st := storetest.Open(t)
	t.Setenv("TEST_SECRET", "test-key")
	file := configfiletest.Load(t, `providers:
  test:
    type: openai
    baseUrl: http://unused.invalid/v1
    apiKey: { env: TEST_SECRET }
review: { model: test/reviewer }
apps:
  test:
    accounts: [proxy-auth]
    clientId: Iv1.test
    privateKey: { env: TEST_SECRET }
    webhookSecret: { env: TEST_SECRET }
`)
	if err := st.ApplyConfig(ctx, file); err != nil {
		t.Fatal(err)
	}
	storetest.Repositories(t, st, file, "proxy-auth/repo")
	account, _ := file.Account(configfile.ForgeGitHub, "proxy-auth")
	var runID string
	if err := st.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
		var err error
		runID, err = store.InsertRunnerRun(ctx, tx, account.ID(), store.RunnerKindIndex, "", 0)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	token, err := st.MintGatewayToken(ctx, store.GatewayGrant{
		RunID: runID, AccountID: account.ID(), RepositoryID: configfile.RepositoryID(account.ID(), "proxy-auth/repo"),
	}, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := st.ReserveGatewayTokens(ctx, token, 1); err != nil || ok {
		t.Fatalf("reserved a step on a token without a budget: %v, %v", ok, err)
	}

	reached := 0
	var digests []string
	proxy := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached++
		d, _ := egress.DigestOf(r.Context())
		digests = append(digests, d.String())
		if r.Header.Get("Proxy-Authorization") != "" {
			t.Error("the run token reached the proxy")
		}
		if r.Header.Get(runspec.DigestHeader) != "" {
			t.Error("the digest header reached the proxy")
		}
		http.Error(w, "reached", http.StatusTeapot)
	})
	registry := &AquaRegistry{body: []byte(aquaFixture)}
	srv := httptest.NewServer(&Server{
		Store: st, Current: configfile.NewCurrent(file), Logger: slog.New(slog.DiscardHandler), Proxy: proxy, AquaRegistry: registry,
	})
	defer srv.Close()
	via := func(user, password string) *http.Client {
		pu, err := url.Parse(srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		if password != "" {
			pu.User = url.UserPassword(user, password)
		}
		return &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu)}}
	}
	get := func(client *http.Client, target string, header ...string) (int, http.Header, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return 0, nil, err
		}
		for i := 0; i+1 < len(header); i += 2 {
			req.Header.Set(header[i], header[i+1])
		}
		resp, err := client.Do(req)
		if err != nil {
			return 0, nil, err
		}
		_ = resp.Body.Close()
		return resp.StatusCode, resp.Header, nil
	}

	// A plain http:// request reaches the gateway itself.
	for _, tt := range []struct {
		name, password string
		want           int
	}{
		{"no token", "", http.StatusProxyAuthRequired},
		{"an unknown token", "krk_unknown", http.StatusProxyAuthRequired},
		{"the run token", token, http.StatusTeapot},
	} {
		t.Run(tt.name, func(t *testing.T) {
			code, header, err := get(via("", tt.password), "http://example.com/")
			if err != nil || code != tt.want {
				t.Fatalf("status = %d, %v; want %d", code, err, tt.want)
			}
			if code == http.StatusProxyAuthRequired && !strings.HasPrefix(header.Get("Proxy-Authenticate"), "Basic") {
				t.Fatalf("Proxy-Authenticate = %q", header.Get("Proxy-Authenticate"))
			}
		})
	}
	// A download a runner checks names its digest in
	// runspec.DigestHeader; the proxy gets the request marked with it, and
	// one naming no sound digest unmarked, the header gone from both.
	digest := "sha256:" + strings.Repeat("ab", 32)
	for _, tt := range []struct {
		name, header string
		digests      []string
	}{
		{"a digest", digest, []string{":", digest}},
		{"no sound digest", "md5:abc", []string{":", digest, ":"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if code, _, err := get(via("", token), "http://example.com/", runspec.DigestHeader, tt.header); err != nil || code != http.StatusTeapot {
				t.Fatalf("status = %d, %v; want %d", code, err, http.StatusTeapot)
			}
			if !slices.Equal(digests, tt.digests) {
				t.Fatalf("digests = %v; want %v", digests, tt.digests)
			}
		})
	}
	// A CONNECT's refusal, and the proxy's own answer, surface as the
	// transport's error.
	t.Run("a CONNECT without the token", func(t *testing.T) {
		if _, _, err := get(via("", ""), "https://example.com/"); err == nil || !strings.Contains(err.Error(), "Proxy Authentication Required") {
			t.Fatalf("err = %v, want the gateway's refusal", err)
		}
	})
	t.Run("a CONNECT with the run token", func(t *testing.T) {
		if _, _, err := get(via("", token), "https://example.com/"); err == nil || !strings.Contains(err.Error(), "teapot") {
			t.Fatalf("err = %v, want the proxy's answer", err)
		}
	})
	// The aqua registry the gateway keeps is the run token's to read, as a
	// runner reads it: from the gateway, not through its proxy.
	t.Run("the aqua registry", func(t *testing.T) { checkAquaRegistry(t, srv.URL, token, registry) })
	t.Run("a revoked token", func(t *testing.T) {
		if err := st.RevokeGatewayTokens(ctx, runID); err != nil {
			t.Fatal(err)
		}
		if code, _, err := get(via("", token), "http://example.com/"); err != nil || code != http.StatusProxyAuthRequired {
			t.Fatalf("status = %d, %v; want %d", code, err, http.StatusProxyAuthRequired)
		}
	})
	if reached != 4 {
		t.Fatalf("the proxy served %d requests, want the four with the run token", reached)
	}
}

// checkAquaRegistry reads the registry the gateway at base keeps, as a
// runner does, with no token, with the run token, and with it before the
// gateway has fetched one.
func checkAquaRegistry(t *testing.T, base, token string, registry *AquaRegistry) {
	t.Helper()
	for _, tt := range []struct {
		name, authorization string
		kept                bool
		want                int
	}{
		{"no token", "", true, http.StatusUnauthorized},
		{"the run token", "Bearer " + token, true, http.StatusOK},
		{"none fetched yet", "Bearer " + token, false, http.StatusServiceUnavailable},
	} {
		registry.mu.Lock()
		registry.body = nil
		if tt.kept {
			registry.body = []byte(aquaFixture)
		}
		registry.mu.Unlock()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, base+runspec.AquaRegistryPath, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", tt.authorization)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != tt.want || (tt.want == http.StatusOK && string(body) != aquaFixture) {
			t.Fatalf("%s: status = %d, body %q; want %d", tt.name, resp.StatusCode, body, tt.want)
		}
	}
}

// TestProxyRecordsRefusals sends a request the rules refuse through the
// gateway with a run token: the refusal is recorded against the run, with
// the host alone, for the account's analytics.
func TestProxyRecordsRefusals(t *testing.T) {
	ctx := t.Context()
	st := storetest.Open(t)
	t.Setenv("TEST_SECRET", "test-key")
	file := configfiletest.Load(t, `providers:
  test:
    type: openai
    baseUrl: http://unused.invalid/v1
    apiKey: { env: TEST_SECRET }
review: { model: test/reviewer }
apps:
  test:
    accounts: [proxy-refused]
    clientId: Iv1.test
    privateKey: { env: TEST_SECRET }
    webhookSecret: { env: TEST_SECRET }
`)
	if err := st.ApplyConfig(ctx, file); err != nil {
		t.Fatal(err)
	}
	storetest.Repositories(t, st, file, "proxy-refused/repo")
	account, _ := file.Account(configfile.ForgeGitHub, "proxy-refused")
	var runID string
	if err := st.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
		var err error
		runID, err = store.InsertRunnerRun(ctx, tx, account.ID(), store.RunnerKindIndex, "", 0)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var repoID string
	if err := st.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT id FROM repositories WHERE account_id = $1 AND name = 'proxy-refused/repo'`, account.ID()).Scan(&repoID)
	}); err != nil {
		t.Fatal(err)
	}
	token, err := st.MintGatewayToken(ctx, store.GatewayGrant{RunID: runID, AccountID: account.ID(), RepositoryID: repoID}, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	gw := &Server{Store: st, Current: configfile.NewCurrent(file), Logger: slog.New(slog.DiscardHandler)}
	gw.Proxy = &egress.Proxy{Rules: file.EgressRules, Refused: gw.Refused, Logger: gw.Logger}
	srv := httptest.NewServer(gw)
	defer srv.Close()
	pu, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	pu.User = url.UserPassword("", token)
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu)}}
	for range 2 {
		resp, err := client.Get("http://Registry.Example.COM:80/v2/")
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", resp.StatusCode)
		}
	}
	if _, err := client.Get("https://docs.example.com/"); err == nil || !strings.Contains(err.Error(), "Forbidden") {
		t.Fatalf("CONNECT err = %v, want a Forbidden from the proxy", err)
	}
	var rows []store.EgressRefusalRow
	if err := st.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
		var err error
		rows, err = store.ReadEgressRefusals(ctx, tx, time.Now().Add(-time.Hour), time.Now().Add(time.Hour), 10)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Host != "registry.example.com" || rows[0].Count != 2 || rows[0].Runs != 1 ||
		rows[0].Repository != "proxy-refused/repo" || rows[0].Reasons["host not allowed"] != 2 ||
		rows[1].Host != "docs.example.com" || rows[1].Count != 1 {
		t.Fatalf("refusals = %+v", rows)
	}
}
