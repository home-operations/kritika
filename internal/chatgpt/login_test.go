package chatgpt

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

const issuedClient = "oaiapp_1"

// fakeOpenAI is OpenAI's authorization server as the login sees it:
// discovery, JWKS and a token endpoint that checks the exchange and
// issues an RS256 ID token for issuedClient.
type fakeOpenAI struct {
	srv  *httptest.Server
	sign func(claims map[string]any) string
	// challenge is the PKCE challenge the authorize URL carried, which the
	// token endpoint checks the verifier against.
	challenge string
	// nonce is what the ID token carries; "" means the authorize URL's.
	nonce string
	// scope is what the token response grants.
	scope   string
	subject string
	// exchange is the last token request's form.
	exchange url.Values
}

func newFakeOpenAI(t *testing.T) *fakeOpenAI {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "k1"))
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeOpenAI{scope: strings.Join(scopes, " "), subject: "user-1"}
	f.sign = func(claims map[string]any) string {
		raw, _ := json.Marshal(claims)
		sig, err := signer.Sign(raw)
		if err != nil {
			t.Fatal(err)
		}
		out, err := sig.CompactSerialize()
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	mux := http.NewServeMux()
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	iss := f.srv.URL
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"issuer": iss, "authorization_endpoint": iss + "/api/accounts/authorize", "token_endpoint": iss + "/api/accounts/oauth/token",
			"jwks_uri": iss + "/jwks", "id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("GET /jwks", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"}}})
	})
	mux.HandleFunc("POST /api/accounts/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.exchange = r.PostForm
		sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
		if base64.RawURLEncoding.EncodeToString(sum[:]) != f.challenge || r.PostForm.Get("code") != "code-1" {
			w.WriteHeader(http.StatusBadRequest)
			writeJSON(w, map[string]string{"error": "invalid_grant"})
			return
		}
		now := time.Now()
		nonce := f.nonce
		if nonce == "" {
			nonce = r.PostForm.Get("x-nonce")
		}
		writeJSON(w, map[string]any{
			"access_token": "at-1", "refresh_token": "rt-1", "token_type": "Bearer", "expires_in": 3600, "scope": f.scope,
			"id_token": f.sign(map[string]any{
				"iss": iss, "sub": f.subject, "aud": issuedClient, "exp": now.Add(time.Hour).Unix(), "iat": now.Unix(),
				"nonce": nonce, "email": "dev@example.com",
			}),
		})
	})
	return f
}

// output is what the login prints, read by the test while Run writes it.
type output struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (o *output) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.b.Write(p)
}

func (o *output) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.b.String()
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// signIn runs the login and plays the browser: it reads the printed URL,
// then calls the callback with query, which may override the state and
// client the browser would bring. It returns what Run returned.
func signIn(t *testing.T, f *fakeOpenAI, current Credentials, query url.Values) (Credentials, string, error) {
	t.Helper()
	var out output
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	type result struct {
		c   Credentials
		err error
	}
	done := make(chan result, 1)
	go func() {
		c, err := Login{Addr: "127.0.0.1:0", Issuer: f.srv.URL, Client: f.srv.Client(), Out: &out}.Run(ctx, current)
		done <- result{c, err}
	}()
	// The URL is printed once the listener is up.
	var authorize *url.URL
	for authorize == nil {
		select {
		case r := <-done:
			return r.c, out.String(), r.err
		case <-time.After(10 * time.Millisecond):
		}
		if _, after, ok := strings.Cut(out.String(), "\n\n  "); ok {
			line, _, _ := strings.Cut(after, "\n")
			u, err := url.Parse(line)
			if err != nil {
				t.Fatal(err)
			}
			authorize = u
		}
	}
	q := authorize.Query()
	f.challenge = q.Get("code_challenge")
	wantClient := current.ClientID
	if wantClient == "" {
		wantClient = registrationClient
	}
	if q.Get("client_id") != wantClient || q.Get("ext_agent_host_id") != current.HostID ||
		q.Get("response_type") != "code" || q.Get("resource") != Resource || q.Get("code_challenge_method") != "S256" ||
		q.Get("scope") != strings.Join(scopes, " ") || q.Get("nonce") == "" || q.Get("state") == "" {
		t.Fatalf("authorize URL = %s", authorize)
	}
	if current.ClientID == "" && q.Get("agent_name_hint") != agentName {
		t.Fatalf("registration URL = %s", authorize)
	}
	if current.ClientID != "" && (q.Get("agent_name_hint") != "" || q.Get("id_token_hint") != current.IDToken || q.Get("login_hint") != current.Email) {
		t.Fatalf("reauthorization URL = %s", authorize)
	}
	redirect, err := url.Parse(q.Get("redirect_uri"))
	if err != nil || redirect.Scheme != "http" || redirect.Hostname() != "127.0.0.1" || redirect.Path != callbackPath {
		t.Fatalf("redirect_uri = %q (%v)", q.Get("redirect_uri"), err)
	}
	// The fake's token endpoint has no authorize step, so the nonce
	// travels to it in the test's own form field.
	back := url.Values{"code": {"code-1"}, "state": {q.Get("state")}, "client_id": {issuedClient}, "scope": {f.scope}}
	maps.Copy(back, query)
	f.nonce = cmpNonce(f.nonce, q.Get("nonce"))
	redirect.RawQuery = back.Encode()
	resp, err := http.Get(redirect.String())
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	r := <-done
	return r.c, out.String(), r.err
}

