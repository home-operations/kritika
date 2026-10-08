package chatgpt

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var saved = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

func record() Credentials {
	return Credentials{
		Issuer: Issuer, ClientID: "oaiapp_1", HostID: "urn:uuid:host", AccessToken: "at-1", RefreshToken: "rt-1",
		TokenType: "Bearer", ExpiresIn: 3600, Scopes: []string{"openid", PlanScope}, SavedAt: saved,
	}
}

func TestParseCredentials(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"valid", `{"client_id":"oaiapp_1","access_token":"a","refresh_token":"r","scopes":["chatgpt.tokens.use.direct"]}`, ""},
		{"not json", `{`, "credentials:"},
		{"no client", `{"access_token":"a","refresh_token":"r","scopes":["chatgpt.tokens.use.direct"]}`, "client_id is missing"},
		{"no access token", `{"client_id":"c","refresh_token":"r","scopes":["chatgpt.tokens.use.direct"]}`, "access_token is missing"},
		{"no refresh token", `{"client_id":"c","access_token":"a","scopes":["chatgpt.tokens.use.direct"]}`, "refresh_token is missing"},
		{"plan usage not granted", `{"client_id":"c","access_token":"a","refresh_token":"r","scopes":["openid"]}`, "did not grant chatgpt.tokens.use.direct"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseCredentials([]byte(tt.raw))
			if tt.want == "" && err != nil {
				t.Fatal(err)
			}
			if tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)) {
				t.Fatalf("err = %v, want one containing %q", err, tt.want)
			}
		})
	}
}

