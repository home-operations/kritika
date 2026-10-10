package model

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/home-operations/kritika/internal/chatgpt"
)

// tokens is a TokenSource of one fixed token that counts Expire calls.
type tokens struct {
	token   string
	err     error
	expired atomic.Int32
}

func (s *tokens) Token(context.Context) (string, error) { return s.token, s.err }
func (s *tokens) Expire(context.Context, string) error  { s.expired.Add(1); return nil }

// sse is a stream of the events given, each a JSON object with its type.
func sse(events ...string) string {
	var b strings.Builder
	for _, e := range events {
		var typed struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal([]byte(e), &typed)
		b.WriteString("event: " + typed.Type + "\ndata: " + e + "\n\n")
	}
	return b.String()
}

func responseEvent(kind, response string) string {
	return `{"type":"` + kind + `","sequence_number":1,"response":` + response + `}`
}

func completed(output, usage string) string {
	return sse(`{"type":"response.created","sequence_number":0,"response":{"id":"resp_1","object":"response","status":"in_progress"}}`,
		responseEvent("response.completed", `{"id":"resp_1","object":"response","status":"completed","model":"gpt-x","output":`+output+`,"usage":`+usage+`}`))
}

const (
	textOutput    = `[{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"hello","annotations":[]}]}]`
	responseUsage = `{"input_tokens":700,"input_tokens_details":{"cached_tokens":500},"output_tokens":59,"output_tokens_details":{"reasoning_tokens":40},"total_tokens":759}`
)

func newTestChatGPT(t *testing.T, srv *httptest.Server, src TokenSource) *ChatGPT {
	t.Helper()
	if src == nil {
		src = &tokens{token: "at-1"}
	}
	c, err := NewChatGPT(ChatGPTConfig{BaseURL: srv.URL + "/v1", Tokens: src})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestChatGPTStepResponses(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantText  string
		wantCalls []ToolCall
		wantStop  StopReason
		wantUsage Usage
		wantCost  float64
	}{
		{
			name:      "text, with the cached prompt split out",
			body:      completed(textOutput, responseUsage),
			wantText:  "hello",
			wantStop:  StopEndTurn,
			wantUsage: Usage{Input: 200, CacheRead: 500, Output: 59},
		},
		{
			name: "function calls become tool calls by call id",
			body: completed(`[{"type":"reasoning","id":"rs_1","summary":[]},`+
				`{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read_file","namespace":"kritika","arguments":"{\"path\":\"main.go\"}","status":"completed"},`+
				`{"type":"function_call","id":"fc_2","call_id":"call_2","name":"list","namespace":"kritika","arguments":"","status":"completed"}]`, responseUsage),
			wantCalls: []ToolCall{
				{ID: "call_1", Name: "read_file", Input: json.RawMessage(`{"path":"main.go"}`)},
				{ID: "call_2", Name: "list", Input: json.RawMessage(`{}`)},
			},
			wantStop:  StopToolUse,
			wantUsage: Usage{Input: 200, CacheRead: 500, Output: 59},
		},
		{
			name:      "a refusal is the answer's text",
			body:      completed(`[{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"refusal","refusal":"no"}]}]`, responseUsage),
			wantText:  "no",
			wantStop:  StopEndTurn,
			wantUsage: Usage{Input: 200, CacheRead: 500, Output: 59},
		},
		{
			name: "an answer the model's own cap cut off returns what it has",
			body: sse(responseEvent("response.incomplete", `{"id":"resp_1","object":"response","status":"incomplete","model":"gpt-x",`+
				`"incomplete_details":{"reason":"max_output_tokens"},"output":`+textOutput+`,"usage":`+responseUsage+`}`)),
			wantText:  "hello",
			wantStop:  StopMaxTokens,
			wantUsage: Usage{Input: 200, CacheRead: 500, Output: 59},
		},
		{
			name: "a tool call the cap cut off is still the cap's",
			body: sse(responseEvent("response.incomplete", `{"id":"resp_1","object":"response","status":"incomplete","model":"gpt-x",`+
				`"incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"function_call","call_id":"call_1","name":"submit","arguments":"{\"find","status":"incomplete"}],"usage":`+responseUsage+`}`)),
			wantCalls: []ToolCall{{ID: "call_1", Name: "submit", Input: json.RawMessage(`{"find`)}},
			wantStop:  StopMaxTokens,
			wantUsage: Usage{Input: 200, CacheRead: 500, Output: 59},
		},
		{
			name:      "plan token usage has no per-call cost",
			body:      completed(textOutput, `{"input_tokens":1000000,"input_tokens_details":{"cached_tokens":0},"output_tokens":100000,"total_tokens":1100000}`),
			wantText:  "hello",
			wantStop:  StopEndTurn,
			wantUsage: Usage{Input: 1_000_000, Output: 100_000},
			wantCost:  0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _ := fakeProvider(t, http.StatusOK, tt.body)
			resp, err := newTestChatGPT(t, srv, nil).Step(t.Context(), StepRequest{Model: "gpt-x", Messages: []Message{{Role: RoleUser, Text: "hi"}}})
			if err != nil {
				t.Fatal(err)
			}
			if resp.Text != tt.wantText || resp.Stop != tt.wantStop || resp.Usage != tt.wantUsage || resp.CostUSD != tt.wantCost || resp.Model != "gpt-x" || !resp.ChatGPTPlan {
				t.Fatalf("resp = %+v", resp)
			}
			if len(resp.ToolCalls) != len(tt.wantCalls) {
				t.Fatalf("tool calls = %+v, want %+v", resp.ToolCalls, tt.wantCalls)
			}
			for i, c := range tt.wantCalls {
				if got := resp.ToolCalls[i]; got.ID != c.ID || got.Name != c.Name || string(got.Input) != string(c.Input) {
					t.Fatalf("tool call %d = %+v, want %+v", i, got, c)
				}
			}
		})
	}
}

