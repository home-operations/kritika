package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestChatGPTLatestModels(t *testing.T) {
	var reads atomic.Int32
	var chosen atomic.Value
	var catalogMu sync.Mutex
	catalog := []chatGPTCatalogModel{
		{Slug: "gpt-5.6-sol", Visibility: "list"}, {Slug: "gpt-6-sol", Visibility: "list"},
		{Slug: "gpt-6.9-sol", Visibility: "list"}, {Slug: "gpt-6.10-sol", Visibility: "list"},
		{Slug: "gpt-7-sol", Visibility: "hide"}, {Slug: "gpt-8-sol-preview", Visibility: "list"},
		{Slug: "gpt-6-astra", Visibility: "list"}, {Slug: "gpt-6.1-astra", Visibility: "list"},
		{Slug: "newfamily-1.1", Visibility: "list"},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer at-1" && r.Header.Get("Authorization") != "Bearer at-2" {
			t.Error("model catalog request did not use the plan token")
		}
		if r.URL.Path == "/v1/models" {
			catalogMu.Lock()
			defer catalogMu.Unlock()
			reads.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"models": catalog})
			return
		}
		if r.URL.Path != "/v1/responses" {
			t.Errorf("unexpected request to %s", r.URL.Path)
		}
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		chosen.Store(body.Model)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(completed(textOutput, responseUsage)))
	}))
	t.Cleanup(srv.Close)
	src := &tokens{token: "at-1"}
	c := newTestChatGPT(t, srv, src)
	now := time.Now()
	c.now = func() time.Time { return now }
	step := func(alias, want string) {
		t.Helper()
		resp, err := c.Step(t.Context(), StepRequest{Model: alias})
		if err != nil || chosen.Load() != want || resp.Model != want {
			t.Fatalf("%s = %s, %+v, %v; want %s", alias, chosen.Load(), resp, err, want)
		}
	}
	step("~sol-latest", "gpt-6.10-sol")
	step("~astra-latest", "gpt-6.1-astra")
	step("~newfamily-latest", "newfamily-1.1")
	step("gpt-5.6-sol", "gpt-5.6-sol")
	if reads.Load() != 1 {
		t.Fatalf("catalog read %d times, want one shared across aliases", reads.Load())
	}
	catalogMu.Lock()
	catalog[4].Visibility = "list"
	catalogMu.Unlock()
	step("~sol-latest", "gpt-6.10-sol")
	now = now.Add(modelCatalogTTL)
	step("~sol-latest", "gpt-6.10-sol")
	settle(&c.catalogCache, "at-1")
	step("~sol-latest", "gpt-7-sol")
	if reads.Load() != 2 {
		t.Fatal("new models were not discovered after cache expiry")
	}
	catalogMu.Lock()
	catalog = []chatGPTCatalogModel{{Slug: "gpt-5.6-sol", Visibility: "list"}}
	catalogMu.Unlock()
	src.token = "at-2"
	step("~sol-latest", "gpt-5.6-sol")
	if reads.Load() != 3 {
		t.Fatal("a new token reused the earlier account's catalog")
	}
	if _, err := c.Step(t.Context(), StepRequest{Model: "~astra-latest"}); err == nil || !strings.Contains(err.Error(), "no versioned astra model") {
		t.Fatalf("unavailable family = %v", err)
	}
}

// TestChatGPTCatalogSharedFetch: alias steps that find the catalog stale
// share one fetch, and one that gives up waiting leaves it running for the
// others.
func TestChatGPTCatalogSharedFetch(t *testing.T) {
	var reads atomic.Int32
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			reads.Add(1)
			<-release
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"models":[{"slug":"gpt-6-sol","visibility":"list"}]}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(completed(textOutput, responseUsage)))
	}))
	t.Cleanup(srv.Close)
	c := newTestChatGPT(t, srv, nil)
	impatient, cancel := context.WithCancel(t.Context())
	errs := make(chan error, 3)
	go func() {
		_, err := c.Step(impatient, StepRequest{Model: "~sol-latest"})
		errs <- err
	}()
	for reads.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	for range 2 {
		go func() {
			resp, err := c.Step(t.Context(), StepRequest{Model: "~sol-latest"})
			if err == nil && resp.Model != "gpt-6-sol" {
				err = fmt.Errorf("resolved %s", resp.Model)
			}
			errs <- err
		}()
	}
	cancel()
	if err := <-errs; !errors.Is(err, context.Canceled) {
		t.Fatalf("the step that gave up = %v, want canceled", err)
	}
	close(release)
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if reads.Load() != 1 {
		t.Fatalf("catalog read %d times, want one shared fetch", reads.Load())
	}
}

func TestChatGPTPinnedModelSkipsCatalog(t *testing.T) {
	for _, id := range []string{"gpt-5.6-sol", "sol-latest", "astra-latest"} {
		t.Run(id, func(t *testing.T) {
			srv, got := fakeProvider(t, http.StatusOK, completed(textOutput, responseUsage))
			resp, err := newTestChatGPT(t, srv, nil).Step(t.Context(), StepRequest{Model: id})
			if err != nil || got.path != "/v1/responses" || field(got.body, "model") != id || resp.Model != id {
				t.Fatalf("explicit model = %+v, %v, path %s, body %v", resp, err, got.path, got.body)
			}
		})
	}
}

func TestChatGPTCatalogAuthenticationFailure(t *testing.T) {
	srv, _ := fakeProvider(t, http.StatusUnauthorized, `{"error":{"message":"expired","type":"invalid_request_error"}}`)
	src := &tokens{token: "at-1"}
	if _, err := newTestChatGPT(t, srv, src).Step(t.Context(), StepRequest{Model: "~sol-latest"}); err == nil || src.expired.Load() != 1 {
		t.Fatalf("catalog refusal = %v, expired %d", err, src.expired.Load())
	}
}

func TestChatGPTCatalogPlanLimit(t *testing.T) {
	srv, got := fakeProvider(t, http.StatusTooManyRequests, planLimitBody)
	c := newTestChatGPT(t, srv, nil)
	_, err := c.Step(t.Context(), StepRequest{Model: "~sol-latest"})
	if !errors.Is(err, ErrPlanLimit) || strings.Count(err.Error(), "model: chatgpt:") != 1 || got.path != "/v1/models" {
		t.Fatalf("catalog at the plan's limit = %v", err)
	}
	got.path = ""
	if _, err := c.Pin(t.Context(), "~sol-latest", ""); !errors.Is(err, ErrPlanLimit) || got.path != "" {
		t.Fatalf("pin on a paused plan = %v, request to %q", err, got.path)
	}
}

func TestChatGPTPin(t *testing.T) {
	var reads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		if r.Header.Get("Authorization") != "Bearer at-1" {
			t.Error("model catalog request did not use the plan token")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[{"slug":"gpt-6-sol","visibility":"list"},{"slug":"gpt-6.1-sol","visibility":"list"}]}`))
	}))
	t.Cleanup(srv.Close)
	src := &tokens{token: "at-1"}
	c := newTestChatGPT(t, srv, src)
	if got, err := c.Pin(t.Context(), "~sol-latest", EffortHigh); err != nil || got != "~sol-latest@gpt-6.1-sol" {
		t.Fatalf("Pin = %q, %v", got, err)
	}
	src.err = errors.New("signed out")
	if got, err := c.Pin(t.Context(), "gpt-6-sol", EffortHigh); err != nil || got != "gpt-6-sol" || reads.Load() != 1 {
		t.Fatalf("Pin of a model ID = %q, %v after %d catalog reads", got, err, reads.Load())
	}
}
