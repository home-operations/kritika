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
	st := &scriptedStepper{steps: []model.StepResponse{
		{ToolCalls: []model.ToolCall{toolCall("1", "grep", `{"pattern":`)}},
		{Text: "Done.", ToolCalls: []model.ToolCall{submit}, Usage: model.Usage{Input: 100, CacheRead: 900, Output: 50}},
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
	want := append(slices.Clone(last.Messages), model.Message{Role: model.RoleAssistant, Text: "Done.", ToolCalls: []model.ToolCall{submit}})
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

	unsubmitted := Run{Stepper: &scriptedStepper{}, Model: "m", User: "u", Submit: testSubmitDef}.Do(t.Context())
	if unsubmitted.Conversation != nil {
		t.Fatalf("a run that did not submit kept %+v", unsubmitted.Conversation)
	}
}
