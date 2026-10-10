package model

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// catalogModel is a model as the Models API lists it, with only the
// fields a test sets.
type catalogModel struct {
	ID           string         `json:"id"`
	Line         string         `json:"line,omitempty"`
	Lifecycle    string         `json:"lifecycle,omitempty"`
	CreatedAt    string         `json:"created_at,omitempty"`
	Capabilities map[string]any `json:"capabilities,omitempty"`
}

// effortLevels is a model's capabilities, taking effort at levels.
func effortLevels(levels ...string) map[string]any {
	effort := map[string]any{"supported": true}
	for _, l := range []string{"low", "medium", "high", "xhigh", "max"} {
		effort[l] = map[string]any{"supported": slices.Contains(levels, l)}
	}
	return map[string]any{"effort": effort}
}

func writeAnthropicCatalog(t *testing.T, w http.ResponseWriter, models []catalogModel, more bool) {
	t.Helper()
	var last string
	if len(models) > 0 {
		last = models[len(models)-1].ID
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{"data": models, "has_more": more, "last_id": last}); err != nil {
		t.Error(err)
	}
}

func writeAnthropicAnswer(t *testing.T, w http.ResponseWriter) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if _, err := w.Write([]byte(anthropicMessage(`[{"type":"text","text":"ok"}]`, "end_turn", anthropicUsage))); err != nil {
		t.Error(err)
	}
}

func TestAnthropicLatestModels(t *testing.T) {
	var reads atomic.Int32
	var chosen atomic.Value
	var catalogMu sync.Mutex
	// Newest first, as the API lists them, whatever their dates say.
	catalog := []catalogModel{
		{ID: "claude-opus-without-line", Lifecycle: "active"},
		{ID: "claude-opus-deprecated", Line: "opus", Lifecycle: "deprecated"},
		{ID: "claude-opus-retired", Line: "opus", Lifecycle: "retired"},
		{ID: "opus-without-high-effort", Line: "opus", Lifecycle: "active", Capabilities: effortLevels("low", "medium")},
		{ID: "a-new-family-id", Line: "newfamily", Lifecycle: "active"},
		{ID: "claude-haiku-5", Line: "haiku", Lifecycle: "active"},
		{ID: "opaque-opus-id", Line: "opus", Lifecycle: "active", CreatedAt: "1970-01-01T00:00:00Z", Capabilities: effortLevels("high")},
		{ID: "claude-sonnet-5", Line: "sonnet", Lifecycle: "active", CreatedAt: "2026-02-01T00:00:00Z"},
		{ID: "claude-opus-4-6", Line: "opus", Lifecycle: "active", CreatedAt: "2026-03-01T00:00:00Z"},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "test-key" {
			t.Error("request did not use the configured API key")
		}
		if r.URL.Path == "/gw/v1/models" {
			reads.Add(1)
			if strings.Contains(r.URL.RawQuery, "lifecycle") {
				t.Errorf("catalog request filters by lifecycle: %s", r.URL.RawQuery)
			}
			catalogMu.Lock()
			defer catalogMu.Unlock()
			switch r.URL.Query().Get("after_id") {
			case "":
				writeAnthropicCatalog(t, w, catalog[:2], true)
			case catalog[1].ID:
				writeAnthropicCatalog(t, w, catalog[2:], false)
			default:
				t.Errorf("unexpected model cursor: %s", r.URL.RawQuery)
				w.WriteHeader(http.StatusBadRequest)
			}
			return
		}
		if r.URL.Path != "/gw/v1/messages" {
			t.Errorf("unexpected request to %s", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		chosen.Store(field(body, "model"))
		if field(body, "output_config", "effort") != "high" || field(body, "max_tokens") != float64(321) {
			t.Errorf("request options = %v", body)
		}
		writeAnthropicAnswer(t, w)
	}))
	t.Cleanup(srv.Close)
	c := newTestAnthropic(t, srv, Pricing{"opaque-opus-id": {Input: 3}, "~opus-latest": {Input: 7}})
	now := c.now()
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
		{"~opus-latest", "opaque-opus-id", 0.0003},
		{"~sonnet-latest", "claude-sonnet-5", 0},
		{"~haiku-latest", "claude-haiku-5", 0},
		{"~newfamily-latest", "a-new-family-id", 0},
	} {
		t.Run(tt.alias, func(t *testing.T) { step(t, tt.alias, tt.want, tt.cost) })
	}
	step(t, "~missing-latest", "claude-sonnet-5", 0, "~sonnet-latest")
	if reads.Load() != 2 {
		t.Fatalf("catalog read %d times, want two pages shared across aliases", reads.Load())
	}
	catalogMu.Lock()
	catalog[6].ID = "brand-new-opus-id"
	catalogMu.Unlock()
	step(t, "~opus-latest", "opaque-opus-id", 0.0003)
	now = now.Add(modelCatalogTTL)
	step(t, "~opus-latest", "opaque-opus-id", 0.0003)
	settle(&c.catalogCache, "")
	step(t, "~opus-latest", "brand-new-opus-id", 0.0007)
	if reads.Load() != 4 {
		t.Fatalf("catalog read %d times, want a complete refresh after expiry", reads.Load())
	}
}

