package model

import (
	"context"
	"errors"
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
		{"index out of range", RerankCohere, `{"results":[{"index":3,"relevance_score":0.3}]}`, http.StatusOK, "rank 0 has an invalid or repeated index"},
		{"duplicate index", RerankCohere, `{"results":[{"index":0,"relevance_score":0.3},{"index":0,"relevance_score":0.2}]}`, http.StatusOK,
			"rank 1 has an invalid or repeated index"},
		{"more ranks than documents", RerankCohere,
			`{"results":[{"index":0,"relevance_score":0.3},{"index":1,"relevance_score":0.2},{"index":0,"relevance_score":0.1}]}`, http.StatusOK,
			"ranks more documents than it was sent"},
		{"more TEI ranks than documents", RerankTEI, `[{"index":0,"score":0.3},{"index":1,"score":0.2},{"index":0,"score":0.1}]`, http.StatusOK,
			"ranks more documents than it was sent"},
		{"TEI ranks and more", RerankTEI, `[{"index":0,"score":0.3}] []`, http.StatusOK, "decode"},
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
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want a reranker that never answers to time out", err)
	}
}

// TestRerankerTransportErrorValues: a request that fails in transport fails
// with an error that quotes nothing the provider sent, such as a redirect's
// Location that does not parse, which net/http quotes before it would ask
// whether to follow it, or a malformed header line. A cancellation stays
// one, and a certificate that does not verify says so.
func TestRerankerTransportErrorValues(t *testing.T) {
	raw := func(answer string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			conn, buf, err := http.NewResponseController(w).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			defer func() { _ = conn.Close() }()
			_, _ = buf.WriteString(answer)
			_ = buf.Flush()
		}
	}
	for _, tt := range []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"unparsable redirect", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Location", "/%zz-sk-echoed")
			w.WriteHeader(http.StatusTemporaryRedirect)
		}},
		{"malformed header", raw("HTTP/1.1 200 OK\r\nsk-echoed\r\nContent-Length: 0\r\n\r\n")},
		{"malformed trailer", raw("HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n2\r\n[]\r\n0\r\nsk-echoed\r\n\r\n")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(tt.handler)
			defer srv.Close()
			_, _, err := NewReranker(RerankTEI, srv.URL, "", "m", time.Minute).Rerank(t.Context(), "q", []string{"a"})
			if err == nil || strings.Contains(err.Error(), "sk-echoed") {
				t.Fatalf("err = %v, want a failure that quotes none of the answer", err)
			}
		})
	}
	t.Run("canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		srv := httptest.NewServer(http.NotFoundHandler())
		defer srv.Close()
		_, _, err := NewReranker(RerankTEI, srv.URL, "", "m", time.Minute).Rerank(ctx, "q", []string{"a"})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	})
	t.Run("unverified certificate", func(t *testing.T) {
		srv := httptest.NewTLSServer(http.NotFoundHandler())
		defer srv.Close()
		_, _, err := NewReranker(RerankTEI, srv.URL, "", "m", time.Minute).Rerank(t.Context(), "q", []string{"a"})
		if err == nil || !strings.Contains(err.Error(), "certificate does not verify") {
			t.Fatalf("err = %v, want the certificate named", err)
		}
	})
}

// TestRerankerTransportErrorEndpoint: a reranker that cannot be reached
// fails with an error that does not name its endpoint, whose path can carry
// a key, since the gateway logs the error.
func TestRerankerTransportErrorEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	base := srv.URL + "/v1/sk-in-path"
	srv.Close()
	_, _, err := NewReranker(RerankCohere, base, "", "m", time.Minute).Rerank(t.Context(), "q", []string{"a"})
	if err == nil || strings.Contains(err.Error(), "sk-in-path") || !strings.Contains(err.Error(), "cannot be reached") {
		t.Fatalf("err = %v, want a failure to reach the endpoint that does not name it", err)
	}
}

