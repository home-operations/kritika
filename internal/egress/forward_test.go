package egress

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/home-operations/kritika/internal/egresspolicy"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestUpgradeHopHeaders(t *testing.T) {
	for _, tt := range []struct {
		name        string
		credentials map[string]string
	}{
		{"unconfigured", nil},
		{"configured", map[string]string{"example.com": "Bearer configured"}},
		{"empty", map[string]string{"example.com": ""}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			p := &Proxy{
				Rules: func() egresspolicy.Rules {
					return egresspolicy.Rules{Allow: hosts("example.com"), Credentials: tt.credentials}
				},
				Logger: slog.New(slog.DiscardHandler),
				Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
					called = true
					for _, header := range []string{"Connection", "Proxy-Authorization", "X-Hop-One", "X-Hop-Two"} {
						if got := r.Header.Get(header); got != "" {
							t.Errorf("upstream %s = %q", header, got)
						}
					}
					credential, configured := tt.credentials["example.com"]
					if _, present := r.Header["Authorization"]; present != configured || r.Header.Get("Authorization") != credential {
						t.Errorf("upstream Authorization = %q, present=%v; want %q, present=%v", r.Header.Values("Authorization"), present, credential, configured)
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

func TestForwardRequestCompatibility(t *testing.T) {
	for _, tt := range []struct {
		name      string
		userAgent *string
		want      string
	}{
		{"missing", nil, "kritika"},
		{"explicit", new("runner/1"), "runner/1"},
		{"suppressed", new(""), ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.RawQuery != "z=1;y=2&a=%zz&b=3" {
					t.Errorf("query = %q", r.URL.RawQuery)
				}
				if got := r.Header.Get("User-Agent"); got != tt.want {
					t.Errorf("User-Agent = %q, want %q", got, tt.want)
				}
				for _, h := range []string{"Connection", "Upgrade", "X-Forwarded-Host"} {
					if _, present := r.Header[h]; present {
						t.Errorf("hop header %s forwarded", h)
					}
				}
				if got := r.Header.Values("Forwarded"); !slices.Equal(got, []string{"for=example", "proto=http"}) {
					t.Errorf("Forwarded = %q", got)
				}
				if r.Header.Get("X-Forwarded-For") != "192.0.2.1" || r.Header.Get("X-Forwarded-Proto") != "http" {
					t.Errorf("forwarding headers = %v", r.Header)
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			defer upstream.Close()
			transport := upstream.Client().Transport.(*http.Transport).Clone()
			transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, upstream.Listener.Addr().String())
			}
			defer transport.CloseIdleConnections()
			p := &Proxy{
				Rules:  func() egresspolicy.Rules { return egresspolicy.Rules{Allow: hosts("example.com")} },
				Logger: slog.New(slog.DiscardHandler), Transport: transport,
			}
			r := httptest.NewRequest(http.MethodGet, "http://example.com/?z=1;y=2&a=%zz&b=3", nil)
			if tt.userAgent != nil {
				r.Header.Set("User-Agent", *tt.userAgent)
			}
			r.Header["Connection"] = []string{"upgrade", "x-FORWARDED-host"}
			r.Header.Set("Upgrade", "websocket")
			r.Header["Forwarded"] = []string{"for=example", "proto=http"}
			r.Header.Set("X-Forwarded-For", "192.0.2.1")
			r.Header.Set("X-Forwarded-Host", "example.com")
			r.Header.Set("X-Forwarded-Proto", "http")
			w := httptest.NewRecorder()
			p.ServeHTTP(w, r)
			if w.Code != http.StatusNoContent {
				t.Fatalf("status = %d, body %q", w.Code, w.Body.String())
			}
		})
	}
}

func TestForwardRejectsProtocolSwitch(t *testing.T) {
	outcomes := []string{}
	p := &Proxy{
		Rules: func() egresspolicy.Rules {
			return egresspolicy.Rules{Allow: hosts("example.com"), Credentials: map[string]string{"example.com": "Bearer configured"}}
		},
		Logger:  slog.New(slog.DiscardHandler),
		Observe: func(kind, outcome string) { outcomes = append(outcomes, kind+"/"+outcome) },
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.Header.Get("Upgrade") != "" || r.Header.Get("Connection") != "" {
				t.Error("upgrade forwarded")
			}
			return &http.Response{StatusCode: http.StatusSwitchingProtocols,
				Header: http.Header{"Connection": {"Upgrade"}, "Upgrade": {"websocket"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
		}),
	}
	r := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	r.Header.Set("Connection", "Upgrade")
	r.Header.Set("Upgrade", "websocket")
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r)
	if w.Code != http.StatusBadGateway || !slices.Equal(outcomes, []string{"http/error"}) {
		t.Fatalf("status=%d, outcomes=%v", w.Code, outcomes)
	}
}
