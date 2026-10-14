package egress

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestUpgradeHopHeaders(t *testing.T) {
	for _, credential := range []string{"", "Bearer configured"} {
		t.Run(credential, func(t *testing.T) {
			called := false
			p := &Proxy{
				Rules: func() Rules {
					return Rules{Allow: hosts("example.com"), Credentials: map[string]string{"example.com": credential}}
				},
				Logger: slog.New(slog.DiscardHandler),
				Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
					called = true
					for _, header := range []string{"Connection", "Proxy-Authorization", "X-Hop-One", "X-Hop-Two"} {
						if got := r.Header.Get(header); got != "" {
							t.Errorf("upstream %s = %q", header, got)
						}
					}
					if got := r.Header.Get("Authorization"); got != credential {
						t.Errorf("upstream Authorization = %q, want %q", got, credential)
					}
					body, err := io.ReadAll(r.Body)
					if err != nil || string(body) != "request" || r.ContentLength != 7 || r.Method != http.MethodPost {
						t.Errorf("upstream request = %s, length %d, body %q, %v", r.Method, r.ContentLength, body, err)
					}
					if r.URL.String() != "https://example.com/path?q=value" || r.Host != "example.com" {
						t.Errorf("upstream target = %s, host %s", r.URL, r.Host)
					}
					if r.Header.Get("X-End-To-End") != "request" {
						t.Error("end-to-end request header missing")
					}
					return &http.Response{StatusCode: http.StatusAccepted, Header: http.Header{
						"Connection": {"X-Hop-One, X-Hop-Two"}, "X-Hop-One": {"one"}, "X-Hop-Two": {"two"},
						"Proxy-Authenticate": {"Basic"}, "X-End-To-End": {"response"},
					}, Body: io.NopCloser(strings.NewReader("response"))}, nil
				}),
			}
			r := httptest.NewRequest(http.MethodPost, "http://example.com/path?q=value", strings.NewReader("request"))
			r.Header["Connection"] = []string{"X-Hop-One", "X-Hop-Two, Authorization"}
			r.Header.Set("X-Hop-One", "one")
			r.Header.Set("X-Hop-Two", "two")
			r.Header.Set("Authorization", "Bearer runner")
			r.Header.Set("Proxy-Authorization", "Basic proxy")
			r.Header.Set("X-End-To-End", "request")
			w := httptest.NewRecorder()
			p.ServeHTTP(w, r)
			if !called || w.Code != http.StatusAccepted || w.Body.String() != "response" {
				t.Fatalf("forwarded=%v, status %d, body %q", called, w.Code, w.Body.String())
			}
			for _, header := range []string{"Connection", "Proxy-Authenticate", "X-Hop-One", "X-Hop-Two"} {
				if got := w.Header().Get(header); got != "" {
					t.Errorf("response %s = %q", header, got)
				}
			}
			if w.Header().Get("X-End-To-End") != "response" {
				t.Error("end-to-end response header missing")
			}
		})
	}
}
