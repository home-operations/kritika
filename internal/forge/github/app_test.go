package github

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

func testKeyPEM(t *testing.T) (*rsa.PrivateKey, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return key, string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
}

func TestAppJWTClaims(t *testing.T) {
	key, _ := testKeyPEM(t)
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	tok, err := appJWT("Iv1.abc", key, now)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := jwt.ParseSigned(tok, []jose.SignatureAlgorithm{jose.RS256})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if h := parsed.Headers[0]; h.ExtraHeaders[jose.HeaderType] != "JWT" {
		t.Fatalf("header = %+v, want typ JWT", h)
	}
	var claims jwt.Claims
	if err := parsed.Claims(&key.PublicKey, &claims); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if err := claims.ValidateWithLeeway(jwt.Expected{Issuer: "Iv1.abc", Time: now}, 0); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if got := claims.IssuedAt.Time(); !got.Equal(now.Add(-30 * time.Second)) {
		t.Fatalf("iat = %v, want 30s backdated", got)
	}
	if got := claims.Expiry.Time(); !got.Equal(now.Add(9 * time.Minute)) {
		t.Fatalf("exp = %v, want now+9m (under GitHub's 10m cap)", got)
	}
}

func TestNewAppKeys(t *testing.T) {
	key, pkcs1 := testKeyPEM(t)
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	pkcs8 := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	ec, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if der, err = x509.MarshalPKCS8PrivateKey(ec); err != nil {
		t.Fatal(err)
	}
	ecPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	for _, tt := range []struct {
		name, pem string
		ok        bool
	}{
		{"a PKCS #1 key, as GitHub issues one", pkcs1, true},
		{"a PKCS #8 RSA key", pkcs8, true},
		{"not PEM", "not a key", false},
		{"an EC key", ecPEM, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewApp("Iv1.abc", tt.pem, ""); (err == nil) != tt.ok {
				t.Fatalf("NewApp = %v, want ok = %v", err, tt.ok)
			}
		})
	}
}

func TestInstallationTokensMintOnceAndRefresh(t *testing.T) {
	_, pemKey := testKeyPEM(t)
	mints := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ey") {
			t.Errorf("mint request without an App JWT bearer: %q", r.Header.Get("Authorization"))
		}
		if r.URL.Path != "/api/v3/app/installations/42/access_tokens" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		mints++
		exp := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
		if mints == 1 {
			exp = time.Now().Add(30 * time.Second).UTC().Format(time.RFC3339) // expires soon: forces a refresh
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token":"ghs_test` + string(rune('0'+mints)) + `","expires_at":"` + exp + `"}`))
	}))
	defer srv.Close()

	app, err := NewApp("Iv1.abc", pemKey, srv.URL+"/api/v3")
	if err != nil {
		t.Fatal(err)
	}
	tokens := app.InstallationTokens(42)
	ctx := t.Context()
	first, err := tokens.Token(ctx)
	if err != nil || first != "ghs_test1" {
		t.Fatalf("first token = %q, %v", first, err)
	}
	// The first token expires within the refresh margin, so the next call
	// mints again. The second token is good for an hour and is then cached.
	second, _ := tokens.Token(ctx)
	third, _ := tokens.Token(ctx)
	if second != "ghs_test2" || third != "ghs_test2" || mints != 2 {
		t.Fatalf("tokens = %q, %q with %d mints; want a refresh then a cache hit", second, third, mints)
	}
}

// TestReadOnlyToken: a runner's token is minted fresh for each run, for
// the one repository, with read access to its contents and metadata alone.
func TestReadOnlyToken(t *testing.T) {
	_, pemKey := testKeyPEM(t)
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token":"ghs_ro","expires_at":"` + time.Now().Add(time.Hour).UTC().Format(time.RFC3339) + `"}`))
	}))
	defer srv.Close()
	app, err := NewApp("Iv1.abc", pemKey, srv.URL+"/api/v3")
	if err != nil {
		t.Fatal(err)
	}
	tokens := app.InstallationTokens(42)
	for range 2 {
		if tok, err := tokens.ReadOnly(t.Context(), "repo-1"); err != nil || tok != "ghs_ro" {
			t.Fatalf("ReadOnly = %q, %v", tok, err)
		}
	}
	want := `{"repositories":["repo-1"],"permissions":{"contents":"read","metadata":"read"}}`
	if len(bodies) != 2 || strings.TrimSpace(bodies[0]) != want {
		t.Fatalf("mint requests = %q, want two of %s", bodies, want)
	}
}

