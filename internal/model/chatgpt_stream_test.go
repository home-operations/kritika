package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func outputItemDone(index int, item string) string {
	return fmt.Sprintf(`{"type":"response.output_item.done","output_index":%d,"item":%s}`, index, item)
}

// planOutput is newTestChatGPT's output of items, a JSON array; nil for
// "null".
func planOutput(t *testing.T, items string) *ChatGPTOutput {
	t.Helper()
	var raw []json.RawMessage
	if err := json.Unmarshal([]byte(items), &raw); err != nil {
		t.Fatal(err)
	}
	if raw == nil {
		return nil
	}
	return &ChatGPTOutput{Provider: testPlan, Items: raw}
}

func TestChatGPTReplaysOutput(t *testing.T) {
	const reasoning = `{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"opaque-reasoning"}`
	const message = `{"type":"message","id":"msg_1","role":"assistant","phase":"commentary","status":"completed","content":[{"type":"output_text","text":"reading","annotations":[]}]}`
	const call = `{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read_file","namespace":"kritika","arguments":"{}","status":"completed"}`
	output := "[" + reasoning + "," + call + "," + message + "]"
	streamed := sse(outputItemDone(0, reasoning), outputItemDone(1, call), outputItemDone(2, message))
	incomplete := responseEvent("response.incomplete", `{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[],"usage":`+responseUsage+`}`)
	for _, tt := range []struct{ name, body string }{
		{"completed output", completed(output, responseUsage)},
		{"streamed output", streamed + completed("[]", responseUsage)},
		{"partial snapshot", streamed + completed("["+reasoning+"]", responseUsage)},
		{"duplicate snapshot", streamed + completed(output, responseUsage)},
		{"output cap", streamed + sse(incomplete)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv, got := fakeProvider(t, http.StatusOK, tt.body)
			client := newTestChatGPT(t, srv, nil)
			req := StepRequest{Model: "gpt-x", System: "review", Session: "run-1", Messages: []Message{{Role: RoleUser, Text: "look"}}}
			resp, err := client.Step(t.Context(), req)
			if err != nil {
				t.Fatal(err)
			}
			want := &ChatGPTOutput{Provider: testPlan, Items: []json.RawMessage{json.RawMessage(reasoning), json.RawMessage(call), json.RawMessage(message)}}
			if !reflect.DeepEqual(resp.ChatGPTOutput, want) {
				t.Fatalf("output = %s, want %s", mustJSON(resp.ChatGPTOutput), output)
			}
			prefix := got.body["input"].([]any)
			req.Messages = append(req.Messages,
				Message{Role: RoleAssistant, Text: resp.Text, ToolCalls: resp.ToolCalls, ChatGPTOutput: resp.ChatGPTOutput},
				Message{Role: RoleUser, ToolResults: []ToolResult{{CallID: "call_1", Content: "file contents"}}})
			if _, err := client.Step(t.Context(), req); err != nil {
				t.Fatal(err)
			}
			input := got.body["input"].([]any)
			if len(input) != 5 || !reflect.DeepEqual(input[:1], prefix) || !jsonEqual(t, mustJSON(input[1:4]), json.RawMessage(output)) {
				t.Fatalf("replayed input = %s", mustJSON(input))
			}
			if field(input, 4, "type") != "function_call_output" || field(input, 4, "call_id") != "call_1" {
				t.Fatalf("tool result = %s", mustJSON(input[4]))
			}
		})
	}
}

