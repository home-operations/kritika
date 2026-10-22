package forgejo

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/home-operations/kritika/internal/configfile"
)

// Forgejo authenticates an authorized integration by a JWT the integration
// signs, which it checks against the issuer's OpenID discovery document
// and the JSON Web Key Set that names, both fetched from the issuer's URL
// over HTTPS and cached. kritika is the issuer: it signs each connection's
// tokens with that connection's key and serves the two documents under
// its own URL.

// issuerPath is where the documents live under the dashboard's URL, so
// the issuer is one URL for every connection.
const issuerPath = "/forgejo"

// tokenTTL is how long a token kritika calls the API with lasts, and
// runnerTokenTTL one a runner fetches with, which is read once a run
// starts but may wait in a queue first. skew is how far a token's issue
// and not-before times are set back, since Forgejo refuses a token issued
// in its future and allows no leeway.
const (
	tokenTTL       = 10 * time.Minute
	runnerTokenTTL = time.Hour
	skew           = time.Minute
)

// IssuerURL is the issuer kritika's tokens name when the dashboard is at
// web: the discovery document is at its .well-known/openid-configuration.
func IssuerURL(web *url.URL) string {
	return strings.TrimRight(web.String(), "/") + issuerPath
}

// Issuer signs the tokens of one connection with its key.
type Issuer struct {
	url string
	key *rsa.PrivateKey
	kid string
}

// NewIssuer builds an Issuer at issuerURL signing with privateKeyPEM, an
// RSA key in PKCS #1 or PKCS #8 form.
func NewIssuer(issuerURL, privateKeyPEM string) (*Issuer, error) {
	key, err := parseRSAKey(privateKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("forgejo: private key: %w", err)
	}
	jwk := jose.JSONWebKey{Key: key.Public(), Algorithm: string(jose.RS256), Use: "sig"}
	thumb, err := jwk.Thumbprint(crypto.SHA256)
	if err != nil {
		return nil, fmt.Errorf("forgejo: key thumbprint: %w", err)
	}
	return &Issuer{url: issuerURL, key: key, kid: base64.RawURLEncoding.EncodeToString(thumb)}, nil
}

// URL is the issuer's URL, the iss claim of its tokens.
func (i *Issuer) URL() string { return i.url }

// PublicKey is the key that verifies the issuer's tokens, as the JWKS
// lists it: the key id is the key's thumbprint, so a rotated key gets a
// new id and both can be served while Forgejo's cache of the set lasts.
func (i *Issuer) PublicKey() jose.JSONWebKey {
	return jose.JSONWebKey{Key: i.key.Public(), KeyID: i.kid, Algorithm: string(jose.RS256), Use: "sig"}
}

// Token signs a token for audience, the integration's, lasting ttl from
// now, issued skew ago. The subject is kritika, which a claim rule on the
// integration can pin.
func (i *Issuer) Token(audience string, ttl time.Duration, now time.Time) (string, error) {
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: i.key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", i.kid))
	if err != nil {
		return "", fmt.Errorf("forgejo: signer: %w", err)
	}
	jti := make([]byte, 16)
	if _, err := rand.Read(jti); err != nil {
		return "", fmt.Errorf("forgejo: token id: %w", err)
	}
	claims := jwt.Claims{
		Issuer: i.url, Subject: "kritika", Audience: jwt.Audience{audience}, ID: base64.RawURLEncoding.EncodeToString(jti),
		IssuedAt: jwt.NewNumericDate(now.Add(-skew)), NotBefore: jwt.NewNumericDate(now.Add(-skew)), Expiry: jwt.NewNumericDate(now.Add(ttl)),
	}
	token, err := jwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		return "", fmt.Errorf("forgejo: sign token: %w", err)
	}
	return token, nil
}

// parseRSAKey reads a PEM RSA private key in PKCS #1 or PKCS #8 form.
func parseRSAKey(privateKeyPEM string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(privateKeyPEM))
	if block == nil {
		return nil, errors.New("no PEM block")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	rsaKey, ok := key.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("not an RSA key but %T", key)
	}
	return rsaKey, nil
}

// Keys is the key set Discovery serves for file: every Forgejo
// connection's signing key and the one it is rotating out, as kritika's
// tokens are verified against; a key that does not parse is left out and
// logged.
func Keys(issuerURL string, file *configfile.File, logger *slog.Logger) []jose.JSONWebKey {
	var out []jose.JSONWebKey
	for i := range file.Connections {
		in := &file.Connections[i]
		if in.Forge != configfile.ForgeForgejo {
			continue
		}
		for _, key := range []string{in.Forgejo.PrivateKeyValue().Value(), in.Forgejo.PreviousKeyValue().Value()} {
			if key == "" {
				continue
			}
			issuer, err := NewIssuer(issuerURL, key)
			if err != nil {
				logger.Warn("forgejo signing key not served", "connection", in.Name, "error", err)
				continue
			}
			out = append(out, issuer.PublicKey())
		}
	}
	return out
}

// Discovery serves the issuer's two documents under issuerURL's path:
// .well-known/openid-configuration, naming the issuer, the key set's URL
// and the signing algorithm, and .well-known/keys, the set of every
// connection's public key as keys lists them when asked, so a key added
// or rotated in the configuration is served on the next request.
func Discovery(issuerURL string, keys func() []jose.JSONWebKey) http.Handler {
	base, _ := url.Parse(issuerURL)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var doc any
		switch path.Base(r.URL.Path) {
		case "openid-configuration":
			doc = map[string]any{
				"issuer": issuerURL, "jwks_uri": issuerURL + "/.well-known/keys",
				"id_token_signing_alg_values_supported": []string{string(jose.RS256)},
				"response_types_supported":              []string{"id_token"},
				"subject_types_supported":               []string{"public"},
			}
		case "keys":
			doc = jose.JSONWebKeySet{Keys: keys()}
		default:
			http.NotFound(w, r)
			return
		}
		if base == nil || !strings.HasPrefix(r.URL.Path, strings.TrimRight(base.Path, "/")+"/.well-known/") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(doc)
	})
}
