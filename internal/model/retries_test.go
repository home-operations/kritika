package model

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/openai/openai-go/v3"
)

// failing answers every request with status, with no x-should-retry
// header, and counts them.
func failing(t *testing.T, status int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"error":{"message":"down","type":"server_error"}}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &n
}

// TestAdaptersSendOnce: an adapter built for a provider sends a request the
// server failed once, since the gateway owns retrying; one built with
// Retries sends it that many times more.
func TestAdaptersSendOnce(t *testing.T) {
	req := StepRequest{Model: "m", Messages: []Message{{Role: RoleUser, Text: "hi"}}}
	t.Run("openai", func(t *testing.T) {
		srv, n := failing(t, http.StatusServiceUnavailable)
		s, err := NewStepper(ProviderOpenAI, srv.URL+"/v1", "k", nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Step(t.Context(), req); err == nil {
			t.Fatal("Step answered a 503")
		}
		if n.Load() != 1 {
			t.Fatalf("the provider got %d requests, want 1", n.Load())
		}
		c, err := NewOpenAI(OpenAIConfig{BaseURL: srv.URL + "/v1", APIKey: "k", Retries: 2})
		if err != nil {
			t.Fatal(err)
		}
		n.Store(0)
		_, _ = c.Step(t.Context(), req)
		if n.Load() != 3 {
			t.Fatalf("the provider got %d requests, want 3 with two retries", n.Load())
		}
	})
	t.Run("anthropic", func(t *testing.T) {
		srv, n := failing(t, http.StatusServiceUnavailable)
		s, err := NewStepper(ProviderAnthropic, srv.URL, "k", nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Step(t.Context(), req); err == nil {
			t.Fatal("Step answered a 503")
		}
		if n.Load() != 1 {
			t.Fatalf("the provider got %d requests, want 1", n.Load())
		}
	})
}

func TestRetryAfter(t *testing.T) {
	with := func(name, v string) *http.Response { return &http.Response{Header: http.Header{name: {v}}} }
	tests := []struct {
		name string
		err  error
		want time.Duration
	}{
		{"nil", nil, 0},
		{"no response", &openai.Error{StatusCode: 429}, 0},
		{"no header", &openai.Error{StatusCode: 429, Response: &http.Response{Header: http.Header{}}}, 0},
		{"seconds from openai", &openai.Error{StatusCode: 429, Response: with("Retry-After", "45")}, 45 * time.Second},
		{"seconds from anthropic", &anthropic.Error{StatusCode: 529, Response: with("Retry-After", "7")}, 7 * time.Second},
		{"a fraction of seconds", &openai.Error{StatusCode: 429, Response: with("Retry-After", "2.5")}, 2500 * time.Millisecond},
		{"milliseconds", &anthropic.Error{StatusCode: 429, Response: with("Retry-After-Ms", "1500")}, 1500 * time.Millisecond},
		{"a date past", &openai.Error{StatusCode: 429, Response: with("Retry-After", "Wed, 21 Oct 2015 07:28:00 GMT")}, 0},
		{"zero asks nothing", &openai.Error{StatusCode: 429, Response: with("Retry-After", "0")}, 0},
		{"a negative wait", &openai.Error{StatusCode: 429, Response: with("Retry-After", "-3")}, 0},
		{"NaN", &openai.Error{StatusCode: 429, Response: with("Retry-After", "NaN")}, 0},
		{"infinity", &openai.Error{StatusCode: 429, Response: with("Retry-After-Ms", "Inf")}, 0},
		{"a wait past any use", &openai.Error{StatusCode: 429, Response: with("Retry-After", "1e30")}, retryWaitCeiling},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RetryAfter(tt.err); got != tt.want {
				t.Fatalf("RetryAfter = %s, want %s", got, tt.want)
			}
		})
	}
	t.Run("a date ahead", func(t *testing.T) {
		at := time.Now().Add(30 * time.Second).UTC().Format(http.TimeFormat)
		if got := RetryAfter(&openai.Error{StatusCode: 429, Response: with("Retry-After", at)}); got <= 28*time.Second || got > 30*time.Second {
			t.Fatalf("RetryAfter = %s, want about 30s", got)
		}
	})
}

// TestShouldRetry: the provider's x-should-retry, when it sends one.
func TestShouldRetry(t *testing.T) {
	with := func(v string) error {
		return &openai.Error{StatusCode: http.StatusServiceUnavailable, Response: &http.Response{Header: http.Header{"X-Should-Retry": {v}}}}
	}
	for _, tt := range []struct {
		name       string
		err        error
		retry, set bool
	}{
		{"true", with("true"), true, true},
		{"false", with("false"), false, true},
		{"anything else", with("maybe"), false, false},
		{"no response", &openai.Error{StatusCode: 503}, false, false},
		{"not a provider's", errors.New("boom"), false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if retry, set := ShouldRetry(tt.err); retry != tt.retry || set != tt.set {
				t.Fatalf("ShouldRetry = %v, %v; want %v, %v", retry, set, tt.retry, tt.set)
			}
		})
	}
}
