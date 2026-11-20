package runner

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"

	"github.com/home-operations/kritika/internal/contextpack"
	"github.com/home-operations/kritika/internal/gitfetch"
	"github.com/home-operations/kritika/internal/runspec"
)

// fakeSimilarGateway answers a run's exclusion report with status, and its
// similar-code requests with answer. It records both.
type fakeSimilarGateway struct {
	status int
	answer contextpack.SimilarResponse

	mu       sync.Mutex
	excluded [][]string
	requests []contextpack.SimilarRequest
}

func (f *fakeSimilarGateway) serve(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.URL.Path {
		case runspec.ReportSimilarExcludePath:
			if r.Header.Get("Authorization") != "Bearer report" {
				t.Errorf("exclusions reported with %q, not the report token", r.Header.Get("Authorization"))
			}
			var v runspec.SimilarExcludeReport
			if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
				t.Error(err)
			}
			f.excluded = append(f.excluded, v.Paths)
			w.WriteHeader(f.status)
		case "/v1/similar":
			var req contextpack.SimilarRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
			}
			f.requests = append(f.requests, req)
			_ = json.NewEncoder(w).Encode(f.answer)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestSimilarContextExclusions: a run reports the paths its searches leave
// out once, both names of a rename among them, and its requests carry none.
// A run whose report is refused searches nothing: the index would answer
// with the old copies of the files the prompt shows.
func TestSimilarContextExclusions(t *testing.T) {
	const diff = "diff --git a/old.go b/new.go\nsimilarity index 80%\nrename from old.go\nrename to new.go\n" +
		"--- a/old.go\n+++ b/new.go\n@@ -1,1 +1,2 @@\n package main\n+func n() {}\n"
	res := &gitfetch.Result{Diff: diff, Changed: []string{"new.go", "main.go"}, RenamedFrom: []string{"old.go"}}
	for _, tt := range []struct {
		name     string
		status   int
		requests int
		offered  bool
	}{
		{name: "reported", status: http.StatusNoContent, requests: 1, offered: true},
		{name: "refused", status: http.StatusBadRequest},
	} {
		t.Run(tt.name, func(t *testing.T) {
			gw := &fakeSimilarGateway{status: tt.status, answer: contextpack.SimilarResponse{Indexed: true}}
			s := reviewSpec()
			s.GatewayURL = gw.serve(t).URL
			_, search := similarContext(t.Context(), s, runspec.Secrets{GatewayToken: "run", ReportToken: "report"}, res,
				slog.New(slog.DiscardHandler))
			if want := [][]string{{"main.go", "new.go", "old.go"}}; !slices.EqualFunc(gw.excluded, want, slices.Equal) {
				t.Fatalf("reported exclusions %q, want %q", gw.excluded, want)
			}
			if len(gw.requests) != tt.requests || (search != nil) != tt.offered {
				t.Fatalf("%d similar requests, search_code offered %t; want %d, %t", len(gw.requests), search != nil, tt.requests, tt.offered)
			}
			for _, req := range gw.requests {
				if req.Exclude != nil {
					t.Fatalf("a request carries exclusions: %q", req.Exclude)
				}
			}
		})
	}
}