// TestRerankerRefusalKeepsUsage: an answer refused for its ranks still
// reports what the provider said it spent, in its JSON or in TEI's header,
// which the gateway charges the run.
func TestRerankerRefusalKeepsUsage(t *testing.T) {
	for _, tt := range []struct {
		name, api, answer, header string
		want                      RerankUsage
	}{
		{name: "a Cohere rank without its score", api: RerankCohere,
			answer: `{"results":[{"index":0}],"meta":{"billed_units":{"search_units":1,"input_tokens":12}}}`,
			want:   RerankUsage{Tokens: 12, SearchUnits: 1}},
		{name: "a Cohere rank out of range", api: RerankCohere,
			answer: `{"results":[{"index":5,"relevance_score":0.3}],"meta":{"billed_units":{"search_units":1}}}`,
			want:   RerankUsage{SearchUnits: 1}},
		{name: "a Cohere score of the wrong type", api: RerankCohere,
			answer: `{"results":[{"index":0,"relevance_score":"0.5"}],"meta":{"billed_units":{"search_units":1,"input_tokens":12}}}`,
			want:   RerankUsage{Tokens: 12, SearchUnits: 1}},
		{name: "a Voyage index of the wrong type, before the usage", api: RerankVoyage,
			answer: `{"data":[{"index":"0","relevance_score":0.5}],"usage":{"total_tokens":9}}`,
			want:   RerankUsage{Tokens: 9}},
		{name: "Jina ranks of the wrong type", api: RerankJina,
			answer: `{"results":{"index":0},"usage":{"total_tokens":4}}`,
			want:   RerankUsage{Tokens: 4}},
		{name: "Cohere search units that are not a number, beside its tokens", api: RerankCohere,
			answer: `{"results":[{"index":0,"relevance_score":0.5}],"meta":{"billed_units":{"search_units":"many","input_tokens":12}}}`,
			want:   RerankUsage{Tokens: 12}},
		{name: "Cohere billed units of the wrong type, beside its tokens", api: RerankCohere,
			answer: `{"results":[{"index":0,"relevance_score":0.5}],"meta":{"billed_units":[1],"tokens":{"input_tokens":15}}}`,
			want:   RerankUsage{Tokens: 15}},
		{name: "more Voyage ranks than documents, before the usage", api: RerankVoyage,
			answer: `{"data":[{"index":0,"relevance_score":0.5},{"index":1,"relevance_score":0.4}],"usage":{"total_tokens":9}}`,
			want:   RerankUsage{Tokens: 9}},
		{name: "a TEI answer that is not JSON", api: RerankTEI, answer: `[{"index":0,`, header: "33", want: RerankUsage{Tokens: 33}},
		{name: "a TEI rank without its score", api: RerankTEI, answer: `[{"index":0}]`, header: "7", want: RerankUsage{Tokens: 7}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tt.header != "" {
					w.Header().Set("X-Compute-Tokens", tt.header)
				}
				_, _ = io.WriteString(w, tt.answer)
			}))
			defer srv.Close()
			scores, used, err := NewReranker(tt.api, srv.URL, "", "m", time.Minute).Rerank(t.Context(), "q", []string{"a"})
			if err == nil || scores != nil || used != tt.want {
				t.Fatalf("Rerank = %v, %+v, %v; want a refusal that reports %+v spent", scores, used, err, tt.want)
			}
		})
	}
}

// TestRerankerCutShortKeepsUsage: a TEI answer whose body ends before its
// Content-Length still reports the tokens its header says were computed,
// which the gateway charges the run.
func TestRerankerCutShortKeepsUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		conn, buf, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = conn.Close() }()
		_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nX-Compute-Tokens: 21\r\nContent-Length: 100\r\n\r\n[{\"index\":0,")
		_ = buf.Flush()
	}))
	defer srv.Close()
	scores, used, err := NewReranker(RerankTEI, srv.URL, "", "m", time.Minute).Rerank(t.Context(), "q", []string{"a"})
	if err == nil || scores != nil || used != (RerankUsage{Tokens: 21}) {
		t.Fatalf("Rerank = %v, %+v, %v; want a failure that reports 21 tokens spent", scores, used, err)
	}
}

