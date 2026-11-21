package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/home-operations/kritika/internal/contextpack"
	"github.com/home-operations/kritika/internal/gitfetch"
	"github.com/home-operations/kritika/internal/repoconfig"
	"github.com/home-operations/kritika/internal/review"
	"github.com/home-operations/kritika/internal/runspec"
)

func TestPartSimilarCode(t *testing.T) {
	for _, parallel := range []int{0, 1, 2, 8} {
		t.Run(fmt.Sprint(parallel), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			const count = 4
			width := min(count, max(1, parallel))
			var mu sync.Mutex
			var running, most, started int
			together := make(chan struct{})
			gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req contextpack.SimilarRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					return
				}
				if req.Exclude != nil || r.Header.Get("Authorization") != "Bearer t" {
					t.Errorf("request exclusions %v or token changed", req.Exclude)
				}
				mu.Lock()
				running++
				most = max(most, running)
				started++
				if started == width {
					close(together)
				}
				mu.Unlock()
				select {
				case <-together:
				case <-ctx.Done():
					t.Error("requests did not overlap")
				}
				path, _, _ := strings.Cut(req.Queries[0], "\n")
				mu.Lock()
				running--
				mu.Unlock()
				_ = json.NewEncoder(w).Encode(contextpack.SimilarResponse{Indexed: true, Chunks: []contextpack.Chunk{{Path: path}}})
			}))
			t.Cleanup(gw.Close)
			s := reviewSpec()
			s.GatewayURL, s.Agent.Slots = gw.URL, defaultSlots(parallel)
			diffs := make([]string, count)
			for i := range diffs {
				diffs[i] = fileDiff("", fmt.Sprintf("part%d.go", i), added(fmt.Sprintf("part%d.go", i)))
			}
			out, err := partSimilarCode(ctx, s, runspec.Secrets{GatewayToken: "t"}, diffs, nil, slog.New(slog.DiscardHandler))
			if err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			defer mu.Unlock()
			if most != width || started != count {
				t.Fatalf("peak concurrency %d, want %d; %d requests, want %d", most, width, started, count)
			}
			for i, response := range out {
				if !response.Indexed || len(response.Chunks) != 1 || response.Chunks[0].Path != fmt.Sprintf("part%d.go", i) {
					t.Fatalf("part %d received %+v", i, response)
				}
			}
		})
	}
}

func TestPartSimilarCodeOptionalFailure(t *testing.T) {
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req contextpack.SimilarRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		switch {
		case len(req.Queries) == 0:
			t.Error("a part with nothing to search for asked")
		case strings.HasPrefix(req.Queries[0], "failed.go"):
			http.Error(w, "unavailable", http.StatusBadGateway)
		case strings.HasPrefix(req.Queries[0], "unindexed.go"):
			_ = json.NewEncoder(w).Encode(contextpack.SimilarResponse{})
		default:
			_ = json.NewEncoder(w).Encode(contextpack.SimilarResponse{Indexed: true, Chunks: []contextpack.Chunk{{Path: "lib.go"}}})
		}
	}))
	t.Cleanup(gw.Close)
	s := reviewSpec()
	s.GatewayURL, s.Agent.Slots = gw.URL, defaultSlots(2)
	diffs := []string{fileDiff("", "failed.go", added("failed.go")), fileDiff("", "ok.go", added("ok.go")), fileDiff("", "unindexed.go", added("unindexed.go")), ""}
	out, err := partSimilarCode(t.Context(), s, runspec.Secrets{}, diffs, nil, slog.New(slog.DiscardHandler))
	if err != nil || len(out) != len(diffs) || out[0].Indexed || !out[1].Indexed || out[2].Indexed || !out[3].Indexed {
		t.Fatalf("optional retrieval = %+v, %v", out, err)
	}
}

func TestPartSimilarCodeCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var requests atomic.Int32
	started := make(chan struct{}, 2)
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req contextpack.SimilarRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		requests.Add(1)
		started <- struct{}{}
		select {
		case <-r.Context().Done():
		case <-ctx.Done():
		}
	}))
	t.Cleanup(gw.Close)
	s := reviewSpec()
	s.GatewayURL, s.Agent.Slots = gw.URL, defaultSlots(2)
	done := make(chan error, 1)
	go func() {
		_, err := partSimilarCode(ctx, s, runspec.Secrets{}, []string{
			sizedDiff("a.go", 80), sizedDiff("b.go", 80), sizedDiff("c.go", 80), sizedDiff("d.go", 80),
		}, nil, slog.New(slog.DiscardHandler))
		done <- err
	}()
	for range 2 {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			cancel()
			t.Fatal("retrieval did not start")
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) || requests.Load() != 2 {
			t.Fatalf("canceled retrieval = %v, %d requests; want only the two in flight", err, requests.Load())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("retrieval did not stop on cancellation")
	}
}

// TestPartPromptsCheckSharedSimilarCodeAtTheHeadOnce: reviewers that review
// the same files share one similar-code answer, which is checked at the head
// once: the file of a hit under a declaration is read, and parsed, for both.
func TestPartPromptsCheckSharedSimilarCodeAtTheHeadOnce(t *testing.T) {
	const lib = "package lib\n\nfunc Z() {}\n"
	head, reads, blobs := countedHead(t, map[string]string{"x.go": "package x\n", "z.go": lib})
	gw := &fakeSimilarGateway{status: http.StatusNoContent, answer: contextpack.SimilarResponse{Indexed: true, Chunks: []contextpack.Chunk{
		{Stage: contextpack.StageSimilar, Path: "z.go", Symbol: "Z", Kind: "function", StartLine: 3, EndLine: 3, Text: "func Z() {}"},
	}}}
	// The context stages read the head before the exclusions are reported,
	// and the similar code is checked at it after.
	gw.reported = func(int) {
		reads.mu.Lock()
		defer reads.mu.Unlock()
		clear(reads.read)
	}
	s := reviewSpec()
	s.GatewayURL, s.Agent.Slots = gw.serve(t).URL, defaultSlots(2)
	paths := []string{"x.go"}
	shares := []partShare{
		{reviewer: &runspec.Reviewer{Name: review.DefaultReviewer}, scoped: paths, paths: paths, index: 1, count: 1, scope: review.ScopeFull},
		{reviewer: &runspec.Reviewer{Name: "security"}, scoped: paths, paths: paths, index: 1, count: 1, scope: review.ScopeFull},
	}
	res := &gitfetch.Result{Diff: fileDiff("", "x.go", added("package x")), Changed: paths}
	var tools agentTools
	parts, _, _, err := partPrompts(t.Context(), s, runspec.Secrets{GatewayToken: "t", ReportToken: "report"}, head, head, res,
		repoconfig.Files{}, nil, shares, &tools, nil, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	for i, part := range parts {
		if !slices.ContainsFunc(part.prompt.context, func(c contextpack.Chunk) bool { return c.Path == "z.go" && c.Symbol == "Z" }) {
			t.Fatalf("part %d's context %+v lacks the similar chunk", i+1, part.prompt.context)
		}
	}
	if n := reads.reads(blobs["z.go"]); n != 1 {
		t.Fatalf("the similar chunk's file was read %d times, want once for the reviewers that share its answer", n)
	}
}
