package forgejo

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/home-operations/kritika/internal/configfile"
)

var (
	testKeyOnce sync.Once
	testKeyPEM  string
)

// testKey is one RSA key for the package's tests, in PKCS #1 PEM.
func testKey(t *testing.T) string {
	t.Helper()
	testKeyOnce.Do(func() {
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatal(err)
		}
		testKeyPEM = string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	})
	return testKeyPEM
}

// testIssuer is an Issuer at https://kritika.example.com/forgejo signing
// with the test key.
func testIssuer(t *testing.T) *Issuer {
	t.Helper()
	i, err := NewIssuer("https://kritika.example.com/forgejo", testKey(t))
	if err != nil {
		t.Fatal(err)
	}
	return i
}

// verify makes sure that a token is valid against the issuer's key and
// returns its claims.
func verify(t *testing.T, i *Issuer, token string) (jwt.Claims, string) {
	t.Helper()
	parsed, err := jwt.ParseSigned(token, []jose.SignatureAlgorithm{jose.RS256})
	if err != nil {
		t.Fatalf("parse token: %v", err)
	}
	var claims jwt.Claims
	if err := parsed.Claims(i.PublicKey().Key, &claims); err != nil {
		t.Fatalf("verify token: %v", err)
	}
	return claims, parsed.Headers[0].KeyID
}

func TestIssuerURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://kritika.example.com": "https://kritika.example.com/forgejo", "https://kritika.example.com/": "https://kritika.example.com/forgejo",
		"https://example.com/kritika": "https://example.com/kritika/forgejo",
	} {
		u, _ := url.Parse(in)
		if got := IssuerURL(u); got != want {
			t.Fatalf("IssuerURL(%s) = %s, want %s", in, got, want)
		}
	}
}

func TestIssuerToken(t *testing.T) {
	i := testIssuer(t)
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	token, err := i.Token("aud-1", 10*time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	claims, kid := verify(t, i, token)
	// Issued and valid from a minute ago: Forgejo refuses a token issued in
	// its own future and allows no leeway. So a clock a little ahead must not
	// make every fresh token fail.
	if claims.Issuer != i.URL() || claims.Subject != "kritika" || len(claims.Audience) != 1 || claims.Audience[0] != "aud-1" || claims.ID == "" ||
		!claims.Expiry.Time().Equal(now.Add(10*time.Minute)) || !claims.IssuedAt.Time().Equal(now.Add(-skew)) || !claims.NotBefore.Time().Equal(now.Add(-skew)) {
		t.Fatalf("claims = %+v", claims)
	}
	if err := claims.Validate(jwt.Expected{Issuer: i.URL(), AnyAudience: jwt.Audience{"aud-1"}, Time: now.Add(-30 * time.Second)}); err != nil {
		t.Fatalf("a verifier half a minute behind refuses the token: %v", err)
	}
	if kid == "" || kid != i.PublicKey().KeyID {
		t.Fatalf("kid = %q, key id %q; the token names the key the set lists", kid, i.PublicKey().KeyID)
	}
	again, _ := NewIssuer(i.URL(), testKey(t))
	if again.PublicKey().KeyID != i.PublicKey().KeyID {
		t.Fatal("a key's id is its own, the same on every load")
	}
	other, _ := i.Token("aud-2", time.Minute, now)
	if other == token {
		t.Fatal("tokens differ by audience")
	}
	if pub := i.PublicKey(); pub.Algorithm != "RS256" || pub.Use != "sig" || !pub.IsPublic() {
		t.Fatalf("public key = %+v", pub)
	}
}

func TestNewIssuerRejectsBadKeys(t *testing.T) {
	for name, pemText := range map[string]string{"no pem": "not a key", "ec key": ecKeyPEM(t)} {
		if _, err := NewIssuer("https://kritika.example.com/forgejo", pemText); err == nil {
			t.Fatalf("%s: want an error", name)
		}
	}
	// PKCS #8 is taken as PKCS #1 is.
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	if _, err := NewIssuer("https://kritika.example.com/forgejo", string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))); err != nil {
		t.Fatal(err)
	}
}