func TestChatGPTStreamedOutput(t *testing.T) {
	const message = `{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"hello"}]}`
	const reasoning = `{"type":"reasoning","id":"rs_1","summary":[]}`
	const refusal = `{"type":"message","content":[{"type":"refusal","refusal":"no"}]}`
	call := fmt.Sprintf(`{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read_file","namespace":"kritika","arguments":%q,"status":"completed"}`, `{"path":"main.go"}`)
	submit := fmt.Sprintf(`{"type":"function_call","id":"fc_2","call_id":"call_2","name":"submit_review","namespace":"kritika","arguments":%q,"status":"completed"}`, `{"verdict":"approve"}`)
	readCall := ToolCall{ID: "call_1", Name: "read_file", Input: json.RawMessage(`{"path":"main.go"}`)}
	submitCall := ToolCall{ID: "call_2", Name: "submit_review", Input: json.RawMessage(`{"verdict":"approve"}`)}
	incomplete := sse(responseEvent("response.incomplete", `{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[],"usage":`+responseUsage+`}`))
	for _, tt := range []struct {
		name, body, text string
		calls            []ToolCall
		stop             StopReason
		output           string
	}{
		{"text from a done item", sse(outputItemDone(0, message)) + completed("[]", responseUsage), "hello", nil, StopEndTurn, "[" + message + "]"},
		{"tool from a done item", sse(outputItemDone(0, call)) + completed("[]", responseUsage), "", []ToolCall{readCall}, StopToolUse, "[" + call + "]"},
		{"completed output omitted", sse(outputItemDone(0, message), responseEvent("response.completed", `{"status":"completed","usage":`+responseUsage+`}`)), "hello", nil, StopEndTurn, "[" + message + "]"},
		{"done items in arrival order whatever their index", sse(outputItemDone(0, reasoning), outputItemDone(0, message), outputItemDone(0, submit), outputItemDone(0, call)) + completed("[]", responseUsage), "hello", []ToolCall{submitCall, readCall}, StopToolUse, "[" + reasoning + "," + message + "," + submit + "," + call + "]"},
		{"completed snapshot is not duplicated", sse(outputItemDone(0, message), outputItemDone(1, call)) + completed("["+message+","+call+"]", responseUsage), "hello", []ToolCall{readCall}, StopToolUse, "[" + message + "," + call + "]"},
		{"partial snapshot", sse(outputItemDone(0, reasoning), outputItemDone(1, message), outputItemDone(2, submit)) + completed("["+reasoning+"]", responseUsage), "hello", []ToolCall{submitCall}, StopToolUse, "[" + reasoning + "," + message + "," + submit + "]"},
		{"done items win over the snapshot", sse(outputItemDone(0, message)) + completed(`[{"type":"message","content":[{"type":"output_text","text":"final"}]}]`, responseUsage), "hello", nil, StopEndTurn, "[" + message + "]"},
		{"text retained at the output cap", sse(outputItemDone(0, message)) + incomplete, "hello", nil, StopMaxTokens, "[" + message + "]"},
		{"tool retained at the output cap", sse(outputItemDone(0, call)) + incomplete, "", []ToolCall{readCall}, StopMaxTokens, "[" + call + "]"},
		{"refusal from a done item", sse(outputItemDone(0, refusal)) + completed("[]", responseUsage), "no", nil, StopEndTurn, "[" + refusal + "]"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv, _ := fakeProvider(t, http.StatusOK, tt.body)
			resp, err := newTestChatGPT(t, srv, nil).Step(t.Context(), StepRequest{Model: "gpt-x"})
			if err != nil {
				t.Fatal(err)
			}
			want := StepResponse{Text: tt.text, ToolCalls: tt.calls, Stop: tt.stop, Model: "gpt-x", ChatGPTPlan: true,
				Usage: Usage{Input: 200, CacheRead: 500, Output: 59}}
			want.ChatGPTOutput = planOutput(t, tt.output)
			if !reflect.DeepEqual(resp, want) {
				t.Fatalf("Step = %+v, want %+v", resp, want)
			}
		})
	}
}

func TestChatGPTEmptyResponse(t *testing.T) {
	const reasoning = `{"type":"reasoning","summary":[]}`
	for _, tt := range []struct{ name, body, output string }{
		{"empty output", completed("[]", responseUsage), "null"},
		{"reasoning only", sse(outputItemDone(0, reasoning)) + completed("[]", responseUsage), "[" + reasoning + "]"},
		{"reasoning in the snapshot", completed("["+reasoning+"]", responseUsage), "[" + reasoning + "]"},
		{"unfinished tool", sse(`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","call_id":"call_1","name":"read_file","arguments":"","status":"in_progress"}}`) + completed("[]", responseUsage), "null"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv, _ := fakeProvider(t, http.StatusOK, tt.body)
			resp, err := newTestChatGPT(t, srv, nil).Step(t.Context(), StepRequest{Model: "gpt-x"})
			want := StepResponse{Stop: StopEndTurn, Model: "gpt-x", ChatGPTPlan: true, Usage: Usage{Input: 200, CacheRead: 500, Output: 59}}
			want.ChatGPTOutput = planOutput(t, tt.output)
			if err != nil || !reflect.DeepEqual(resp, want) {
				t.Fatalf("Step = %+v, %v; want %+v", resp, err, want)
			}
		})
	}
}

