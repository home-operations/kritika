package model

import (
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestReranker(t *testing.T) {
	tests := []struct {
		api string
		// path is where the request goes under the base URL, and want its
		// body.
		path, want string
		// answer is the server's, header its extra header.
		answer, header string
		scores         []float64
		used           RerankUsage
	}{
		{
			api: RerankCohere, path: "/v2/rerank",
			want:   `{"model":"rerank-v3.5","query":"q","documents":["a","b","c"],"top_n":3}`,
			answer: `{"id":"x","results":[{"index":2,"relevance_score":0.9},{"index":0,"relevance_score":0.5},{"index":1,"relevance_score":0.1}],"meta":{"billed_units":{"search_units":1,"input_tokens":12}}}`,
			scores: []float64{0.5, 0.1, 0.9}, used: RerankUsage{Tokens: 12, SearchUnits: 1},
		},
		{
			api: RerankJina, path: "/v1/rerank",
			want:   `{"model":"jina-reranker-v3","query":"q","documents":["a","b","c"],"top_n":3,"return_documents":false}`,
			answer: `{"model":"jina-reranker-v3","usage":{"total_tokens":20},"results":[{"index":1,"relevance_score":0.7},{"index":0,"relevance_score":0.6},{"index":2,"relevance_score":0.2}]}`,
			scores: []float64{0.6, 0.7, 0.2}, used: RerankUsage{Tokens: 20},
		},
		{
			api: RerankVoyage, path: "/v1/rerank",
			want:   `{"model":"rerank-2.5","query":"q","documents":["a","b","c"],"top_k":3,"truncation":true}`,
			answer: `{"object":"list","data":[{"relevance_score":0.4,"index":0},{"relevance_score":0.3,"index":2},{"relevance_score":0.2,"index":1}],"model":"rerank-2.5","usage":{"total_tokens":8}}`,
			scores: []float64{0.4, 0.2, 0.3}, used: RerankUsage{Tokens: 8},
		},
		{
			api: RerankTEI, path: "/rerank",
			want:   `{"query":"q","texts":["a","b","c"],"truncate":true}`,
			answer: `[{"index":1,"score":0.8},{"index":2,"score":0.05},{"index":0,"score":0.01}]`, header: "33",
			scores: []float64{0.01, 0.8, 0.05}, used: RerankUsage{Tokens: 33},
		},
		{
			// Billed by the search unit, with the tokens read beside.
			api: RerankCohere, path: "/v2/rerank",
			want:   `{"model":"rerank-v3.5","query":"q","documents":["a","b","c"],"top_n":3}`,
			answer: `{"results":[{"index":0,"relevance_score":0.2}],"meta":{"billed_units":{"search_units":1},"tokens":{"input_tokens":40}}}`,
			scores: []float64{0.2, -1, -1}, used: RerankUsage{Tokens: 40, SearchUnits: 1},
		},
		{
			// A document the answer leaves out ranks last.
			api: RerankCohere, path: "/v2/rerank",
			want:   `{"model":"rerank-v3.5","query":"q","documents":["a","b","c"],"top_n":3}`,
			answer: `{"results":[{"index":1,"relevance_score":0.3}]}`,
			scores: []float64{-1, 0.3, -1},
		},
	}
	models := map[string]string{RerankCohere: "rerank-v3.5", RerankJina: "jina-reranker-v3", RerankVoyage: "rerank-2.5", RerankTEI: "bge"}
	for _, tt := range tests {
		t.Run(tt.api, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != tt.path {
					t.Errorf("request %s %s, want POST %s", r.Method, r.URL.Path, tt.path)
				}
				if got := r.Header.Get("Authorization"); got != "Bearer key" {
					t.Errorf("Authorization = %q", got)
				}
				body, _ := io.ReadAll(r.Body)
				if string(body) != tt.want {
					t.Errorf("body = %s, want %s", body, tt.want)
				}
				if tt.header != "" {
					w.Header().Set("X-Compute-Tokens", tt.header)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tt.answer)
			}))
			defer srv.Close()
			r := NewReranker(tt.api, srv.URL+strings.TrimSuffix(tt.path, "/rerank"), "key", models[tt.api], time.Minute)
			scores, used, err := r.Rerank(t.Context(), "q", []string{"a", "b", "c"})
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(scores, tt.scores) || used != tt.used {
				t.Errorf("scores %v, usage %+v; want %v, %+v", scores, used, tt.scores, tt.used)
			}
		})
	}
}

func TestRerankerRefusals(t *testing.T) {
	for _, tt := range []struct {
		name, answer string
		status       int
		want         string
	}{
		{"status", `{"message":"invalid api token"}`, http.StatusUnauthorized, "401"},
		{"index out of range", `{"results":[{"index":3,"relevance_score":0.3}]}`, http.StatusOK, "index 3"},
		{"duplicate index", `{"results":[{"index":0,"relevance_score":0.3},{"index":0,"relevance_score":0.2}]}`, http.StatusOK, "index 0"},
		{"not JSON", `<html>`, http.StatusOK, "decode"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.answer)
			}))
			defer srv.Close()
			_, _, err := NewReranker(RerankCohere, srv.URL, "key", "m", time.Minute).Rerank(t.Context(), "q", []string{"a", "b"})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want one that says %q", err, tt.want)
			}
		})
	}
}

func TestRerankerTimeout(t *testing.T) {
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-done
	}))
	defer srv.Close()
	defer close(done)
	_, _, err := NewReranker(RerankTEI, srv.URL, "", "m", 50*time.Millisecond).Rerank(t.Context(), "q", []string{"a"})
	if err == nil {
		t.Fatal("a reranker that never answers must time out")
	}
}