func ecKeyPEM(t *testing.T) string {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(mustECKey(t))
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

func TestDiscovery(t *testing.T) {
	i := testIssuer(t)
	served := 0
	h := Discovery(i.URL(), func() []jose.JSONWebKey { served++; return []jose.JSONWebKey{i.PublicKey()} })
	srv := httptest.NewServer(h)
	defer srv.Close()
	get := func(path string) (int, map[string]any) {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var doc map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&doc)
		return resp.StatusCode, doc
	}
	status, conf := get("/forgejo/.well-known/openid-configuration")
	if status != 200 || conf["issuer"] != i.URL() || conf["jwks_uri"] != i.URL()+"/.well-known/keys" {
		t.Fatalf("discovery = %d %v", status, conf)
	}
	algs, _ := conf["id_token_signing_alg_values_supported"].([]any)
	if len(algs) != 1 || algs[0] != "RS256" {
		t.Fatalf("algorithms = %v", conf["id_token_signing_alg_values_supported"])
	}
	status, keys := get("/forgejo/.well-known/keys")
	list, _ := keys["keys"].([]any)
	if status != 200 || len(list) != 1 || served != 1 {
		t.Fatalf("keys = %d %v, served %d", status, keys, served)
	}
	key := list[0].(map[string]any)
	if key["kid"] != i.PublicKey().KeyID || key["kty"] != "RSA" || key["alg"] != "RS256" || key["use"] != "sig" || key["n"] == nil || key["d"] != nil {
		t.Fatalf("key = %v; the public half alone, with its id", key)
	}
	for _, path := range []string{"/forgejo/.well-known/other", "/forgejo/keys", "/other/.well-known/keys"} {
		if status, _ := get(path); status != 404 {
			t.Fatalf("%s = %d, want 404", path, status)
		}
	}
	// Under a base path the documents sit under it too.
	based := httptest.NewServer(Discovery("https://example.com/kritika/forgejo", func() []jose.JSONWebKey { return nil }))
	defer based.Close()
	resp, err := http.Get(based.URL + "/kritika/forgejo/.well-known/openid-configuration")
	if err != nil || resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Content-Type"), "json") {
		t.Fatalf("under a base path: %v %v", resp, err)
	}
	_ = resp.Body.Close()
}

func mustECKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

// TestKeys: the set holds every Forgejo connection's signing key and the
// one it is rotating out, and leaves out one that does not parse.
func TestKeys(t *testing.T) {
	t.Setenv("TEST_KEY", testKey(t))
	t.Setenv("TEST_OLD_KEY", ecKeyPEM(t))
	t.Setenv("TEST_SECRET", "s")
	t.Setenv("TEST_PRIVATE_KEY", "pem")
	file, err := configfile.Parse([]byte(`
apps:
  gh: { accounts: [acme], clientId: x, privateKey: { env: TEST_PRIVATE_KEY }, webhookSecret: { env: TEST_SECRET } }
forgejo:
  one: { host: a.example.com, accounts: [one], privateKey: { env: TEST_KEY }, previousKey: { env: TEST_OLD_KEY }, webhookSecret: { env: TEST_SECRET } }
  two: { host: b.example.com, accounts: [two], privateKey: { env: TEST_KEY }, webhookSecret: { env: TEST_SECRET } }
`))
	if err != nil {
		t.Fatal(err)
	}
	keys := Keys("https://kritika.example.com/forgejo", file, slog.New(slog.DiscardHandler))
	if len(keys) != 2 || keys[0].KeyID != keys[1].KeyID || keys[0].KeyID != testIssuer(t).PublicKey().KeyID {
		t.Fatalf("keys = %d, want the two connections' RSA keys and not the EC one", len(keys))
	}
}
