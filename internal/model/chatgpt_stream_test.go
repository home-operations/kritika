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

func TestChatGPTStreamedOutput(t *testing.T) {
	const message = `{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"hello"}]}`
	const reasoning = `{"type":"reasoning","id":"rs_1","summary":[]}`
	call := fmt.Sprintf(`{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read_file","namespace":"kritika","arguments":%q,"status":"completed"}`, `{"path":"main.go"}`)
	submit := fmt.Sprintf(`{"type":"function_call","id":"fc_2","call_id":"call_2","name":"submit_review","namespace":"kritika","arguments":%q,"status":"completed"}`, `{"verdict":"approve"}`)
	readCall := ToolCall{ID: "call_1", Name: "read_file", Input: json.RawMessage(`{"path":"main.go"}`)}
	submitCall := ToolCall{ID: "call_2", Name: "submit_review", Input: json.RawMessage(`{"verdict":"approve"}`)}
	incomplete := sse(responseEvent("response.incomplete", `{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[],"usage":`+responseUsage+`}`))
	for _, tt := range []struct {
		name, body, text string
		calls            []ToolCall
		stop             StopReason
	}{
		{"text from a done item", sse(outputItemDone(0, message)) + completed("[]", responseUsage), "hello", nil, StopEndTurn},
		{"tool from a done item", sse(outputItemDone(0, call)) + completed("[]", responseUsage), "", []ToolCall{readCall}, StopToolUse},
		{"completed output omitted", sse(outputItemDone(0, message), responseEvent("response.completed", `{"status":"completed","usage":`+responseUsage+`}`)), "hello", nil, StopEndTurn},
		{"done items ordered by output index", sse(outputItemDone(3, submit), outputItemDone(0, reasoning), outputItemDone(2, call), outputItemDone(1, message)) + completed("[]", responseUsage), "hello", []ToolCall{readCall, submitCall}, StopToolUse},
		{"repeated done item", sse(outputItemDone(0, call), outputItemDone(0, call)) + completed("[]", responseUsage), "", []ToolCall{readCall}, StopToolUse},
		{"completed snapshot is not duplicated", sse(outputItemDone(0, message), outputItemDone(1, call)) + completed("["+message+","+call+"]", responseUsage), "hello", []ToolCall{readCall}, StopToolUse},
		{"populated snapshot wins", sse(outputItemDone(0, message)) + completed(`[{"type":"message","content":[{"type":"output_text","text":"final"}]}]`, responseUsage), "final", nil, StopEndTurn},
		{"text retained at the output cap", sse(outputItemDone(0, message)) + incomplete, "hello", nil, StopMaxTokens},
		{"tool retained at the output cap", sse(outputItemDone(0, call)) + incomplete, "", []ToolCall{readCall}, StopMaxTokens},
		{"refusal from a done item", sse(outputItemDone(0, `{"type":"message","content":[{"type":"refusal","refusal":"no"}]}`)) + completed("[]", responseUsage), "no", nil, StopEndTurn},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv, _ := fakeProvider(t, http.StatusOK, tt.body)
			resp, err := newTestChatGPT(t, srv, nil).Step(t.Context(), StepRequest{Model: "gpt-x"})
			if err != nil {
				t.Fatal(err)
			}
			want := StepResponse{Text: tt.text, ToolCalls: tt.calls, Stop: tt.stop, Model: "gpt-x", ChatGPTPlan: true,
				Usage: Usage{Input: 200, CacheRead: 500, Output: 59}}
			if !reflect.DeepEqual(resp, want) {
				t.Fatalf("Step = %+v, want %+v", resp, want)
			}
		})
	}
}

func TestChatGPTEmptyResponse(t *testing.T) {
	for _, tt := range []struct{ name, body string }{
		{"empty output", completed("[]", responseUsage)},
		{"reasoning only", sse(outputItemDone(0, `{"type":"reasoning","summary":[]}`)) + completed("[]", responseUsage)},
		{"reasoning in the snapshot", completed(`[{"type":"reasoning","summary":[]}]`, responseUsage)},
		{"unfinished tool", sse(`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","call_id":"call_1","name":"read_file","arguments":"","status":"in_progress"}}`) + completed("[]", responseUsage)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv, _ := fakeProvider(t, http.StatusOK, tt.body)
			resp, err := newTestChatGPT(t, srv, nil).Step(t.Context(), StepRequest{Model: "gpt-x"})
			if !errors.Is(err, ErrUnavailable) || !Transient(err) {
				t.Fatalf("Step error = %v, want a transient unavailable response", err)
			}
			if resp.Text != "" || len(resp.ToolCalls) != 0 || resp.Model != "gpt-x" || !resp.ChatGPTPlan ||
				resp.Usage != (Usage{Input: 200, CacheRead: 500, Output: 59}) {
				t.Fatalf("failed response = %+v", resp)
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