func TestAnthropicPin(t *testing.T) {
	var reads atomic.Int32
	var chosen atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/gw/v1/models" {
			reads.Add(1)
			writeAnthropicCatalog(t, w, []catalogModel{
				{ID: "opus-without-high-effort", Line: "opus", Lifecycle: "active", Capabilities: effortLevels("low")},
				{ID: "claude-opus-5", Line: "opus", Lifecycle: "active", Capabilities: effortLevels("low", "high")},
			}, false)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		chosen.Store(field(body, "model"))
		writeAnthropicAnswer(t, w)
	}))
	t.Cleanup(srv.Close)
	c := newTestAnthropic(t, srv, nil)
	for _, tt := range []struct {
		id     string
		effort Effort
		want   string
	}{
		{"~opus-latest", EffortHigh, "~opus-latest@claude-opus-5"},
		{"~opus-latest", "", "~opus-latest@opus-without-high-effort"},
		{"~opus-latest", EffortMinimal, "~opus-latest@opus-without-high-effort"},
		{"~opus-latest@claude-opus-4-6", EffortHigh, "~opus-latest@claude-opus-4-6"},
		{"claude-opus-4-6", EffortHigh, "claude-opus-4-6"},
	} {
		if got, err := c.Pin(t.Context(), tt.id, tt.effort); err != nil || got != tt.want {
			t.Errorf("Pin(%s, %q) = %q, %v; want %s", tt.id, tt.effort, got, err, tt.want)
		}
	}
	if reads.Load() != 1 {
		t.Fatalf("catalog read %d times, want once", reads.Load())
	}
	// Another replica steps the pinned alias without reading the catalog,
	// and prices the model under the alias.
	other := newTestAnthropic(t, srv, Pricing{"~opus-latest": {Input: 7}})
	resp, err := other.Step(t.Context(), StepRequest{Model: "~opus-latest@claude-opus-5"})
	if err != nil || chosen.Load() != "claude-opus-5" || resp.Model != "claude-opus-5" || math.Abs(resp.CostUSD-0.0007) > 1e-9 {
		t.Fatalf("pinned step = %+v, %v, model %v", resp, err, chosen.Load())
	}
	if reads.Load() != 1 {
		t.Fatalf("pinned step read the catalog")
	}
}

func TestAnthropicPinnedModelsSkipCatalog(t *testing.T) {
	for _, id := range []string{"claude-opus-4-6", "claude-3-5-sonnet-latest", "opus-latest", "local-model"} {
		t.Run(id, func(t *testing.T) {
			srv, got := fakeProvider(t, http.StatusOK, anthropicMessage(`[{"type":"text","text":"ok"}]`, "end_turn", anthropicUsage))
			resp, err := newTestAnthropic(t, srv, nil).Step(t.Context(), StepRequest{Model: id})
			if err != nil || got.path != "/gw/v1/messages" || field(got.body, "model") != id || resp.Model != id {
				t.Fatalf("explicit model = %+v, %v, request to %s: %v", resp, err, got.path, got.body)
			}
		})
	}
}