func TestChatGPTDoneItemsRequireCompletion(t *testing.T) {
	item := outputItemDone(0, `{"type":"function_call","call_id":"call_1","name":"submit_review","arguments":"{}"}`)
	for _, tt := range []struct {
		name, body string
		want       error
	}{
		{"stream cut after an item", sse(item), io.ErrUnexpectedEOF},
		{"failed after an item", sse(item, responseEvent("response.failed", `{"error":{"code":"server_error","message":"down"}}`)), ErrUnavailable},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv, _ := fakeProvider(t, http.StatusOK, tt.body)
			resp, err := newTestChatGPT(t, srv, nil).Step(t.Context(), StepRequest{Model: "gpt-x"})
			if !errors.Is(err, tt.want) || !Transient(err) || len(resp.ToolCalls) != 0 {
				t.Fatalf("Step = %+v, %v", resp, err)
			}
		})
	}
}

func TestChatGPTStreamedConfidence(t *testing.T) {
	const answer = `{"score":100,"risk":"low","reason":"safe change"}`
	item := fmt.Sprintf(`{"type":"function_call","call_id":"confidence_1","name":"confidence","namespace":"kritika","arguments":%q,"status":"completed"}`, answer)
	srv, _ := fakeProvider(t, http.StatusOK, sse(outputItemDone(0, item))+completed("[]", responseUsage))
	c := Structured{Stepper: newTestChatGPT(t, srv, nil)}
	resp, err := c.Complete(t.Context(), CompletionRequest{Model: "gpt-x", User: "score this review", SchemaName: "confidence",
		Schema: json.RawMessage(`{"type":"object","properties":{"score":{"type":"integer"},"risk":{"type":"string"},"reason":{"type":"string"}},"required":["score","risk","reason"]}`)})
	if err != nil {
		t.Fatal(err)
	}
	want := CompletionResponse{Raw: answer, Model: "gpt-x", InputTokens: 700, CachedTokens: 500, OutputTokens: 59}
	if resp != want {
		t.Fatalf("Complete = %+v, want %+v", resp, want)
	}
}

