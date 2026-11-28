package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/home-operations/kritika/internal/tuning"
)

func TestStarting(t *testing.T) {
	srv := httptest.NewServer(Starting())
	defer srv.Close()
	tests := []struct {
		name, method, path string
		wantType, wantBody string
	}{
		{name: "the dashboard gets a page", method: http.MethodGet, path: "/", wantType: "text/html; charset=utf-8", wantBody: "kritika is starting"},
		{name: "a dashboard route too", method: http.MethodGet, path: "/reviews/1", wantType: "text/html; charset=utf-8", wantBody: "kritika is starting"},
		{name: "a webhook delivery is refused in plain text", method: http.MethodPost, path: "/hooks/github", wantType: "text/plain; charset=utf-8", wantBody: "waiting for the database"},
		{name: "a head has no body", method: http.MethodHead, path: "/", wantType: "text/html; charset=utf-8"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequest(tt.method, srv.URL+tt.path, nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = resp.Body.Close() }()
			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want 503", resp.StatusCode)
			}
			if got := resp.Header.Get("Retry-After"); got != "5" {
				t.Fatalf("Retry-After = %q, want 5", got)
			}
			if got := resp.Header.Get("Content-Type"); got != tt.wantType {
				t.Fatalf("Content-Type = %q, want %q", got, tt.wantType)
			}
			if !strings.Contains(string(body), tt.wantBody) {
				t.Fatalf("body = %q, want it to contain %q", body, tt.wantBody)
			}
		})
	}
}

func TestSwitch(t *testing.T) {
	sw := NewSwitch(Starting())
	srv := httptest.NewServer(sw)
	defer srv.Close()
	get := func() int {
		resp, err := http.Get(srv.URL + "/")
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	if got := get(); got != http.StatusServiceUnavailable {
		t.Fatalf("before Set: status = %d, want 503", got)
	}
	sw.Set(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	if got := get(); got != http.StatusNoContent {
		t.Fatalf("after Set: status = %d, want 204", got)
	}
}

// TestStartingRoundsTheRetryUp: the header and the page's refresh are in
// whole seconds, rounded up, never 0, which reloads at once.
func TestStartingRoundsTheRetryUp(t *testing.T) {
	for _, tt := range []struct {
		retry time.Duration
		want  string
	}{{500 * time.Millisecond, "1"}, {1500 * time.Millisecond, "2"}, {2 * time.Second, "2"}} {
		t.Run(tt.retry.String(), func(t *testing.T) {
			tuning.Override(t, func(v *tuning.Values) { v.Server.StartingRetry = tt.retry })
			rec := httptest.NewRecorder()
			Starting().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
			if got := rec.Header().Get("Retry-After"); got != tt.want {
				t.Errorf("Retry-After = %q, want %q", got, tt.want)
			}
			if want := `content="` + tt.want + `"`; !strings.Contains(rec.Body.String(), want) {
				t.Errorf("page = %q, want a refresh of %s", rec.Body.String(), want)
			}
		})
	}
}