func TestChatGPTRequest(t *testing.T) {
	srv, got := fakeProvider(t, http.StatusOK, completed(textOutput, responseUsage))
	req := StepRequest{
		Model:  "gpt-x",
		System: "be brief",
		Messages: []Message{
			{Role: RoleUser, Text: "look"},
			{Role: RoleAssistant, Text: "reading", ToolCalls: []ToolCall{{ID: "call_1", Name: "read_file", Input: json.RawMessage(`{"path":"a"}`)}}},
			{Role: RoleUser, ToolResults: []ToolResult{{CallID: "call_1", Content: "nope", IsError: true}}},
		},
		Tools:      []ToolDef{{Name: "read_file", Description: "reads", InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`)}},
		ToolChoice: ToolChoice{Mode: ToolChoiceRequired},
		MaxTokens:  4096,
		Effort:     EffortHigh,
		Session:    "run-1",
	}
	if _, err := newTestChatGPT(t, srv, &tokens{token: "at-1"}).Step(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	if got.path != "/v1/responses" || got.header.Get("Authorization") != "Bearer at-1" {
		t.Fatalf("sent %s with Authorization %q", got.path, got.header.Get("Authorization"))
	}
	b := got.body
	if b["store"] != false || b["stream"] != true || b["instructions"] != "be brief" || b["model"] != "gpt-x" {
		t.Fatalf("body = %v", b)
	}
	for _, key := range []string{"max_output_tokens", "temperature", "previous_response_id"} {
		if _, ok := b[key]; ok {
			t.Fatalf("the request carries %s, which the route refuses", key)
		}
	}
	if b["prompt_cache_key"] != "run-1" {
		t.Fatalf("prompt_cache_key = %v, want the session", b["prompt_cache_key"])
	}
	if string(mustJSON(b["include"])) != `["reasoning.encrypted_content"]` {
		t.Fatalf("include = %v", b["include"])
	}
	if field(b, "reasoning", "effort") != "high" || b["tool_choice"] != "required" {
		t.Fatalf("reasoning = %v, tool_choice = %v", b["reasoning"], b["tool_choice"])
	}
	input, _ := b["input"].([]any)
	want := []map[string]any{
		{"role": "user", "content": "look"},
		{"role": "assistant", "content": "reading"},
		{"type": "function_call", "call_id": "call_1", "name": "read_file", "arguments": `{"path":"a"}`, "namespace": "kritika"},
		{"type": "function_call_output", "call_id": "call_1", "output": "Error: nope", "namespace": "kritika"},
	}
	if len(input) != len(want) {
		t.Fatalf("input = %v, want %d items", input, len(want))
	}
	for i, item := range want {
		for key, value := range item {
			if got := field(input, i, key); got != value {
				t.Fatalf("input[%d].%s = %v, want %v", i, key, got, value)
			}
		}
	}
	tools, _ := b["tools"].([]any)
	if len(tools) != 1 || field(tools, 0, "type") != "namespace" || field(tools, 0, "name") != "kritika" {
		t.Fatalf("tools = %v, want one namespace", tools)
	}
	fn := field(tools, 0, "tools", 0)
	for key, value := range map[string]any{"type": "function", "name": "read_file", "description": "reads"} {
		if got := field(fn, key); got != value {
			t.Fatalf("namespace tool %s = %v, want %v", key, got, value)
		}
	}
	if field(fn, "parameters", "properties", "path", "type") != "string" {
		t.Fatalf("namespace tool parameters = %v", field(fn, "parameters"))
	}
}

func TestChatGPTToolChoice(t *testing.T) {
	tools := []ToolDef{{Name: "submit"}}
	for _, tt := range []struct {
		name   string
		choice ToolChoice
		want   any
	}{
		{"auto", ToolChoice{}, "auto"},
		{"a named tool", ToolChoice{Mode: ToolChoiceTool, Name: "submit"}, map[string]any{"type": "function", "name": "submit"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv, got := fakeProvider(t, http.StatusOK, completed(textOutput, responseUsage))
			if _, err := newTestChatGPT(t, srv, nil).Step(t.Context(), StepRequest{Model: "m", Tools: tools, ToolChoice: tt.choice}); err != nil {
				t.Fatal(err)
			}
			if want, _ := json.Marshal(tt.want); string(want) != string(mustJSON(got.body["tool_choice"])) {
				t.Fatalf("tool_choice = %v, want %v", got.body["tool_choice"], tt.want)
			}
		})
	}
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

const planLimitBody = `{"error":{"type":"insufficient_quota","code":"subscription_sharing_usage_limit_exceeded","message":"limit reached","param":null}}`

func TestChatGPTErrors(t *testing.T) {
	step := func(t *testing.T, status int, body string, src *tokens) error {
		t.Helper()
		srv, _ := fakeProvider(t, status, body)
		var ts TokenSource
		if src != nil {
			ts = src
		}
		_, err := newTestChatGPT(t, srv, ts).Step(t.Context(), StepRequest{Model: "m", Messages: []Message{{Role: RoleUser, Text: "hi"}}})
		if err == nil {
			t.Fatal("Step answered")
		}
		return err
	}
	t.Run("the plan at its limit before the stream", func(t *testing.T) {
		err := step(t, http.StatusTooManyRequests, planLimitBody, nil)
		if !errors.Is(err, ErrPlanLimit) || Transient(err) || !strings.Contains(err.Error(), "limit reached") {
			t.Fatalf("err = %v, want ErrPlanLimit, not transient", err)
		}
	})
	t.Run("the plan at its limit after the stream opened", func(t *testing.T) {
		body := sse(responseEvent("response.failed", `{"id":"resp_1","object":"response","status":"failed","error":{"code":"subscription_sharing_usage_limit_exceeded","message":"spent"}}`))
		if err := step(t, http.StatusOK, body, nil); !errors.Is(err, ErrPlanLimit) || Transient(err) {
			t.Fatalf("err = %v, want ErrPlanLimit", err)
		}
	})
	t.Run("a plan whose usage could not be checked is transient", func(t *testing.T) {
		for _, code := range []string{planUnavailableCode, planUserUnavailableCode} {
			body := sse(responseEvent("response.failed", `{"id":"resp_1","object":"response","status":"failed","error":{"code":"`+code+`","message":"later"}}`))
			if err := step(t, http.StatusOK, body, nil); !Transient(err) || errors.Is(err, ErrPlanLimit) {
				t.Fatalf("%s: err = %v, want transient", code, err)
			}
		}
	})
	t.Run("error events carry the same plan failure codes", func(t *testing.T) {
		for _, tt := range []struct {
			code      string
			limit     bool
			transient bool
		}{{planLimitCode, true, false}, {planUnavailableCode, false, true}, {planUserUnavailableCode, false, true}} {
			body := sse(`{"type":"error","code":"` + tt.code + `","message":"later"}`)
			err := step(t, http.StatusOK, body, nil)
			if errors.Is(err, ErrPlanLimit) != tt.limit || Transient(err) != tt.transient {
				t.Fatalf("%s: err = %v", tt.code, err)
			}
		}
	})
	t.Run("another failed response is a refusal", func(t *testing.T) {
		body := sse(responseEvent("response.failed", `{"id":"resp_1","object":"response","status":"failed","error":{"code":"invalid_prompt","message":"bad"}}`))
		if err := step(t, http.StatusOK, body, nil); Transient(err) || !strings.Contains(err.Error(), "invalid_prompt") {
			t.Fatalf("err = %v, want a refusal naming the code", err)
		}
	})
	t.Run("an incomplete response is a refusal", func(t *testing.T) {
		body := sse(responseEvent("response.incomplete", `{"id":"resp_1","object":"response","status":"incomplete","incomplete_details":{"reason":"content_filter"}}`))
		if err := step(t, http.StatusOK, body, nil); Transient(err) || !strings.Contains(err.Error(), "content_filter") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a stream that ends early is transient", func(t *testing.T) {
		body := sse(`{"type":"response.created","sequence_number":0,"response":{"id":"resp_1","object":"response","status":"in_progress"}}`)
		if err := step(t, http.StatusOK, body, nil); !Transient(err) {
			t.Fatalf("err = %v, want transient", err)
		}
	})
	t.Run("a 503 is transient", func(t *testing.T) {
		if err := step(t, http.StatusServiceUnavailable, `{"error":{"message":"down","type":"server_error"}}`, nil); !Transient(err) {
			t.Fatalf("err = %v, want transient", err)
		}
	})
	t.Run("a rejected token is renewed for the next step", func(t *testing.T) {
		src := &tokens{token: "at-1"}
		err := step(t, http.StatusUnauthorized, `{"detail":"not admitted"}`, src)
		if Transient(err) || src.expired.Load() != 1 || !strings.Contains(err.Error(), "not admitted") {
			t.Fatalf("err = %v, expired %d times", err, src.expired.Load())
		}
	})
	t.Run("a token source that fails fails the step without a request", func(t *testing.T) {
		srv, got := fakeProvider(t, http.StatusOK, completed(textOutput, responseUsage))
		resp, err := newTestChatGPT(t, srv, &tokens{err: errors.New("signed out")}).Step(t.Context(), StepRequest{Model: "m"})
		if err == nil || !strings.Contains(err.Error(), "signed out") || got.path != "" {
			t.Fatalf("err = %v, request to %q", err, got.path)
		}
		if !resp.ChatGPTPlan {
			t.Fatal("a failed step on the plan is not marked as the plan's")
		}
	})
}

// TestChatGPTStreamFailuresRetry: a server error or rate limit after the
// stream opened is retried as the same failure before it would be.
func TestChatGPTStreamFailuresRetry(t *testing.T) {
	for _, code := range []string{"server_error", "rate_limit_exceeded"} {
		for name, body := range map[string]string{
			"response.failed": sse(responseEvent("response.failed", `{"id":"resp_1","object":"response","status":"failed","error":{"code":"`+code+`","message":"later"}}`)),
			"error event":     sse(`{"type":"error","code":"` + code + `","message":"later"}`),
		} {
			t.Run(code+"/"+name, func(t *testing.T) {
				srv, _ := fakeProvider(t, http.StatusOK, body)
				_, err := newTestChatGPT(t, srv, nil).Step(t.Context(), StepRequest{Model: "m", Messages: []Message{{Role: RoleUser, Text: "hi"}}})
				if !Transient(err) || !strings.Contains(err.Error(), code) {
					t.Fatalf("err = %v, want transient", err)
				}
			})
		}
	}
}

// TestChatGPTPause: after the plan refuses a step at its limit, the adapter
// fails every step for PlanPause without a request, then sends again.
func TestChatGPTPause(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("x-should-retry", "false")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(planLimitBody))
	}))
	t.Cleanup(srv.Close)
	c := newTestChatGPT(t, srv, nil)
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }
	req := StepRequest{Model: "m", Fallbacks: []string{"other"}, Messages: []Message{{Role: RoleUser, Text: "hi"}}}
	for i := range 3 {
		_, err := c.Step(t.Context(), req)
		if !errors.Is(err, ErrPlanLimit) || Transient(err) {
			t.Fatalf("step %d: err = %v", i, err)
		}
	}
	if n.Load() != 1 {
		t.Fatalf("the plan got %d requests, want 1 before the pause", n.Load())
	}
	now = now.Add(PlanPause)
	if _, err := c.Step(t.Context(), req); !errors.Is(err, ErrPlanLimit) || n.Load() != 2 {
		t.Fatalf("after the pause: err = %v, %d requests", err, n.Load())
	}
}

func TestNewChatGPTRejects(t *testing.T) {
	tests := []struct {
		name string
		cfg  ChatGPTConfig
	}{
		{"no token source", ChatGPTConfig{}},
		{"relative base URL", ChatGPTConfig{BaseURL: "gw.example.com/v1", Tokens: &tokens{}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewChatGPT(tt.cfg); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
	t.Run("the default URL ignores OPENAI_BASE_URL", func(t *testing.T) {
		t.Setenv("OPENAI_BASE_URL", "https://elsewhere.example.com/v1")
		t.Setenv("OPENAI_API_KEY", "sk-env")
		var host, auth string
		client := &http.Client{Transport: roundTripper(func(r *http.Request) (*http.Response, error) {
			host, auth = r.URL.Host, r.Header.Get("Authorization")
			return &http.Response{StatusCode: http.StatusUnauthorized, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`)), Request: r}, nil
		})}
		c, err := NewChatGPT(ChatGPTConfig{Tokens: &tokens{token: "at-1"}, HTTPClient: client})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Step(t.Context(), StepRequest{Model: "m"}); err == nil || host != "api.openai.com" || auth != "Bearer at-1" {
			t.Fatalf("reached %q with %q (%v), want api.openai.com with the plan's token", host, auth, err)
		}
	})
}

