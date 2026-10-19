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
				if !slices.Equal(req.Exclude, []string{"changed.go"}) || r.Header.Get("Authorization") != "Bearer t" {
					t.Errorf("request exclusions or token changed: %v", req.Exclude)
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
			s.Model.GatewayURL, s.Agent.Parallel = gw.URL, parallel
			diffs := make([]string, count)
			for i := range diffs {
				diffs[i] = sizedDiff(fmt.Sprintf("part%d.go", i), 80)
			}
			out, err := partSimilarCode(ctx, s, Secrets{GatewayToken: "t"}, diffs, []string{"changed.go"}, slog.New(slog.DiscardHandler))
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
	s.Model.GatewayURL, s.Agent.Parallel = gw.URL, 2
	diffs := []string{sizedDiff("failed.go", 80), sizedDiff("ok.go", 80), sizedDiff("unindexed.go", 80), ""}
	out, err := partSimilarCode(t.Context(), s, Secrets{}, diffs, nil, slog.New(slog.DiscardHandler))
	if err != nil || len(out) != len(diffs) || out[0].Indexed || !out[1].Indexed || out[2].Indexed || out[3].Indexed {
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
	s.Model.GatewayURL, s.Agent.Parallel = gw.URL, 2
	done := make(chan error, 1)
	go func() {
		_, err := partSimilarCode(ctx, s, Secrets{}, []string{
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