func TestAnthropicMissingFamily(t *testing.T) {
	for _, tt := range []struct {
		name, body string
		effort     Effort
	}{
		{"empty catalog", `{"data":[],"has_more":false}`, ""},
		{"no line despite model name", `{"data":[{"id":"claude-opus-9","lifecycle":"active"}],"has_more":false}`, ""},
		{"null line", `{"data":[{"id":"claude-opus-9","line":null,"lifecycle":"active"}],"has_more":false}`, ""},
		{"another line despite model name", `{"data":[{"id":"claude-opus-9","line":"sonnet","lifecycle":"active"}],"has_more":false}`, ""},
		{"deprecated line", `{"data":[{"id":"claude-opus-9","line":"opus","lifecycle":"deprecated"}],"has_more":false}`, ""},
		{"retired line", `{"data":[{"id":"claude-opus-9","line":"opus","lifecycle":"retired"}],"has_more":false}`, ""},
		{"missing lifecycle", `{"data":[{"id":"claude-opus-9","line":"opus"}],"has_more":false}`, ""},
		{"no effort", `{"data":[{"id":"claude-opus-9","line":"opus","lifecycle":"active","capabilities":{"effort":{"supported":false}}}],"has_more":false}`, EffortLow},
		{"no such effort", `{"data":[{"id":"claude-opus-9","line":"opus","lifecycle":"active","capabilities":{"effort":{"supported":true,"max":{"supported":false}}}}],"has_more":false}`, EffortMax},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv, got := fakeProvider(t, http.StatusOK, tt.body)
			_, err := newTestAnthropic(t, srv, nil).Step(t.Context(), StepRequest{Model: "~opus-latest", Effort: tt.effort})
			if err == nil || !strings.Contains(err.Error(), "no active opus model") || Transient(err) || got.path != "/gw/v1/models" {
				t.Fatalf("unavailable family = %v after request to %s", err, got.path)
			}
		})
	}
}

func TestAnthropicCatalogErrors(t *testing.T) {
	for _, tt := range []struct {
		name      string
		status    int
		body      string
		transient bool
	}{
		{"unauthorized", 401, `{"type":"error","error":{"type":"authentication_error","message":"bad key"}}`, false},
		{"unsupported gateway", 404, `{"type":"error","error":{"type":"not_found_error","message":"missing"}}`, false},
		{"rate limited", 429, `{"type":"error","error":{"type":"rate_limit_error","message":"limited"}}`, true},
		{"unavailable", 503, `{"type":"error","error":{"type":"overloaded_error","message":"unavailable"}}`, true},
		{"truncated catalog", 200, `{`, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var reads, messages atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/gw/v1/models" {
					reads.Add(1)
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(tt.status)
					_, _ = w.Write([]byte(tt.body))
					return
				}
				messages.Add(1)
				writeAnthropicAnswer(t, w)
			}))
			t.Cleanup(srv.Close)
			c := newTestAnthropic(t, srv, nil)
			_, err := c.Step(t.Context(), StepRequest{Model: "~opus-latest"})
			if err == nil || !strings.Contains(err.Error(), "model catalog") || Transient(err) != tt.transient || messages.Load() != 0 {
				t.Fatalf("catalog error = %v (transient %v), messages %d", err, Transient(err), messages.Load())
			}
			resp, err := c.Step(t.Context(), StepRequest{Model: "~opus-latest", Fallbacks: []string{"pinned-fallback"}})
			if err != nil || resp.Model != "pinned-fallback" || reads.Load() != 2 || messages.Load() != 1 {
				t.Fatalf("fallback = %+v, %v; catalog reads %d, messages %d", resp, err, reads.Load(), messages.Load())
			}
		})
	}
}