func TestChatGPTMasksToken(t *testing.T) {
	for _, tt := range []struct {
		name   string
		status int
		body   string
	}{
		{"refusal", http.StatusServiceUnavailable, `{"error":{"message":"token at-secret refused","type":"server_error"}}`},
		{"stream failure", http.StatusOK, sse(responseEvent("response.failed", `{"error":{"code":"invalid_prompt","message":"at-secret"}}`))},
		{"text and tool input", http.StatusOK, completed(`[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"at-secret"}]},`+
			`{"type":"function_call","call_id":"call_1","name":"read_file","arguments":"{\"path\":\"at-secret\"}"}]`, responseUsage)},
		{"streamed text and tool input", http.StatusOK, sse(
			outputItemDone(0, `{"type":"message","content":[{"type":"output_text","text":"at-secret"}]}`),
			outputItemDone(1, `{"type":"function_call","call_id":"call_1","name":"read_file","arguments":"{\"path\":\"at-secret\"}"}`)) + completed("[]", responseUsage)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv, _ := fakeProvider(t, tt.status, tt.body)
			resp, err := newTestChatGPT(t, srv, &tokens{token: "at-secret"}).Step(t.Context(), StepRequest{Model: "m"})
			if strings.HasSuffix(tt.name, "text and tool input") {
				if err != nil || resp.Text != "***" || len(resp.ToolCalls) != 1 || string(resp.ToolCalls[0].Input) != `{"path":"***"}` {
					t.Fatalf("masked response = %+v, %v", resp, err)
				}
			} else if err == nil {
				t.Fatal("failed response succeeded")
			}
			if tt.status == http.StatusServiceUnavailable && !Transient(err) {
				t.Fatalf("masked error lost retry classification: %v", err)
			}
			if err != nil && strings.Contains(err.Error(), "at-secret") {
				t.Fatalf("error contains token: %v", err)
			}
			if strings.Contains(resp.Text, "at-secret") {
				t.Fatal("text contains token")
			}
			for _, call := range resp.ToolCalls {
				if strings.Contains(string(call.Input), "at-secret") {
					t.Fatal("tool input contains token")
				}
			}
			for _, item := range resp.ChatGPTOutput {
				if strings.Contains(string(item), "at-secret") {
					t.Fatal("replay output contains token")
				}
			}
		})
	}
}

