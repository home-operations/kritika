package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"testing"
)

func outputItemDone(index int, item string) string {
	return fmt.Sprintf(`{"type":"response.output_item.done","output_index":%d,"item":%s}`, index, item)
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
			want := []json.RawMessage{json.RawMessage(reasoning), json.RawMessage(call), json.RawMessage(message)}
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
			if err := json.Unmarshal([]byte(tt.output), &want.ChatGPTOutput); err != nil {
				t.Fatal(err)
			}
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
			if err := json.Unmarshal([]byte(tt.output), &want.ChatGPTOutput); err != nil {
				t.Fatal(err)
			}
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
