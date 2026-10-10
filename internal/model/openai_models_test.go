package model

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestOpenAILatestModels(t *testing.T) {
	var reads atomic.Int32
	var chosen atomic.Value
	var catalogMu sync.Mutex
	catalog := []string{
		"gpt-5.6-sol", "sol-6.9", "gpt-6.10-sol", "gpt-9-sol-preview", "sol-10-20261001",
		"gpt-6-astra", "astra-6.1", "newfamily-1.1",
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Error("request did not use the configured API key")
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/gw/v1/models" {
			reads.Add(1)
			catalogMu.Lock()
			defer catalogMu.Unlock()
			models := make([]map[string]any, 0, len(catalog))
			for i, id := range catalog {
				models = append(models, map[string]any{"id": id, "object": "model", "created": len(catalog) - i})
			}
			if err := json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": models}); err != nil {
				t.Error(err)
			}
			return
		}
		if r.URL.Path != "/gw/v1/chat/completions" {
			t.Errorf("unexpected request to %s", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		chosen.Store(field(body, "model"))
		if field(body, "reasoning_effort") != "high" || field(body, "max_completion_tokens") != float64(321) {
			t.Errorf("request options = %v", body)
		}
		if _, err := w.Write([]byte(chatCompletion(`{"role":"assistant","content":"ok"}`, "stop", plainUsage, ""))); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(srv.Close)
	c := newTestOpenAI(t, srv, false, Pricing{"gpt-6.10-sol": {Input: 3}, "~sol-latest": {Input: 7}})
	now := time.Now()
	c.now = func() time.Time { return now }
	step := func(t *testing.T, alias, want string, cost float64, fallbacks ...string) {
		t.Helper()
		resp, err := c.Step(t.Context(), StepRequest{Model: alias, Fallbacks: fallbacks, Effort: EffortHigh, MaxTokens: 321})
		if err != nil || chosen.Load() != want || resp.Model != want || math.Abs(resp.CostUSD-cost) > 1e-9 {
			t.Fatalf("%s = %v, %+v, %v; want %s with cost %v", alias, chosen.Load(), resp, err, want, cost)
		}
	}
	for _, tt := range []struct {
		alias, want string
		cost        float64
	}{
		{"~sol-latest", "gpt-6.10-sol", 0.00036},
		{"~astra-latest", "astra-6.1", 0},
		{"~newfamily-latest", "newfamily-1.1", 0},
	} {
		t.Run(tt.alias, func(t *testing.T) { step(t, tt.alias, tt.want, tt.cost) })
	}
	step(t, "~missing-latest", "astra-6.1", 0, "~astra-latest")
	if reads.Load() != 1 {
		t.Fatalf("catalog read %d times, want one shared across aliases", reads.Load())
	}
	catalogMu.Lock()
	catalog[3] = "sol-7"
	catalogMu.Unlock()
	step(t, "~sol-latest", "gpt-6.10-sol", 0.00036)
	now = now.Add(modelCatalogTTL)
	step(t, "~sol-latest", "gpt-6.10-sol", 0.00036)
	settle(&c.catalogCache, "")
	step(t, "~sol-latest", "sol-7", 0.00084)
	if reads.Load() != 2 {
		t.Fatalf("catalog read %d times, want a refresh after expiry", reads.Load())
	}
}

func TestOpenAIPin(t *testing.T) {
	var reads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"gpt-6-sol"},{"id":"gpt-6.1-sol"}]}`))
	}))
	t.Cleanup(srv.Close)
	for _, tt := range []struct {
		name, id, want       string
		openRouter, openCode bool
	}{
		{"alias", "~sol-latest", "~sol-latest@gpt-6.1-sol", false, false},
		{"pinned", "gpt-6-sol", "gpt-6-sol", false, false},
		{"openrouter", "~sol-latest", "~sol-latest", true, false},
		{"opencode", "~sol-latest", "~sol-latest", false, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestOpenAI(t, srv, tt.openRouter, nil)
			c.openCode = tt.openCode
			if got, err := c.Pin(t.Context(), tt.id, EffortHigh); err != nil || got != tt.want {
				t.Fatalf("Pin = %q, %v; want %s", got, err, tt.want)
			}
		})
	}
	if reads.Load() != 1 {
		t.Fatalf("catalog read %d times, want once, for the alias", reads.Load())
	}
}

func TestOpenAIModelIDsPassThrough(t *testing.T) {
	for _, tt := range []struct {
		name, id             string
		openRouter, openCode bool
	}{
		{"pinned", "gpt-5.6-sol", false, false},
		{"native alias", "chatgpt-sol-latest", false, false},
		{"unprefixed family", "sol-latest", false, false},
		{"local", "local-model", false, false},
		{"openrouter", "~sol-latest", true, false},
		{"opencode", "~sol-latest", false, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv, got := fakeProvider(t, http.StatusOK, chatCompletion(`{"role":"assistant","content":"ok"}`, "stop", plainUsage, ""))
			c := newTestOpenAI(t, srv, tt.openRouter, nil)
			c.openCode = tt.openCode
			_, err := c.Step(t.Context(), StepRequest{Model: tt.id})
			if err != nil || got.path != "/gw/v1/chat/completions" || field(got.body, "model") != tt.id {
				t.Fatalf("explicit model = %v, path %s, body %v", err, got.path, got.body)
			}
		})
	}
}

func TestOpenAICatalogErrors(t *testing.T) {
	for _, tt := range []struct {
		name      string
		status    int
		body      string
		transient bool
	}{
		{"unauthorized", 401, `{"error":{"message":"bad key"}}`, false},
		{"unsupported gateway", 404, `{"error":{"message":"missing"}}`, false},
		{"rate limited", 429, `{"error":{"message":"limited"}}`, true},
		{"unavailable", 503, `{"error":{"message":"unavailable"}}`, true},
		{"truncated catalog", 200, `{`, true},
		{"missing family", 200, `{"object":"list","data":[{"id":"gpt-6-astra"}]}`, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var reads, messages atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/gw/v1/models" {
					reads.Add(1)
					w.WriteHeader(tt.status)
					_, _ = w.Write([]byte(tt.body))
					return
				}
				messages.Add(1)
				_, _ = w.Write([]byte(chatCompletion(`{"role":"assistant","content":"ok"}`, "stop", plainUsage, "")))
			}))
			t.Cleanup(srv.Close)
			c := newTestOpenAI(t, srv, false, nil)
			_, err := c.Step(t.Context(), StepRequest{Model: "~sol-latest"})
			if err == nil || Transient(err) != tt.transient || messages.Load() != 0 || !strings.Contains(err.Error(), "model: openai:") {
				t.Fatalf("catalog error = %v (transient %v), messages %d", err, Transient(err), messages.Load())
			}
			wantReads := int32(2)
			if tt.name == "missing family" {
				wantReads = 1
			}
			resp, err := c.Step(t.Context(), StepRequest{Model: "~sol-latest", Fallbacks: []string{"pinned-fallback"}})
			if err != nil || resp.Model != "pinned-fallback" || reads.Load() != wantReads || messages.Load() != 1 {
				t.Fatalf("fallback = %+v, %v; catalog reads %d, messages %d", resp, err, reads.Load(), messages.Load())
			}
		})
	}
}