// tokenEndpoint answers every refresh with status and body, counting them
// and keeping the last form it was sent.
func tokenEndpoint(t *testing.T, status int, body string) (*httptest.Server, *atomic.Int32, *url.Values) {
	t.Helper()
	var n atomic.Int32
	form := &url.Values{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		raw, _ := io.ReadAll(r.Body)
		*form, _ = url.ParseQuery(string(raw))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &n, form
}

const refreshed = `{"access_token":"at-2","refresh_token":"rt-2","token_type":"Bearer","expires_in":3600,` +
	`"scope":"chatgpt.tokens.use.direct openid"}`

func TestRefresh(t *testing.T) {
	later := saved.Add(time.Hour)
	t.Run("rotates both tokens and keeps the rest", func(t *testing.T) {
		srv, _, form := tokenEndpoint(t, http.StatusOK, refreshed)
		c, err := Refresh(t.Context(), srv.Client(), srv.URL, record(), later)
		if err != nil {
			t.Fatal(err)
		}
		if c.AccessToken != "at-2" || c.RefreshToken != "rt-2" || !c.SavedAt.Equal(later) || c.ExpiresIn != 3600 {
			t.Fatalf("credentials = %+v", c)
		}
		if c.ClientID != "oaiapp_1" || c.HostID != "urn:uuid:host" || len(c.Scopes) != 2 || c.Scopes[0] != PlanScope {
			t.Fatalf("credentials = %+v, want the client and host kept and the scopes replaced", c)
		}
		want := url.Values{"grant_type": {"refresh_token"}, "client_id": {"oaiapp_1"}, "refresh_token": {"rt-1"}, "resource": {Resource}}
		if form.Encode() != want.Encode() {
			t.Fatalf("form = %v, want %v", *form, want)
		}
	})
	t.Run("a refused refresh token signs out", func(t *testing.T) {
		for _, code := range signedOutCodes {
			srv, _, _ := tokenEndpoint(t, http.StatusBadRequest, `{"error":"`+code+`","error_description":"gone"}`)
			c, err := Refresh(t.Context(), srv.Client(), srv.URL, record(), later)
			if !errors.Is(err, ErrSignedOut) || !strings.Contains(err.Error(), "gone") {
				t.Fatalf("%s: err = %v, want ErrSignedOut", code, err)
			}
			if c.RefreshToken != "rt-1" {
				t.Fatalf("%s: credentials changed to %+v", code, c)
			}
		}
	})
	t.Run("another failure keeps the record", func(t *testing.T) {
		for _, tt := range []struct {
			status int
			body   string
		}{
			{http.StatusInternalServerError, `oops`},
			{http.StatusBadRequest, `{"error":"invalid_client"}`},
			{http.StatusOK, `{"token_type":"Bearer"}`},
		} {
			srv, _, _ := tokenEndpoint(t, tt.status, tt.body)
			c, err := Refresh(t.Context(), srv.Client(), srv.URL, record(), later)
			if err == nil || errors.Is(err, ErrSignedOut) {
				t.Fatalf("%d %s: err = %v, want a failure that is not a sign-out", tt.status, tt.body, err)
			}
			if c.AccessToken != "at-1" || c.RefreshToken != "rt-1" {
				t.Fatalf("%d %s: credentials changed to %+v", tt.status, tt.body, c)
			}
		}
	})
}

func newTestSession(t *testing.T, srv *httptest.Server, now time.Time) *Session {
	t.Helper()
	s := NewSession(record(), srv.Client())
	s.tokenURL = srv.URL
	s.now = func() time.Time { return now }
	return s
}

func TestSessionToken(t *testing.T) {
	t.Run("a fresh token is returned without a request", func(t *testing.T) {
		srv, n, _ := tokenEndpoint(t, http.StatusOK, refreshed)
		s := newTestSession(t, srv, saved.Add(10*time.Minute))
		if tok, err := s.Token(t.Context()); err != nil || tok != "at-1" || n.Load() != 0 {
			t.Fatalf("Token = %q, %v after %d requests", tok, err, n.Load())
		}
	})
	t.Run("near expiry it is renewed once", func(t *testing.T) {
		srv, n, _ := tokenEndpoint(t, http.StatusOK, refreshed)
		s := newTestSession(t, srv, saved.Add(56*time.Minute))
		for range 2 {
			if tok, err := s.Token(t.Context()); err != nil || tok != "at-2" {
				t.Fatalf("Token = %q, %v", tok, err)
			}
		}
		if n.Load() != 1 {
			t.Fatalf("the token endpoint got %d requests, want 1", n.Load())
		}
	})
	t.Run("a refused refresh signs out for good", func(t *testing.T) {
		srv, n, _ := tokenEndpoint(t, http.StatusBadRequest, `{"error":"refresh_token_reused"}`)
		s := newTestSession(t, srv, saved.Add(56*time.Minute))
		for range 2 {
			if _, err := s.Token(t.Context()); !errors.Is(err, ErrSignedOut) {
				t.Fatalf("Token err = %v, want ErrSignedOut", err)
			}
		}
		if n.Load() != 1 {
			t.Fatalf("the token endpoint got %d requests, want 1", n.Load())
		}
	})
	t.Run("a failed refresh keeps the token while it lasts", func(t *testing.T) {
		srv, _, _ := tokenEndpoint(t, http.StatusServiceUnavailable, `down`)
		s := newTestSession(t, srv, saved.Add(56*time.Minute))
		if tok, err := s.Token(t.Context()); err != nil || tok != "at-1" {
			t.Fatalf("Token = %q, %v, want the current token", tok, err)
		}
		s.now = func() time.Time { return saved.Add(61 * time.Minute) }
		if _, err := s.Token(t.Context()); err == nil || errors.Is(err, ErrSignedOut) {
			t.Fatalf("Token err = %v, want the refresh's failure once the token expired", err)
		}
	})
	t.Run("expire renews the token on the next call", func(t *testing.T) {
		srv, n, _ := tokenEndpoint(t, http.StatusOK, refreshed)
		s := newTestSession(t, srv, saved.Add(10*time.Minute))
		s.Expire()
		if tok, err := s.Token(t.Context()); err != nil || tok != "at-2" || n.Load() != 1 {
			t.Fatalf("Token = %q, %v after %d requests, want a renewed token", tok, err, n.Load())
		}
	})
}