func TestAnthropicCatalogIgnoringCursor(t *testing.T) {
	var reads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		writeAnthropicCatalog(t, w, []catalogModel{{ID: "same-opus", Line: "opus", Lifecycle: "active"}}, true)
	}))
	t.Cleanup(srv.Close)
	_, err := newTestAnthropic(t, srv, nil).Step(t.Context(), StepRequest{Model: "~opus-latest"})
	if err == nil || !strings.Contains(err.Error(), "lists same-opus twice") || Transient(err) || reads.Load() != 2 {
		t.Fatalf("catalog that repeats its page = %v after %d reads", err, reads.Load())
	}
}

func TestAnthropicIncompleteCatalog(t *testing.T) {
	var reads atomic.Int32
	var fail atomic.Bool
	fail.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/gw/v1/models" {
			writeAnthropicAnswer(t, w)
			return
		}
		reads.Add(1)
		if r.URL.Query().Get("after_id") == "" {
			writeAnthropicCatalog(t, w, []catalogModel{{ID: "claude-sonnet-5", Line: "sonnet", Lifecycle: "active"}}, true)
			return
		}
		if fail.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"overloaded_error","message":"unavailable"}}`))
			return
		}
		writeAnthropicCatalog(t, w, []catalogModel{{ID: "listed-opus", Line: "opus", Lifecycle: "active"}}, false)
	}))
	t.Cleanup(srv.Close)
	c := newTestAnthropic(t, srv, nil)
	if resp, err := c.Step(t.Context(), StepRequest{Model: "~opus-latest"}); err == nil || !Transient(err) {
		t.Fatalf("incomplete catalog = %+v, %v", resp, err)
	}
	fail.Store(false)
	resp, err := c.Step(t.Context(), StepRequest{Model: "~opus-latest"})
	if err != nil || resp.Model != "listed-opus" || reads.Load() != 4 {
		t.Fatalf("catalog after recovery = %+v, %v; reads %d", resp, err, reads.Load())
	}
}

// TestAnthropicCatalogOutlivesCanceledStep: the fetch a step stops waiting
// for still fills the catalog the next step uses.
func TestAnthropicCatalogOutlivesCanceledStep(t *testing.T) {
	var reads atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	signalStart := sync.OnceFunc(func() { close(started) })
	releaseFetch := sync.OnceFunc(func() { close(release) })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/gw/v1/models" {
			reads.Add(1)
			signalStart()
			<-release
			writeAnthropicCatalog(t, w, []catalogModel{{ID: "current-opus", Line: "opus", Lifecycle: "active"}}, false)
			return
		}
		writeAnthropicAnswer(t, w)
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(releaseFetch)
	c := newTestAnthropic(t, srv, nil)
	impatient, cancel := context.WithCancel(t.Context())
	defer cancel()
	first := make(chan error, 1)
	go func() {
		_, err := c.Step(impatient, StepRequest{Model: "~opus-latest"})
		first <- err
	}()
	<-started
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled step = %v", err)
	}
	releaseFetch()
	resp, err := c.Step(t.Context(), StepRequest{Model: "~opus-latest"})
	if err != nil || resp.Model != "current-opus" || reads.Load() != 1 {
		t.Fatalf("next step = %+v, %v after %d catalog reads", resp, err, reads.Load())
	}
}

func TestAnthropicCatalogPerProvider(t *testing.T) {
	var reads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			reads.Add(1)
			writeAnthropicCatalog(t, w, []catalogModel{{ID: r.Header.Get("X-Api-Key") + "-model", Line: "opus", Lifecycle: "active"}}, false)
			return
		}
		writeAnthropicAnswer(t, w)
	}))
	t.Cleanup(srv.Close)
	for _, key := range []string{"key-one", "key-two"} {
		t.Run(key, func(t *testing.T) {
			c, err := NewAnthropic(AnthropicConfig{BaseURL: srv.URL, APIKey: key})
			if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				resp, err := c.Step(t.Context(), StepRequest{Model: "~opus-latest"})
				if err != nil || resp.Model != key+"-model" {
					t.Fatalf("%s resolved %+v, %v", key, resp, err)
				}
			}
		})
	}
	if reads.Load() != 2 {
		t.Fatalf("catalog read %d times, want one per provider", reads.Load())
	}
}
