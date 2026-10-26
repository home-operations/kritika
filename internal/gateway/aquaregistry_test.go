package gateway

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// aquaFixture is an aqua registry with one package.
const aquaFixture = `packages:
  - type: github_release
    repo_owner: acme
    repo_name: tool
    asset: tool
`

// TestAquaRegistryRefresh: a registry the source answers with replaces the
// one kept once it parses as one with packages; an unchanged one is asked
// for with the last answer's validator, and a failed or broken answer
// leaves the last kept.
func TestAquaRegistryRefresh(t *testing.T) {
	type answer struct {
		status     int
		etag, body string
	}
	var next answer
	var validator string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		validator = r.Header.Get("If-None-Match")
		w.Header().Set("ETag", next.etag)
		w.WriteHeader(next.status)
		_, _ = w.Write([]byte(next.body))
	}))
	t.Cleanup(srv.Close)
	a := &AquaRegistry{URL: srv.URL, Client: srv.Client(), Logger: slog.New(slog.DiscardHandler)}
	updated := aquaFixture + "  - type: github_release\n    repo_owner: acme\n    repo_name: other\n    asset: other\n"
	for _, tt := range []struct {
		name      string
		answer    answer
		fails     bool
		validator string
		kept      string
	}{
		{"the first", answer{http.StatusOK, `"1"`, aquaFixture}, false, "", aquaFixture},
		{"unchanged", answer{http.StatusNotModified, `"1"`, ""}, false, `"1"`, aquaFixture},
		{"a failure", answer{http.StatusBadGateway, "", "down"}, true, `"1"`, aquaFixture},
		{"not a registry", answer{http.StatusOK, `"2"`, "<html>rate limited</html>"}, true, `"1"`, aquaFixture},
		{"no packages", answer{http.StatusOK, `"3"`, "packages: []\n"}, true, `"1"`, aquaFixture},
		{"a newer", answer{http.StatusOK, `"4"`, updated}, false, `"1"`, updated},
		{"after it", answer{http.StatusNotModified, `"4"`, ""}, false, `"4"`, updated},
	} {
		t.Run(tt.name, func(t *testing.T) {
			next = tt.answer
			err := a.refresh(t.Context())
			if (err != nil) != tt.fails {
				t.Fatalf("refresh error = %v, want a failure: %v", err, tt.fails)
			}
			if validator != tt.validator {
				t.Errorf("If-None-Match = %q, want %q", validator, tt.validator)
			}
			if got := string(a.current()); got != tt.kept {
				t.Errorf("kept %q, want %q", got, tt.kept)
			}
		})
	}
}

// TestAquaRegistryRetries: a replica whose source is down when it starts
// asks again within aquaRegistryRetry rather than waiting out Refresh.
func TestAquaRegistryRetries(t *testing.T) {
	was := aquaRegistryRetry
	t.Cleanup(func() { aquaRegistryRetry = was })
	aquaRegistryRetry = 10 * time.Millisecond
	var asked atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if asked.Add(1) == 1 {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(aquaFixture))
	}))
	t.Cleanup(srv.Close)
	a := &AquaRegistry{URL: srv.URL, Refresh: time.Hour, Client: srv.Client(), Logger: slog.New(slog.DiscardHandler)}
	done := make(chan error, 1)
	ctx, cancel := context.WithCancel(t.Context())
	go func() { done <- a.Run(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for a.current() == nil && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v", err)
	}
	if a.current() == nil || asked.Load() != 2 {
		t.Fatalf("kept %q after %d fetches, want the registry after the second", a.current(), asked.Load())
	}
}
