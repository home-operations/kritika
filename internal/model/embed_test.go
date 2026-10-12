package model

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAIEmbedderBatchesAndChecksDims(t *testing.T) {
	var batches [][]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input      []string `json:"input"`
			Dimensions int      `json:"dimensions"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		batches = append(batches, req.Input)
		data := make([]string, 0, len(req.Input))
		for i := range req.Input {
			data = append(data, `{"object":"embedding","index":`+strconvInt(i)+`,"embedding":[0.1,0.2,0.3]}`)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","model":"emb","data":[` + strings.Join(data, ",") + `],"usage":{"prompt_tokens":1,"total_tokens":1}}`))
	}))
	defer srv.Close()
	e := NewOpenAIEmbedder(srv.URL, "k", "emb", 3)
	e.MaxBatch = 2
	e.MaxItemChars = 5
	vecs, tokens, err := e.Embed(t.Context(), []string{"a", "b", "c", "dddddddddd"})
	if err != nil {
		t.Fatal(err)
	}
	if tokens != 2 {
		t.Fatalf("tokens = %d, want the two batches' usage summed", tokens)
	}
	if len(vecs) != 4 || len(vecs[0]) != 3 || len(batches) != 2 || len(batches[0]) != 2 || batches[1][1] != "ddddd" {
		t.Fatalf("vecs=%d batches=%v", len(vecs), batches)
	}
	wrong := NewOpenAIEmbedder(srv.URL, "k", "emb", 8)
	if _, _, err := wrong.Embed(t.Context(), []string{"x"}); err == nil {
		t.Fatal("a dimension mismatch must be an error")
	}
}

func strconvInt(i int) string {
	b, _ := json.Marshal(i)
	return string(b)
}

func TestOpenAIEmbedderErrorSaysWhy(t *testing.T) {
	srv, _ := fakeProvider(t, http.StatusBadRequest, `{"error":{"message":"input too long"}}`)
	_, _, err := NewOpenAIEmbedder(srv.URL, "k", "emb", 3).Embed(t.Context(), []string{"a"})
	if err == nil || !strings.Contains(err.Error(), "400") || !strings.Contains(err.Error(), "input too long") {
		t.Fatalf("err = %v, want the status and the provider's reason", err)
	}
}

func TestOpenAIEmbedderResponseIndices(t *testing.T) {
	tests := []struct {
		name      string
		data      string
		wantError bool
	}{
		{"omitted", `[{"embedding":[1,0]},{"embedding":[0,1]}]`, false},
		{"mixed missing first", `[{"embedding":[1,0]},{"index":1,"embedding":[0,1]}]`, true},
		{"mixed missing last", `[{"index":0,"embedding":[1,0]},{"embedding":[0,1]}]`, true},
		{"null", `[{"index":null,"embedding":[1,0]},{"index":1,"embedding":[0,1]}]`, true},
		{"invalid type", `[{"index":"zero","embedding":[1,0]},{"index":1,"embedding":[0,1]}]`, true},
		{"reordered", `[{"index":1,"embedding":[0,1]},{"index":0,"embedding":[1,0]}]`, false},
		{"duplicate", `[{"index":0,"embedding":[1,0]},{"index":0,"embedding":[0,1]}]`, true},
		{"negative", `[{"index":-1,"embedding":[1,0]},{"index":1,"embedding":[0,1]}]`, true},
		{"out of range", `[{"index":0,"embedding":[1,0]},{"index":2,"embedding":[0,1]}]`, true},
		{"missing vector", `[{"index":0,"embedding":[1,0]}]`, true},
		{"wrong dimensions", `[{"index":0,"embedding":[1]},{"index":1,"embedding":[0,1]}]`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"object":"list","model":"emb","data":` + tt.data + `,"usage":{"prompt_tokens":7,"total_tokens":7}}`))
			}))
			defer srv.Close()
			e := NewOpenAIEmbedder(srv.URL, "k", "emb", 2)
			e.MaxBatch = 2
			got, tokens, err := e.Embed(t.Context(), []string{"first", "second", "third", "fourth"})
			if (err != nil) != tt.wantError {
				t.Fatalf("Embed error = %v, want error %v", err, tt.wantError)
			}
			wantTokens := int64(14)
			if tt.wantError {
				wantTokens = 7
			}
			if tokens != wantTokens {
				t.Fatalf("tokens = %d, want %d", tokens, wantTokens)
			}
			if !tt.wantError && (len(got) != 4 || got[0][0] != 1 || got[1][1] != 1 || got[2][0] != 1 || got[3][1] != 1) {
				t.Fatalf("vectors mapped to wrong inputs: %v", got)
			}
		})
	}
}

func TestOpenAIEmbedderPreservesPartialUsage(t *testing.T) {
	for _, tt := range []struct {
		name       string
		failAt     int
		wantTokens int64
	}{
		{"first batch fails", 1, 0},
		{"second batch fails", 2, 7},
		{"third batch fails", 3, 14},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls++
				w.Header().Set("Content-Type", "application/json")
				if calls == tt.failAt {
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(`{"error":{"message":"bad input"}}`))
					return
				}
				_, _ = w.Write([]byte(`{"object":"list","model":"emb","data":[{"index":0,"embedding":[1,0]}],"usage":{"prompt_tokens":7,"total_tokens":7}}`))
			}))
			defer srv.Close()
			e := NewOpenAIEmbedder(srv.URL, "k", "emb", 2)
			e.MaxBatch = 1
			got, tokens, err := e.Embed(t.Context(), []string{"first", "second", "third"})
			if err == nil || got != nil || tokens != tt.wantTokens {
				t.Fatalf("Embed = %v, %d, %v; want error and %d tokens", got, tokens, err, tt.wantTokens)
			}
		})
	}
}