func TestChatGPTReplayable(t *testing.T) {
	const reasoning = `{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"plan"}],"encrypted_content":"opaque"}`
	const call = `{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read_file","namespace":"kritika","arguments":"{}"}`
	const message = `{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"reading"}]}`
	turn := func(items ...string) Message {
		out := &ChatGPTOutput{Provider: testPlan}
		for _, item := range items {
			out.Items = append(out.Items, json.RawMessage(item))
		}
		return Message{Role: RoleAssistant, Text: "reading", ToolCalls: []ToolCall{{ID: "call_1", Name: "read_file"}}, ChatGPTOutput: out}
	}
	with := func(m Message, edit func(*Message)) Message { edit(&m); return m }
	for _, tt := range []struct {
		name     string
		m        Message
		provider string
		want     bool
	}{
		{"a turn as it came", turn(reasoning, call, message), testPlan, true},
		{"another provider's output", turn(reasoning, call, message), "other", false},
		{"an adapter serving no provider", turn(reasoning, call, message), "", false},
		{"a user turn", with(turn(message), func(m *Message) { m.Role, m.ToolCalls = RoleUser, nil }), testPlan, false},
		{"reasoning without its encrypted content", turn(`{"type":"reasoning","id":"rs_1","summary":[]}`, call, message), testPlan, false},
		{"reasoning that leads to no item", with(turn(reasoning), func(m *Message) { m.Text, m.ToolCalls = "(no response)", nil }), testPlan, false},
		{"reasoning after the calls", with(turn(call, reasoning), func(m *Message) { m.Text = "" }), testPlan, false},
		{"text the loop changed", with(turn(reasoning, call, message), func(m *Message) { m.Text = "edited" }), testPlan, false},
		{"calls that differ", with(turn(reasoning, call, message), func(m *Message) { m.ToolCalls[0].ID = "call_2" }), testPlan, false},
		{"a user message", turn(reasoning, call, `{"type":"message","role":"user","content":[{"type":"output_text","text":"reading"}]}`), testPlan, false},
		{"a developer message", turn(reasoning, call, `{"type":"message","role":"developer","content":[{"type":"output_text","text":"reading"}]}`), testPlan, false},
		{"an image part", turn(reasoning, call, `{"type":"message","role":"assistant","content":[{"type":"input_image","image_url":"https://example.com/x"},{"type":"output_text","text":"reading"}]}`), testPlan, false},
		{"an image in a reasoning summary", turn(`{"type":"reasoning","summary":[{"type":"input_image","image_url":"https://example.com/x"}],"encrypted_content":"opaque"}`, call, message), testPlan, false},
		{"an item reference", turn(reasoning, call, message, `{"type":"item_reference","id":"msg_0"}`), testPlan, false},
		{"a call in another namespace", turn(reasoning, `{"type":"function_call","call_id":"call_1","name":"read_file","namespace":"other","arguments":"{}"}`, message), testPlan, false},
		{"a malformed item", turn(reasoning, call, message, `{"type":`), testPlan, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := replayable(tt.m, tt.provider); got != tt.want {
				t.Fatalf("replayable = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestChatGPTSendsUnreplayableTurnsAsTextAndCalls: a turn whose output the
// route would refuse goes as the loop recorded it, reasoning left out.
func TestChatGPTSendsUnreplayableTurnsAsTextAndCalls(t *testing.T) {
	const reasoning = `{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"opaque"}`
	const call = `{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read_file","namespace":"kritika","arguments":"{}"}`
	for _, tt := range []struct {
		name  string
		turn  []Message
		types []string
	}{
		{"reasoning that leads to no item", []Message{
			{Role: RoleAssistant, Text: "(no response)", ChatGPTOutput: &ChatGPTOutput{Provider: testPlan, Items: []json.RawMessage{json.RawMessage(reasoning)}}},
			{Role: RoleUser, Text: "go on"},
		}, []string{"message", "message", "message"}},
		{"another provider's output", []Message{
			{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "call_1", Name: "read_file", Input: json.RawMessage(`{}`)}},
				ChatGPTOutput: &ChatGPTOutput{Provider: "other", Items: []json.RawMessage{json.RawMessage(reasoning), json.RawMessage(call)}}},
			{Role: RoleUser, ToolResults: []ToolResult{{CallID: "call_1", Content: "file contents"}}},
		}, []string{"message", "function_call", "function_call_output"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv, got := fakeProvider(t, http.StatusOK, completed(textOutput, responseUsage))
			req := StepRequest{Model: "gpt-x", Messages: append([]Message{{Role: RoleUser, Text: "look"}}, tt.turn...)}
			if _, err := newTestChatGPT(t, srv, nil).Step(t.Context(), req); err != nil {
				t.Fatal(err)
			}
			input := got.body["input"].([]any)
			types := make([]string, 0, len(input))
			for i := range input {
				// A turn sent as its text is an easy input message, which
				// has a role and no type.
				kind, _ := field(input, i, "type").(string)
				if kind == "" && field(input, i, "role") != nil {
					kind = "message"
				}
				types = append(types, kind)
			}
			if !reflect.DeepEqual(types, tt.types) {
				t.Fatalf("input = %s, want items of types %v", mustJSON(input), tt.types)
			}
			for i := range input {
				if field(input, i, "id") != nil {
					t.Fatalf("input = %s, want no item replayed as it came", mustJSON(input))
				}
			}
		})
	}
}

// answer is one response of a sequence.
type answer struct {
	status int
	body   string
}

// sequence answers each request with the next of answers and records the
// body of every request.
func sequence(t *testing.T, answers ...answer) (*httptest.Server, *[]map[string]any) {
	t.Helper()
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		bodies = append(bodies, body)
		a := answers[min(len(bodies), len(answers))-1]
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("x-should-retry", "false")
		w.WriteHeader(a.status)
		_, _ = w.Write([]byte(a.body))
	}))
	t.Cleanup(srv.Close)
	return srv, &bodies
}