func TestChatGPTObservesAllowances(t *testing.T) {
	for _, tt := range []struct {
		name    string
		status  int
		body    string
		used    float64
		wantErr bool
	}{
		{"headers", http.StatusOK, completed(textOutput, responseUsage), 25, false},
		{"event supersedes headers", http.StatusOK,
			sse(`{"type":"codex.rate_limits","rate_limits":{"primary":{"used_percent":31,"window_minutes":300,"reset_at":1800000000}}}`) + completed(textOutput, responseUsage), 31, false},
		{"HTTP limit", http.StatusTooManyRequests, planLimitBody, 25, true},
		{"stream limit", http.StatusOK, sse(responseEvent("response.failed", `{"error":{"code":"subscription_sharing_usage_limit_exceeded","message":"spent"}}`)), 25, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.Header().Set("x-codex-primary-used-percent", "25")
				w.Header().Set("x-codex-primary-window-minutes", "300")
				w.Header().Set("x-codex-primary-reset-at", "1800000000")
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			t.Cleanup(srv.Close)
			var observed []chatgpt.Allowance
			c, err := NewChatGPT(ChatGPTConfig{BaseURL: srv.URL + "/v1", Tokens: &tokens{token: "at-1"},
				ObserveAllowances: func(_ context.Context, a []chatgpt.Allowance) { observed = a },
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = c.Step(t.Context(), StepRequest{Model: "m"})
			if (err != nil) != tt.wantErr || len(observed) == 0 || observed[len(observed)-1].Primary.UsedPercent != tt.used {
				t.Fatalf("observed %+v, error %v", observed, err)
			}
		})
	}
}