func TestInstallationsAndUninstall(t *testing.T) {
	_, pemKey := testKeyPEM(t)
	var deleted []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ey") {
			t.Errorf("%s %s without an App JWT bearer", r.Method, r.URL.Path)
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v3/app/installations" && r.URL.Query().Get("page") == "":
			w.Header().Set("Link", `<`+"http://"+r.Host+`/api/v3/app/installations?page=2>; rel="next"`)
			_, _ = w.Write([]byte(`[{"id":1,"account":{"login":"org-1","type":"Organization"},"repository_selection":"all",` +
				`"html_url":"https://github.com/organizations/org-1/settings/installations/1"}]`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v3/app/installations":
			_, _ = w.Write([]byte(`[{"id":2,"account":{"login":"user-1","type":"User"},"repository_selection":"selected",` +
				`"suspended_at":"2026-09-01T00:00:00Z"}]`))
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/api/v3/app/installations/"):
			deleted = append(deleted, strings.TrimPrefix(r.URL.Path, "/api/v3/app/installations/"))
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	app, err := NewApp("Iv1.abc", pemKey, srv.URL+"/api/v3")
	if err != nil {
		t.Fatal(err)
	}
	got, err := app.Installations(t.Context())
	want := []Installation{
		{ID: 1, Account: "org-1", AccountType: "Organization", AllRepositories: true, HTMLURL: "https://github.com/organizations/org-1/settings/installations/1"},
		{ID: 2, Account: "user-1", AccountType: "User", Suspended: true},
	}
	if err != nil || len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("Installations = %+v, %v", got, err)
	}
	if err := app.Uninstall(t.Context(), 2); err != nil || len(deleted) != 1 || deleted[0] != "2" {
		t.Fatalf("Uninstall = %v, deleted %v", err, deleted)
	}
}

// TestRepositories also makes sure that the listing's token is the
// installation's one. Listed again, and through a client of the installation,
// no token is minted anew.
func TestRepositories(t *testing.T) {
	_, pemKey := testKeyPEM(t)
	mints := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v3/app/installations/7/access_tokens":
			mints++
			exp := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
			_, _ = w.Write([]byte(`{"token":"ghs_7","expires_at":"` + exp + `"}`))
		case "/api/v3/installation/repositories":
			if r.Header.Get("Authorization") != "token ghs_7" && r.Header.Get("Authorization") != "Bearer ghs_7" {
				t.Errorf("listing repositories with %q, want the installation token", r.Header.Get("Authorization"))
			}
			_, _ = w.Write([]byte(`{"total_count":2,"repositories":[{"name":"repo-1","full_name":"org-1/repo-1","private":true,"default_branch":"main"},` +
				`{"name":"old","full_name":"org-1/old","archived":true,"fork":true}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	app, err := NewApp("Iv1.abc", pemKey, srv.URL+"/api/v3")
	if err != nil {
		t.Fatal(err)
	}
	got, err := app.Repositories(t.Context(), 7)
	want := []Repository{
		{Name: "repo-1", FullName: "org-1/repo-1", DefaultBranch: "main"},
		{Name: "old", FullName: "org-1/old", Archived: true, Fork: true},
	}
	if err != nil || len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("Repositories = %+v, %v", got, err)
	}
	if _, err := app.Repositories(t.Context(), 7); err != nil {
		t.Fatal(err)
	}
	if tok, err := app.InstallationTokens(7).Token(t.Context()); err != nil || tok != "ghs_7" {
		t.Fatalf("token = %q, %v", tok, err)
	}
	if mints != 1 {
		t.Fatalf("%d tokens minted for one installation; want one, shared", mints)
	}
}

// TestReach: each account is installed or not, with the repositories that its
// installation reaches. A suspended installation reaches none and is not
// asked for them.
func TestReach(t *testing.T) {
	_, pemKey := testKeyPEM(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v3/app/installations":
			_, _ = w.Write([]byte(`[{"id":7,"account":{"login":"Org-1","type":"Organization"}},` +
				`{"id":8,"account":{"login":"user-1","type":"User"},"suspended_at":"2026-09-01T00:00:00Z"}]`))
		case "/api/v3/app/installations/7/access_tokens":
			exp := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
			_, _ = w.Write([]byte(`{"token":"ghs_7","expires_at":"` + exp + `"}`))
		case "/api/v3/installation/repositories":
			_, _ = w.Write([]byte(`{"total_count":1,"repositories":[{"name":"repo-1","full_name":"Org-1/repo-1","default_branch":"main","fork":true}]}`))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	app, err := NewApp("Iv1.abc", pemKey, srv.URL+"/api/v3")
	if err != nil {
		t.Fatal(err)
	}
	got, err := app.Reach(t.Context(), []string{"org-1", "user-1", "gone"})
	if err != nil {
		t.Fatal(err)
	}
	want := []AccountRepositories{
		{Account: "org-1", Installed: true, Repositories: []Repository{{Name: "repo-1", FullName: "Org-1/repo-1", DefaultBranch: "main", Fork: true}}},
		{Account: "user-1", Repositories: []Repository{}},
		{Account: "gone", Repositories: []Repository{}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Reach = %+v, want %+v", got, want)
	}
}

// TestBaseTransportBoundsTheHeaderWait: a server that accepts a request and
// never answers fails the request after responseHeaderTimeout. So a caller
// with no deadline of its own does not wait for good.
func TestBaseTransportBoundsTheHeaderWait(t *testing.T) {
	if baseTransport.ResponseHeaderTimeout != responseHeaderTimeout {
		t.Fatalf("ResponseHeaderTimeout = %s, want %s", baseTransport.ResponseHeaderTimeout, responseHeaderTimeout)
	}
	hang := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-hang }))
	// Close waits for the handler, which waits for hang.
	defer srv.Close()
	defer close(hang)
	tr := baseTransport.Clone()
	tr.ResponseHeaderTimeout = 50 * time.Millisecond
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tr.RoundTrip(req); err == nil || !strings.Contains(err.Error(), "timeout awaiting response headers") {
		t.Fatalf("RoundTrip error = %v, want a response header timeout", err)
	}
}