func TestChatGPTResendsWithoutRefusedReasoning(t *testing.T) {
	const reasoning = `{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"opaque"}`
	const call = `{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read_file","namespace":"kritika","arguments":"{}"}`
	refusedBody := `{"error":{"message":"The encrypted content could not be verified.","type":"invalid_request_error","param":null,"code":"invalid_encrypted_content"}}`
	refused := answer{http.StatusBadRequest, refusedBody}
	failed := answer{http.StatusOK, sse(responseEvent("response.failed", `{"id":"resp_1","object":"response","status":"failed","error":{"code":"invalid_encrypted_content","message":"could not be verified"}}`))}
	ok := answer{http.StatusOK, completed(textOutput, responseUsage)}
	turn := Message{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "call_1", Name: "read_file", Input: json.RawMessage(`{}`)}},
		ChatGPTOutput: &ChatGPTOutput{Provider: testPlan, Items: []json.RawMessage{json.RawMessage(reasoning), json.RawMessage(call)}}}
	result := Message{Role: RoleUser, ToolResults: []ToolResult{{CallID: "call_1", Content: "file contents"}}}
	for _, tt := range []struct {
		name     string
		answers  []answer
		replayed bool
	}{
		{"refused before the stream", []answer{refused, ok}, true},
		{"refused in the stream", []answer{failed, ok}, true},
		{"refused with nothing replayed", []answer{refused, ok}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv, bodies := sequence(t, tt.answers...)
			sent := turn
			if !tt.replayed {
				sent.ChatGPTOutput = &ChatGPTOutput{Provider: "other", Items: turn.ChatGPTOutput.Items}
			}
			req := StepRequest{Model: "gpt-x", Messages: []Message{{Role: RoleUser, Text: "look"}, sent, result}}
			resp, err := newTestChatGPT(t, srv, nil).Step(t.Context(), req)
			if !tt.replayed {
				if !errors.Is(err, errEncryptedContent) || len(*bodies) != 1 {
					t.Fatalf("Step = %v after %d requests, want the refusal after one", err, len(*bodies))
				}
				return
			}
			if err != nil || resp.Text != "hello" || len(*bodies) != 2 {
				t.Fatalf("Step = %+v, %v after %d requests", resp, err, len(*bodies))
			}
			first, second := (*bodies)[0]["input"].([]any), (*bodies)[1]["input"].([]any)
			if field(first, 1, "type") != "reasoning" {
				t.Fatalf("first input = %s, want the replayed reasoning", mustJSON(first))
			}
			for i := range second {
				if field(second, i, "type") == "reasoning" {
					t.Fatalf("second input = %s, want no reasoning", mustJSON(second))
				}
			}
			if field(second, 1, "type") != "function_call" || field(second, 1, "call_id") != "call_1" {
				t.Fatalf("second input = %s, want the call as recorded", mustJSON(second))
			}
		})
	}
}

func TestChatGPTOnceKeepsNoOutput(t *testing.T) {
	srv, got := fakeProvider(t, http.StatusOK, completed(textOutput, responseUsage))
	resp, err := newTestChatGPT(t, srv, nil).Step(t.Context(), StepRequest{Model: "gpt-x", Once: true, Messages: []Message{{Role: RoleUser, Text: "look"}}})
	if err != nil || resp.Text != "hello" {
		t.Fatalf("Step = %+v, %v", resp, err)
	}
	if _, ok := got.body["include"]; ok || resp.ChatGPTOutput != nil {
		t.Fatalf("include = %v, output = %+v; want neither on a step no later one reads", got.body["include"], resp.ChatGPTOutput)
	}
}