func cmpNonce(set, fromURL string) string {
	if set != "" {
		return set
	}
	return fromURL
}

func TestLogin(t *testing.T) {
	f := newFakeOpenAI(t)
	c, out, err := signIn(t, f, Credentials{HostID: "urn:uuid:host"}, nil)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if c.ClientID != issuedClient || c.AccessToken != "at-1" || c.RefreshToken != "rt-1" || c.Email != "dev@example.com" || c.Subject != "user-1" || c.Issuer != f.srv.URL || c.IDToken == "" || c.ExpiresIn != 3600 || c.HostID != "urn:uuid:host" || c.SavedAt.IsZero() {
		t.Fatalf("credentials = %+v", c)
	}
	if ex := f.exchange; ex.Get("grant_type") != "authorization_code" || ex.Get("client_id") != issuedClient || ex.Get("resource") != Resource || ex.Get("redirect_uri") == "" || ex.Get("client_secret") != "" {
		t.Fatalf("exchange = %v", ex)
	}
}

func TestLoginReauthorize(t *testing.T) {
	current := Credentials{ClientID: issuedClient, HostID: "urn:uuid:host", Subject: "user-1", Email: "dev@example.com", IDToken: "saved-id-token"}
	for _, tt := range []struct {
		name    string
		client  string
		subject string
		want    string
	}{
		{"callback retains the client", issuedClient, "user-1", ""},
		{"callback omits the client", "", "user-1", ""},
		{"callback changes the client", "oaiapp_other", "user-1", "changed the saved client"},
		{"different identity", issuedClient, "user-2", "differs from the saved account"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeOpenAI(t)
			f.subject = tt.subject
			c, _, err := signIn(t, f, current, url.Values{"client_id": {tt.client}})
			if tt.want != "" {
				if err == nil || !strings.Contains(err.Error(), tt.want) {
					t.Fatalf("err = %v, want %s", err, tt.want)
				}
				return
			}
			if err != nil || c.ClientID != current.ClientID || c.HostID != current.HostID {
				t.Fatalf("credentials = %+v, err = %v", c, err)
			}
		})
	}
}

func TestLoginRefuses(t *testing.T) {
	for _, tt := range []struct {
		name  string
		setup func(*fakeOpenAI)
		query url.Values
		want  string
	}{
		{"plan usage not granted", func(f *fakeOpenAI) { f.scope = "openid profile email" }, nil, "did not grant " + PlanScope},
		{"a nonce for another attempt", func(f *fakeOpenAI) { f.nonce = "stale" }, nil, "nonce mismatch"},
		{"no identity", func(f *fakeOpenAI) { f.subject = "" }, nil, "subject is missing"},
		{"the user declined", nil, url.Values{"error": {"access_denied"}, "code": {""}}, "refused: access_denied"},
		{"no client registered", nil, url.Values{"client_id": {registrationClient}}, "registered no client"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeOpenAI(t)
			if tt.setup != nil {
				tt.setup(f)
			}
			_, _, err := signIn(t, f, Credentials{HostID: "urn:uuid:host"}, tt.query)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %s", err, tt.want)
			}
		})
	}
	t.Run("another state is refused and the wait continues", func(t *testing.T) {
		f := newFakeOpenAI(t)
		var out output
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		errs := make(chan error, 1)
		go func() {
			_, err := (Login{Addr: "127.0.0.1:0", Issuer: f.srv.URL, Client: f.srv.Client(), Out: &out}).Run(ctx, Credentials{HostID: "urn:uuid:host"})
			errs <- err
		}()
		var redirect string
		for redirect == "" {
			select {
			case err := <-errs:
				t.Fatalf("login ended: %v", err)
			case <-time.After(10 * time.Millisecond):
			}
			if _, after, ok := strings.Cut(out.String(), "\n\n  "); ok {
				line, _, _ := strings.Cut(after, "\n")
				u, _ := url.Parse(line)
				redirect = u.Query().Get("redirect_uri")
			}
		}
		resp, err := http.Get(redirect + "?state=other&code=x&client_id=" + issuedClient)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", resp.StatusCode)
		}
		cancel()
		if err := <-errs; !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
	})
}
