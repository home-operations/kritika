package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openai/openai-go/v3"

	"github.com/home-operations/kritika/internal/configfile/configfiletest"
	"github.com/home-operations/kritika/internal/model"
)

func TestUpstreamStatus(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"an overloaded provider", &openai.Error{StatusCode: http.StatusServiceUnavailable}, http.StatusBadGateway},
		{"a rate limit", &openai.Error{StatusCode: http.StatusTooManyRequests}, http.StatusBadGateway},
		{"a timeout", context.DeadlineExceeded, http.StatusBadGateway},
		{"a refused request", &openai.Error{StatusCode: http.StatusBadRequest}, http.StatusUnprocessableEntity},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, _ := upstreamStatus(tt.err)
			// The runner's loop classifies the gateway's answer the same way.
			if status != tt.want || model.Transient(&openai.Error{StatusCode: status}) != model.Transient(tt.err) {
				t.Fatalf("status = %d, want %d, as transient as the provider's error", status, tt.want)
			}
		})
	}
}

func TestRefuseRetries(t *testing.T) {
	for status, retry := range map[int]bool{
		http.StatusInternalServerError: true,
		http.StatusBadGateway:          false,
		http.StatusTooManyRequests:     false,
		http.StatusUnauthorized:        false,
		http.StatusBadRequest:          false,
	} {
		rec := httptest.NewRecorder()
		refuse(rec, status, "code", "message")
		if got := rec.Header().Get("X-Should-Retry") != "false"; got != retry || rec.Code != status {
			t.Fatalf("%d: retry = %v, want %v", status, got, retry)
		}
	}
}

func TestMaskProvider(t *testing.T) {
	t.Setenv("TEST_PROVIDER_KEY", "sk-provider")
	f, err := configfiletest.Parse(t, `providers:
  p:
    type: openai
    baseUrl: https://kritika:url-secret@llm.example/v1
    apiKey: { env: TEST_PROVIDER_KEY }
apps:
  acme-bot:
    accounts: [acme]
    clientId: Iv1.test
    privateKey: { env: TEST_PROVIDER_KEY }
    webhookSecret: { env: TEST_PROVIDER_KEY }
`)
	if err != nil {
		t.Fatal(err)
	}
	got := maskProvider(`POST "https://kritika:url-secret@llm.example/v1/chat/completions": 401 {"error":"bad key sk-provider"}`, f.Providers["p"])
	want := `POST "https://***@llm.example/v1/chat/completions": 401 {"error":"bad key ***"}`
	if got != want {
		t.Fatalf("masked = %s\nwant     %s", got, want)
	}
}

func TestStepPart(t *testing.T) {
	for _, tt := range []struct {
		header string
		want   int
		err    bool
	}{
		{header: "", want: 0},
		{header: "1", want: 1},
		{header: "12", want: 12},
		{header: "0", err: true},
		{header: "-2", err: true},
		{header: "two", err: true},
		{header: "99999999999", err: true},
	} {
		got, err := stepPart(tt.header)
		if got != tt.want || (err != nil) != tt.err {
			t.Fatalf("stepPart(%q) = %d, %v; want %d, error %v", tt.header, got, err, tt.want, tt.err)
		}
	}
}
