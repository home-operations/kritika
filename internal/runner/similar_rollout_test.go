package runner

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/home-operations/kritika/internal/contextpack"
	"github.com/home-operations/kritika/internal/gitfetch"
	"github.com/home-operations/kritika/internal/runspec"
)

// TestSimilarSearchOnAnOlderReplica: mid-rollout, a run's probe and its
// exclusion report can reach a gateway of its own build while a search
// reaches an older replica, which takes the paths a search leaves out from
// the request alone and so would answer with the changed file's own copy.
// The run's searches use a route that the older replica does not serve, so
// that search fails and the run goes without similar code.
func TestSimilarSearchOnAnOlderReplica(t *testing.T) {
	stale := contextpack.SimilarResponse{Indexed: true, Chunks: []contextpack.Chunk{
		{Stage: contextpack.StageSimilar, Path: "main.go", StartLine: 1, EndLine: 1, Text: "func n() {}"},
	}}
	older := http.NewServeMux()
	older.HandleFunc("POST /v1/similar", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(stale)
	})
	newer := http.NewServeMux()
	// A gateway of the run's build serves both routes, as runners of both
	// builds call it.
	probe := func(w http.ResponseWriter, r *http.Request) {
		var req contextpack.SimilarRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		if len(req.Queries) > 0 {
			t.Error("a search reached the replica of the run's own build")
		}
		_ = json.NewEncoder(w).Encode(contextpack.SimilarResponse{Indexed: true, Chunks: []contextpack.Chunk{}})
	}
	newer.HandleFunc("POST "+runspec.SimilarPath, probe)
	newer.HandleFunc("POST /v1/similar", probe)
	newer.HandleFunc("POST "+runspec.ReportSimilarExcludePath, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	// The probe and the report reach the newer replica, every search the
	// older one.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == runspec.ReportSimilarExcludePath {
			newer.ServeHTTP(w, r)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		var req contextpack.SimilarRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Error(err)
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		if len(req.Queries) == 0 {
			newer.ServeHTTP(w, r)
			return
		}
		older.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	const diff = "diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go\n@@ -1,1 +1,2 @@\n package main\n+func n() {}\n"
	s := reviewSpec()
	s.GatewayURL = srv.URL
	secrets := runspec.Secrets{GatewayToken: "run", ReportToken: "report"}
	logger := slog.New(slog.DiscardHandler)
	if !excludeFromSimilar(t.Context(), s, secrets, &gitfetch.Result{Diff: diff, Changed: []string{"main.go"}}, logger) {
		t.Fatal("the probe and the report on the newer replica did not ready the searches")
	}
	if out, ok := similarCodeFor(t.Context(), s, secrets, diff, logger); ok || len(out.Chunks) > 0 {
		t.Fatalf("the older replica's search was used: %+v, %t", out, ok)
	}
	if out, err := similarCode(t.Context(), srv.URL, "run", contextpack.SimilarRequest{
		Queries: []string{"func n"}, Kind: contextpack.SimilarKindSearch,
	}); err == nil {
		t.Fatalf("search_code used the older replica's answer: %+v", out)
	}
}