// TestRerankerDecodeErrorValues: an answer that does not decode fails with
// an error that quotes none of it, since a provider can echo the query, a
// document or a key into any of its fields, and the gateway logs the error.
func TestRerankerDecodeErrorValues(t *testing.T) {
	for _, tt := range []struct {
		name, api, answer string
	}{
		{"usage that is not a number", RerankJina, `{"results":[{"index":0,"relevance_score":0.5}],"usage":{"total_tokens":"sk-echoed"}}`},
		{"billed units that are not a number", RerankCohere, `{"results":[{"index":0,"relevance_score":0.5}],"meta":{"billed_units":{"search_units":"sk-echoed"}}}`},
		{"an index of the wrong type", RerankVoyage, `{"data":[{"index":"sk-echoed","relevance_score":0.5}]}`},
		{"an index past an int", RerankTEI, `[{"index":12345678901234567890123,"score":0.5}]`},
		{"invalid JSON", RerankTEI, `[{"index":0,"score":sk-echoed}]`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, tt.answer)
			}))
			defer srv.Close()
			_, _, err := NewReranker(tt.api, srv.URL, "", "m", time.Minute).Rerank(t.Context(), "q", []string{"a"})
			if err == nil || !strings.Contains(err.Error(), "decode") ||
				strings.Contains(err.Error(), "sk-echoed") || strings.Contains(err.Error(), "12345678901234567890123") {
				t.Fatalf("err = %v, want a decode failure that quotes none of the answer", err)
			}
		})
	}
}

// TestRerankerInvalidIndexValue: a rank whose index is out of range fails
// with an error that names the rank's place in the answer, not the index the
// provider sent, which it can have echoed from the query or a document.
func TestRerankerInvalidIndexValue(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `[{"index":0,"score":0.5},{"index":987654321,"score":0.4}]`)
	}))
	defer srv.Close()
	_, _, err := NewReranker(RerankTEI, srv.URL, "", "m", time.Minute).Rerank(t.Context(), "q", []string{"a", "b"})
	if err == nil || !strings.Contains(err.Error(), "rank 1") || strings.Contains(err.Error(), "987654321") {
		t.Fatalf("err = %v, want one that names rank 1 and not the index it sent", err)
	}
}

// TestRerankerDecodeStopsPastTheDocuments: an answer that ranks far more
// documents than were sent is refused once it ranks one more, without
// decoding the rest, whose ranks would otherwise take memory in proportion to
// the answer rather than to the documents.
func TestRerankerDecodeStopsPastTheDocuments(t *testing.T) {
	ranks := strings.Repeat(`{"index":0,"score":0.5,"relevance_score":0.5},`, 100_000)
	ranks = "[" + strings.TrimSuffix(ranks, ",") + "]"
	for _, tt := range []struct{ api, answer string }{
		{RerankTEI, ranks},
		{RerankCohere, `{"results":` + ranks + `}`},
	} {
		t.Run(tt.api, func(t *testing.T) {
			r := NewReranker(tt.api, "http://unused.invalid", "", "m", time.Minute)
			answer := []byte(tt.answer)
			var err error
			allocs := testing.AllocsPerRun(1, func() { _, _, err = r.decode(answer, 2) })
			if err == nil || !strings.Contains(err.Error(), "ranks more documents than it was sent") {
				t.Errorf("err = %v, want a refusal of the ranks past the documents", err)
			}
			if allocs > 100 {
				t.Errorf("decoding took %v allocations, want those of the first ranks alone", allocs)
			}
		})
	}
}

// TestRerankerOversizedAnswer: an answer longer than maxRerankAnswer is
// refused, even when the part of it within the bound is a whole ranking, and
// still reports the tokens TEI's header says were computed.
func TestRerankerOversizedAnswer(t *testing.T) {
	ranking := `[{"index":0,"score":0.9}]`
	answer := ranking + strings.Repeat(" ", maxRerankAnswer-len(ranking)) + "x"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Compute-Tokens", "17")
		_, _ = io.WriteString(w, answer)
	}))
	defer srv.Close()
	scores, used, err := NewReranker(RerankTEI, srv.URL, "", "m", time.Minute).Rerank(t.Context(), "q", []string{"a"})
	if err == nil || scores != nil || used != (RerankUsage{Tokens: 17}) {
		t.Fatalf("Rerank = %v, %+v, %v; want a refusal that reports 17 tokens spent", scores, used, err)
	}
}
