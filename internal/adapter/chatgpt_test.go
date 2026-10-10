package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/home-operations/kritika/internal/chatgpt"
	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/store"
)

type planSessions struct {
	session store.ChatGPTSession
	found   bool
	err     error
	key     string
	expired string
}

func TestChatGPTProviderFallback(t *testing.T) {
	for _, tt := range []struct {
		name, body string
		status     int
		requests   int32
	}{
		{"HTTP plan limit", `{"error":{"code":"subscription_sharing_usage_limit_exceeded","message":"spent"}}`, 429, 1},
		{"stream plan limit", "event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"subscription_sharing_usage_limit_exceeded\",\"message\":\"spent\"}}}\n\n", 200, 1},
		{"provider unavailable", `{"error":{"message":"temporarily unavailable"}}`, 503, 4},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var planCalls, apiCalls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/responses" {
					planCalls.Add(1)
					w.Header().Set("Content-Type", "application/json")
					if tt.status == 200 {
						w.Header().Set("Content-Type", "text/event-stream")
					}
					w.WriteHeader(tt.status)
					_, _ = w.Write([]byte(tt.body))
					return
				}
				apiCalls.Add(1)
				var body struct {
					Model string `json:"model"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Model != "fallback" ||
					r.Header.Get("Authorization") != "Bearer sk-test" || r.URL.Path != "/v1/chat/completions" {
					t.Errorf("fallback request = %+v, %v, %s", body, err, r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"call-1","model":"fallback","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"cost":0.25}}`))
			}))
			t.Cleanup(srv.Close)
			sessions := &planSessions{found: true, session: store.ChatGPTSession{Credentials: chatgpt.Credentials{AccessToken: "at-1"}}}
			primary, err := model.NewChatGPT(model.ChatGPTConfig{BaseURL: srv.URL + "/v1", Tokens: planTokens{sessions: sessions, key: "plan"}})
			if err != nil {
				t.Fatal(err)
			}
			fallback, err := model.NewOpenAI(model.OpenAIConfig{BaseURL: srv.URL + "/v1", APIKey: "sk-test", OpenRouter: true})
			if err != nil {
				t.Fatal(err)
			}
			route := Route{Ref: "plan/reviewer", Stepper: primary, Provider: configfile.Provider{Retries: 1}}
			call := Call{
				Route:    route,
				Fallback: &Route{Ref: "api/fallback", Stepper: fallback},
			}
			for range 2 {
				resp, _, served, err := call.Do(t.Context(), model.StepRequest{Messages: []model.Message{{Role: model.RoleUser, Text: "review"}}})
				if err != nil || served.Ref != "api/fallback" || resp.Text != "ok" || resp.ChatGPTPlan || resp.CostUSD != 0.25 {
					t.Fatalf("fallback = %+v on %s, %v", resp, served.Ref, err)
				}
			}
			if planCalls.Load() != tt.requests || apiCalls.Load() != 2 {
				t.Fatalf("plan requests = %d, API requests = %d; want %d and 2", planCalls.Load(), apiCalls.Load(), tt.requests)
			}
			if tt.status != 503 {
				call.Fallback = nil
				if _, _, _, err := call.Do(t.Context(), model.StepRequest{}); !errors.Is(err, model.ErrPlanLimit) {
					t.Fatalf("no fallback = %v", err)
				}
			}
		})
	}
}

func (s *planSessions) ChatGPTSession(_ context.Context, key string) (store.ChatGPTSession, bool, error) {
	s.key = key
	return s.session, s.found, s.err
}

func (s *planSessions) ExpireChatGPTSession(_ context.Context, key, token string) error {
	s.key, s.expired = key, token
	return s.err
}

func (s *planSessions) UpdateChatGPTAllowances(_ context.Context, key, token string, _ []chatgpt.Allowance) error {
	s.key = key
	return s.err
}

func TestPlanTokens(t *testing.T) {
	sessions := &planSessions{found: true, session: store.ChatGPTSession{Credentials: chatgpt.Credentials{AccessToken: "at-1"}}}
	src := planTokens{sessions: sessions, key: "github/acme/plan"}
	for _, token := range []string{"at-1", "at-2"} {
		sessions.session.Credentials.AccessToken = token
		got, err := src.Token(t.Context())
		if err != nil || got != token || sessions.key != src.key {
			t.Fatalf("Token = %q, %v for %q", got, err, sessions.key)
		}
	}
	if err := src.Expire(t.Context(), "at-1"); err != nil || sessions.expired != "at-1" || sessions.key != src.key {
		t.Fatalf("Expire = %v, token %q for %q", err, sessions.expired, sessions.key)
	}
	for _, tt := range []struct {
		name string
		set  func()
	}{
		{"missing", func() { sessions.found = false }},
		{"signed out", func() { sessions.found = true; sessions.session.SignedOutAt = time.Now() }},
		{"pending", func() { sessions.session.SignedOutAt = time.Time{}; sessions.session.Credentials.AccessToken = "" }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tt.set()
			if got, err := src.Token(t.Context()); got != "" || !errors.Is(err, chatgpt.ErrSignedOut) || model.Transient(err) {
				t.Fatalf("Token = %q, %v, want a disconnected provider", got, err)
			}
		})
	}
	sessions.err = errors.New("database unavailable")
	if _, err := src.Token(t.Context()); !errors.Is(err, sessions.err) {
		t.Fatalf("database error = %v", err)
	}
}

func TestChatGPTStepperSessionKeys(t *testing.T) {
	f := &configfile.File{
		Providers: map[string]configfile.Provider{"plan": {Type: configfile.ProviderChatGPT}},
	}
	a := &configfile.Account{Forge: configfile.ForgeGitHub, Name: "acme",
		Providers: map[string]configfile.Provider{"own": {Type: configfile.ProviderChatGPT}}}
	sessions := &planSessions{}
	steppers := &Steppers{Build: StepperBuilder(sessions)}
	for _, tt := range []struct{ provider, key string }{{"plan", "plan"}, {"own", "github/acme/own"}} {
		t.Run(tt.provider, func(t *testing.T) {
			s, err := steppers.Stepper(f, a, tt.provider)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Step(t.Context(), model.StepRequest{Model: "m"}); !errors.Is(err, chatgpt.ErrSignedOut) || sessions.key != tt.key {
				t.Fatalf("Step = %v, key %q, want %q", err, sessions.key, tt.key)
			}
		})
	}
}
