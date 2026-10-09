package runner

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/home-operations/kritika/internal/agent"
	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/review"
)

// TestCarryOn: a review carries on the conversation the gateway serves it
// only when it would send that conversation's system prompt and tools;
// otherwise, or when the gateway serves none, it starts afresh.
func TestCarryOn(t *testing.T) {
	s := agentPromptSpec()
	s.Prompt.Continue = &Continuation{RunID: "run-0", Session: "run-0"}
	s.Prompt.Dismissed = []review.DismissedFinding{{Path: "main.go", Line: 1, Severity: review.SeverityP2, Title: "dismissed one"}}
	head := tree(t, map[string]string{"main.go": "package main\n"})
	tools := offeredTools(s, head, nil, nil)
	fresh := loopPrompt(s, false)
	pr := s.Prompt.PullRequest
	opening := func(body string) string {
		msg, _, _ := review.Build(review.Input{Repository: s.Prompt.Repository, Number: pr.Number, Title: pr.Title, Author: pr.Author,
			Body: body, BaseRef: pr.BaseRef, Changed: []string{"main.go"}, Diff: agentDiff, BudgetTokens: review.UserBudget(fresh.system, 0)})
		return msg
	}
	conversation := func(edit func(*agent.Conversation)) string {
		c := agent.Conversation{
			System: fresh.system, Tools: agent.Run{Tools: tools, Submit: fresh.submit}.ToolDefs(), Tokens: 900,
			Messages: []model.Message{
				{Role: model.RoleUser, Text: opening(pr.Body)}, {Role: model.RoleAssistant, ToolCalls: []model.ToolCall{call("1", "submit_review", `{}`)}},
			},
		}
		if edit != nil {
			edit(&c)
		}
		b, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	for _, tc := range []struct {
		name    string
		status  int
		body    string
		carried bool
		fetched bool
	}{
		{name: "the same system prompt, tools and description", status: http.StatusOK, body: conversation(nil), carried: true},
		{name: "one that fetched a repository", status: http.StatusOK, carried: true, fetched: true,
			body: conversation(func(c *agent.Conversation) {
				c.Messages[1].ToolCalls = append([]model.ToolCall{call("0", "fetch_repo", `{}`)}, c.Messages[1].ToolCalls...)
			})},
		{name: "another system prompt", status: http.StatusOK, body: conversation(func(c *agent.Conversation) { c.System = "An older one." })},
		{name: "an older description", status: http.StatusOK, body: conversation(func(c *agent.Conversation) { c.Messages[0].Text = opening("Adds a.") })},
		{name: "too large for the run's budget", status: http.StatusOK, body: conversation(func(c *agent.Conversation) { c.Tokens = 60_000 })},
		{name: "none served", status: http.StatusNotFound, body: `{"error":{"code":"not_found"}}`},
		{name: "not a conversation", status: http.StatusOK, body: `[`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var path, auth string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				path, auth = r.Method+" "+r.URL.Path, r.Header.Get("Authorization")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			s := s
			s.Model = &ModelEndpoint{GatewayURL: srv.URL, Model: "review"}
			got := carryOn(t.Context(), s, Secrets{GatewayToken: "krk_run"}, fresh, tools, agentDiff, slog.New(slog.DiscardHandler))
			if path != "GET /v1/conversation" || auth != "Bearer krk_run" {
				t.Fatalf("request = %s with %q", path, auth)
			}
			if !tc.carried {
				if got.carried != nil || got.user != fresh.user {
					t.Fatalf("a conversation that cannot be carried on was: %+v", got)
				}
				return
			}
			if got.carried == nil || got.carried.Tokens != 900 || got.system != fresh.system || got.submit.Name != fresh.submit.Name {
				t.Fatalf("prompt = %+v", got)
			}
			for _, want := range []string{"moved from " + shaB[:7] + " to " + shaA[:7], "+func b() {}", "- main.go:1 [p2] dismissed one"} {
				if !strings.Contains(got.user, want) {
					t.Fatalf("missing %q in the next turn:\n%s", want, got.user)
				}
			}
			if strings.Contains(got.user, "fetch_repo fetched then") != tc.fetched {
				t.Fatalf("the next turn says the fetches are gone: %t, want %t:\n%s", !tc.fetched, tc.fetched, got.user)
			}
		})
	}
}

func TestSpecContinuation(t *testing.T) {
	s := reviewSpec()
	s.Prompt.Continue = &Continuation{RunID: "run-0", Session: "run-0"}
	if err := s.Validate(); err != nil {
		t.Fatalf("a continuation with a run and a session: %v", err)
	}
	s.Prompt.Continue.Session = ""
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "continuation") {
		t.Fatalf("a continuation without a session = %v", err)
	}
}
