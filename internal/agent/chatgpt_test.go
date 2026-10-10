package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/home-operations/kritika/internal/model"
)

type chatGPTTokens struct{}

func (chatGPTTokens) Token(context.Context) (string, error) { return "at-test", nil }
func (chatGPTTokens) Expire(context.Context, string) error  { return nil }

func TestRunChatGPTStreamedTools(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" || r.Header.Get("Authorization") != "Bearer at-test" {
			t.Errorf("request to %s with auth %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		var body struct {
			Input []struct {
				Type   string `json:"type"`
				CallID string `json:"call_id"`
				Output string `json:"output"`
			} `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		var name, id, args string
		switch calls.Add(1) {
		case 1:
			name, id, args = "read_file", "read_1", `{"path":"main.go"}`
		case 2:
			found := false
			for _, item := range body.Input {
				if item.Type == "function_call_output" && item.CallID == "read_1" && item.Output == "package main" {
					found = true
				}
			}
			if !found {
				t.Error("the read_file result was not returned on the next step")
			}
			name, id, args = "submit_review", "submit_1", validSubmitInput
		default:
			t.Error("review did not submit after two steps")
			// A refusal the loop does not retry, so an extra step fails the
			// test at once instead of after stepRetryWaits.
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		item := fmt.Sprintf(`{"type":"function_call","call_id":%q,"name":%q,"namespace":"kritika","arguments":%q,"status":"completed"}`, id, name, args)
		w.Header().Set("Content-Type", "text/event-stream")
		if _, err := fmt.Fprintf(w, "event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":%s}\n\n"+
			"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":10,\"output_tokens\":3}}}\n\n", item); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(srv.Close)
	c, err := model.NewChatGPT(model.ChatGPTConfig{BaseURL: srv.URL + "/v1", Tokens: chatGPTTokens{}})
	if err != nil {
		t.Fatal(err)
	}
	run := Run{Stepper: c, Model: "gpt-x", User: "review this change", Submit: testSubmitDef,
		Tools: []Tool{&fakeTool{name: "read_file", output: "package main"}}}
	result := run.Do(t.Context())
	if result.Stop != StopSubmitted || result.Err != "" || result.Steps != 2 || string(result.Submitted) != validSubmitInput ||
		result.ToolCalls["read_file"] != 1 || result.Usage != (model.Usage{Input: 20, Output: 6}) || result.CostUSD != 0 || calls.Load() != 2 {
		t.Fatalf("review = %+v after %d requests", result, calls.Load())
	}
}
