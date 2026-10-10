package agent

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	"github.com/home-operations/kritika/internal/model"
)

// TestRunKeepsItsConversation: a submitted Run hands back its exchange as
// the last step sent it, with the model's answer to that step last, and
// it encodes and decodes to itself, a malformed tool input included.
func TestRunKeepsItsConversation(t *testing.T) {
	submit := toolCall("2", "submit_review", validSubmitInput)
	output := []json.RawMessage{json.RawMessage(`{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"opaque"}`)}
	st := &scriptedStepper{steps: []model.StepResponse{
		{ToolCalls: []model.ToolCall{toolCall("1", "grep", `{"pattern":`)}, ChatGPTOutput: output},
		{Text: "Done.", ToolCalls: []model.ToolCall{submit}, ChatGPTOutput: output, Usage: model.Usage{Input: 100, CacheRead: 900, Output: 50}},
	}}
	res := Run{
		Stepper: st, Model: "m", System: "Review it.", User: "The diff.", Tools: []Tool{&fakeTool{name: "grep", output: "hit"}},
		Submit: testSubmitDef,
	}.Do(t.Context())
	c := res.Conversation
	if res.Stop != StopSubmitted || c == nil {
		t.Fatalf("stop = %s, conversation = %v", res.Stop, c)
	}
	last := st.calls[len(st.calls)-1]
	want := append(slices.Clone(last.Messages), model.Message{Role: model.RoleAssistant, Text: "Done.", ToolCalls: []model.ToolCall{submit}, ChatGPTOutput: output})
	if c.System != "Review it." || !reflect.DeepEqual(c.Tools, last.Tools) || !reflect.DeepEqual(c.Messages, want) || c.Tokens != 1050 {
		t.Fatalf("conversation = %+v", c)
	}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var back Conversation
	if err := json.Unmarshal(b, &back); err != nil || !reflect.DeepEqual(back, *c) {
		t.Fatalf("decoded = %+v, %v; want %+v", back, err, *c)
	}
	if string(back.Messages[1].ToolCalls[0].Input) != `{"pattern":` {
		t.Fatalf("malformed input = %s", back.Messages[1].ToolCalls[0].Input)
	}
	if !reflect.DeepEqual(back.Messages[1].ChatGPTOutput, output) {
		t.Fatalf("replay output = %s", back.Messages[1].ChatGPTOutput)
	}

	unsubmitted := Run{Stepper: &scriptedStepper{}, Model: "m", User: "u", Submit: testSubmitDef}.Do(t.Context())
	if unsubmitted.Conversation != nil {
		t.Fatalf("a run that did not submit kept %+v", unsubmitted.Conversation)
	}
}

// TestRunCarriesOnAConversation: a Run carrying on a conversation sends it
// whole, then a turn answering each call of its last answer before the new
// text, and keeps the longer conversation in turn.
func TestRunCarriesOnAConversation(t *testing.T) {
	grep := &fakeTool{name: "grep", output: "hit"}
	output := []json.RawMessage{json.RawMessage(`{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"opaque"}`)}
	carried := &Conversation{
		System: "Review it.", Tools: Run{Tools: []Tool{grep}, Submit: testSubmitDef}.ToolDefs(), Tokens: 500,
		Messages: []model.Message{
			{Role: model.RoleUser, Text: "The diff."},
			{Role: model.RoleAssistant, ChatGPTOutput: output, ToolCalls: []model.ToolCall{toolCall("1", "grep", `{}`), toolCall("2", "submit_review", validSubmitInput)}},
		},
	}
	st := &scriptedStepper{steps: []model.StepResponse{{ToolCalls: []model.ToolCall{toolCall("3", "submit_review", validSubmitInput)}}}}
	run := Run{Stepper: st, Model: "m", System: "Review it.", User: "The head moved.", Tools: []Tool{grep}, Submit: testSubmitDef, Carried: carried}
	if !run.Carries(carried) {
		t.Fatal("a run offering the conversation's system prompt and tools does not carry it")
	}
	res := run.Do(t.Context())
	if res.Stop != StopSubmitted || len(st.calls) != 1 {
		t.Fatalf("stop = %s after %d steps", res.Stop, len(st.calls))
	}
	sent := st.calls[0].Messages
	turn := model.Message{Role: model.RoleUser, Text: "The head moved.", ToolResults: []model.ToolResult{
		{CallID: "1", IsError: true, Content: carriedNotRun}, {CallID: "2", Content: carriedSubmitted},
	}}
	if !reflect.DeepEqual(sent, append(slices.Clone(carried.Messages), turn)) {
		t.Fatalf("first step sent %+v", sent)
	}
	if got := res.Conversation.Messages; len(got) != 4 || got[3].ToolCalls[0].ID != "3" {
		t.Fatalf("kept %+v", got)
	}
	if len(carried.Messages) != 2 {
		t.Fatalf("the carried conversation was changed: %+v", carried.Messages)
	}

	for name, other := range map[string]Run{
		"another system prompt": {System: "Review it again.", Tools: []Tool{grep}, Submit: testSubmitDef},
		"another tool":          {System: "Review it.", Tools: []Tool{grep, &fakeTool{name: "read_file"}}, Submit: testSubmitDef},
		"another submit schema": {System: "Review it.", Tools: []Tool{grep}, Submit: model.ToolDef{Name: "submit_review", InputSchema: json.RawMessage(`{}`)}},
	} {
		if other.Carries(carried) {
			t.Errorf("a run with %s carries the conversation", name)
		}
	}
	if run.Carries(nil) {
		t.Error("a run carries no conversation")
	}
}

func TestRunRetainsChatGPTOutput(t *testing.T) {
	output := []json.RawMessage{json.RawMessage(`{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"opaque"}`)}
	for _, tt := range []struct {
		name   string
		first  model.StepResponse
		limits Limits
	}{
		{"tool result", model.StepResponse{ToolCalls: []model.ToolCall{toolCall("1", "unknown", `{}`)}}, Limits{}},
		{"nudge", model.StepResponse{Text: "looking"}, Limits{}},
		{"empty answer", model.StepResponse{}, Limits{}},
		{"output cap", model.StepResponse{Text: "looking", Stop: model.StopMaxTokens}, Limits{}},
		{"forced submission retry", model.StepResponse{Text: "looking"}, Limits{MaxSteps: 1}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tt.first.ChatGPTOutput = output
			st := &scriptedStepper{steps: []model.StepResponse{tt.first, {ToolCalls: []model.ToolCall{toolCall("2", "submit_review", validSubmitInput)}}}}
			res := Run{Stepper: st, Model: "m", User: "review", Submit: testSubmitDef, Limits: tt.limits}.Do(t.Context())
			if res.Stop != StopSubmitted || len(st.calls) != 2 {
				t.Fatalf("stop = %s after %d calls", res.Stop, len(st.calls))
			}
			if got := st.calls[1].Messages[1].ChatGPTOutput; !reflect.DeepEqual(got, output) {
				t.Fatalf("replay output = %s, want %s", got, output)
			}
		})
	}
}
