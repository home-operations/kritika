package model

import (
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
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
		name, api, answer string
		status            int
		want              string
	}{
		{"status", RerankCohere, `{"message":"invalid api token"}`, http.StatusUnauthorized, "401"},
		{"index out of range", RerankCohere, `{"results":[{"index":3,"relevance_score":0.3}]}`, http.StatusOK, "index 3"},
		{"duplicate index", RerankCohere, `{"results":[{"index":0,"relevance_score":0.3},{"index":0,"relevance_score":0.2}]}`, http.StatusOK, "index 0"},
		{"not JSON", RerankCohere, `<html>`, http.StatusOK, "decode"},
		{"an empty object", RerankCohere, `{}`, http.StatusOK, "ranks no document"},
		{"null", RerankCohere, `null`, http.StatusOK, "ranks no document"},
		{"null results", RerankJina, `{"results":null}`, http.StatusOK, "ranks no document"},
		{"empty results", RerankCohere, `{"results":[]}`, http.StatusOK, "ranks no document"},
		{"another API's envelope", RerankCohere, `{"data":[{"index":0,"relevance_score":0.3}]}`, http.StatusOK, "ranks no document"},
		{"Cohere's envelope to Voyage", RerankVoyage, `{"results":[{"index":0,"relevance_score":0.3}]}`, http.StatusOK, "ranks no document"},
		{"no index", RerankCohere, `{"results":[{"relevance_score":0.3}]}`, http.StatusOK, "without an index"},
		{"null index", RerankVoyage, `{"data":[{"index":null,"relevance_score":0.3}]}`, http.StatusOK, "without an index"},
		{"no score", RerankCohere, `{"results":[{"index":0}]}`, http.StatusOK, "without a score"},
		{"TEI's score to Cohere", RerankCohere, `{"results":[{"index":0,"score":0.3}]}`, http.StatusOK, "without a score"},
		{"an empty TEI list", RerankTEI, `[]`, http.StatusOK, "ranks no document"},
		{"a null TEI list", RerankTEI, `null`, http.StatusOK, "ranks no document"},
		{"Cohere's score to TEI", RerankTEI, `[{"index":0,"relevance_score":0.3}]`, http.StatusOK, "without a score"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.answer)
			}))
			defer srv.Close()
			_, _, err := NewReranker(tt.api, srv.URL, "key", "m", time.Minute).Rerank(t.Context(), "q", []string{"a", "b"})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want one that says %q", err, tt.want)
			}
		})
	}
}

// TestRerankerInvalidUsage: a count of what a call spent that is negative,
// fractional or past maxRerankUsage, in TEI's header or another API's JSON,
// is taken for none reported, since the gateway settles the run's budget at
// it. The scores still stand.
func TestRerankerInvalidUsage(t *testing.T) {
	for _, tt := range []struct {
		name, api, answer, header string
	}{
		{name: "negative header", api: RerankTEI, header: "-5"},
		{name: "header past int64", api: RerankTEI, header: "99999999999999999999"},
		{name: "header past the bound", api: RerankTEI, header: "4294967296"},
		{name: "fractional header", api: RerankTEI, header: "1.5"},
		{name: "negative tokens", api: RerankCohere, answer: `"meta":{"billed_units":{"input_tokens":-3,"search_units":-1}}`},
		{name: "tokens past float64", api: RerankCohere, answer: `"meta":{"tokens":{"input_tokens":1e400}}`},
		{name: "tokens past the bound", api: RerankJina, answer: `"usage":{"total_tokens":1e19}`},
		{name: "fractional units", api: RerankCohere, answer: `"meta":{"billed_units":{"search_units":0.5}}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tt.header != "" {
					w.Header().Set("X-Compute-Tokens", tt.header)
				}
				if tt.api == RerankTEI {
					_, _ = io.WriteString(w, `[{"index":0,"score":0.8},{"index":1,"score":0.1}]`)
					return
				}
				_, _ = io.WriteString(w, `{"results":[{"index":0,"relevance_score":0.8},{"index":1,"relevance_score":0.1}],`+tt.answer+`}`)
			}))
			defer srv.Close()
			scores, used, err := NewReranker(tt.api, srv.URL, "key", "m", time.Minute).Rerank(t.Context(), "q", []string{"a", "b"})
			if err != nil {
				t.Fatalf("Rerank = %v, want the scores kept", err)
			}
			if !slices.Equal(scores, []float64{0.8, 0.1}) || used != (RerankUsage{}) {
				t.Fatalf("scores %v, usage %+v; want [0.8 0.1] and none reported", scores, used)
			}
		})
	}
}

// TestRerankerErrorBody: a refusal's error carries its status code, not what
// the provider answered, in its body or in its status line's reason phrase,
// either of which can echo the query, a document or a key.
func TestRerankerErrorBody(t *testing.T) {
	for _, tt := range []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"body", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"message":"rejected query: func secretSnippet() with key sk-echoed"}`)
		}},
		{"reason phrase", func(w http.ResponseWriter, r *http.Request) {
			conn, buf, err := http.NewResponseController(w).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			defer func() { _ = conn.Close() }()
			_, _ = buf.WriteString("HTTP/1.1 400 rejected func secretSnippet() with key sk-echoed\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
			_ = buf.Flush()
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(tt.handler)
			defer srv.Close()
			_, _, err := NewReranker(RerankCohere, srv.URL, "key", "m", time.Minute).Rerank(t.Context(), "q", []string{"a"})
			if err == nil || !strings.Contains(err.Error(), "400") || strings.Contains(err.Error(), "secretSnippet") ||
				strings.Contains(err.Error(), "sk-echoed") {
				t.Fatalf("err = %v, want the status code alone", err)
			}
		})
	}
}

// TestRerankerRedirect: a reranker that redirects is refused, and the query
// and documents are not sent on to where it points.
func TestRerankerRedirect(t *testing.T) {
	for _, status := range []int{http.StatusTemporaryRedirect, http.StatusPermanentRedirect, http.StatusFound} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			var reached atomic.Bool
			elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				reached.Store(true)
				_, _ = io.WriteString(w, `[{"index":0,"score":0.5}]`)
			}))
			defer elsewhere.Close()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, elsewhere.URL+"/rerank", status)
			}))
			defer srv.Close()
			_, _, err := NewReranker(RerankTEI, srv.URL, "key", "m", time.Minute).Rerank(t.Context(), "q", []string{"a"})
			if err == nil || !strings.Contains(err.Error(), strconv.Itoa(status)) {
				t.Errorf("err = %v, want the redirect refused", err)
			}
			if reached.Load() {
				t.Error("the request followed the redirect")
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
