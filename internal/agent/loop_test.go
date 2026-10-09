package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/home-operations/kritika/internal/model"
)

// fakeTool is a Tool whose Run returns a fixed output or error, for
// exercising the loop without depending on tools.go/tree.go.
type fakeTool struct {
	name   string
	output string
	err    error
}

func (f *fakeTool) Def() model.ToolDef {
	return model.ToolDef{Name: f.name, Description: "fake tool", InputSchema: json.RawMessage(`{"type":"object"}`)}
}

func (f *fakeTool) Run(context.Context, json.RawMessage) (string, error) {
	return f.output, f.err
}

// scriptedStepper replays a fixed sequence of responses, recording every
// request it was called with. Calling it past the end of the script is a
// test bug, not a loop bug, so it errors rather than panicking.
type scriptedStepper struct {
	steps []model.StepResponse
	calls []model.StepRequest
}

// Step records req with its messages cloned: the loop rewrites its own
// slice in place as it goes, and a recorded request must stay as sent.
func (s *scriptedStepper) Step(_ context.Context, req model.StepRequest) (model.StepResponse, error) {
	req.Messages = slices.Clone(req.Messages)
	s.calls = append(s.calls, req)
	if len(s.calls) > len(s.steps) {
		return model.StepResponse{}, fmt.Errorf("scriptedStepper: no script for call %d", len(s.calls))
	}
	return s.steps[len(s.calls)-1], nil
}

// ctxCancelStepper cancels its own ctx, then returns ctx.Err(), simulating a
// Stepper that observes and reports the Run's own cancellation rather than
// an unrelated failure.
type ctxCancelStepper struct {
	cancel context.CancelFunc
}

func (s *ctxCancelStepper) Step(ctx context.Context, _ model.StepRequest) (model.StepResponse, error) {
	s.cancel()
	return model.StepResponse{}, ctx.Err()
}

// ctxDeadlineStepper blocks until ctx ends, then returns ctx.Err(),
// simulating a Stepper whose call was still in flight when a job deadline
// arrived as context.DeadlineExceeded.
type ctxDeadlineStepper struct{}

func (ctxDeadlineStepper) Step(ctx context.Context, _ model.StepRequest) (model.StepResponse, error) {
	<-ctx.Done()
	return model.StepResponse{}, ctx.Err()
}

var testSubmitDef = model.ToolDef{
	Name:        "submit_review",
	Description: "submit the review",
	InputSchema: json.RawMessage(`{"type":"object"}`),
}

const validSubmitInput = `{"verdict":"approve"}`

func toolCall(id, name, input string) model.ToolCall {
	return model.ToolCall{ID: id, Name: name, Input: json.RawMessage(input)}
}

// outputBytes is the tool output e's calls produced, in all.
func outputBytes(e StepEvent) int {
	n := 0
	for _, c := range e.Calls {
		n += c.OutputBytes
	}
	return n
}

// runCase is one TestRun table row. setup builds this case's Stepper and
// context (a nil context means t.Context()); scripted is the same value
// again when the Stepper is a *scriptedStepper, so check can inspect its
// recorded calls, and nil for the ctx-focused cases that use a different
// Stepper double.
//
// setup and check are named functions rather than inline closures so each
// case's assertions are counted, by tooling such as gocyclo, against that
// function alone rather than against TestRun as a whole.
type runCase struct {
	name      string
	tools     []Tool
	limits    Limits
	validate  func(json.RawMessage) error
	fallback  func(json.RawMessage) bool
	setup     func(t *testing.T) (stepper model.Stepper, ctx context.Context, scripted *scriptedStepper)
	wantStop  StopReason
	wantSteps int
	check     func(t *testing.T, result Result, events []StepEvent, scripted *scriptedStepper)
}

func setupGrepReadSubmit(t *testing.T) (model.Stepper, context.Context, *scriptedStepper) {
	st := &scriptedStepper{steps: []model.StepResponse{
		{ToolCalls: []model.ToolCall{toolCall("1", "grep", `{"pattern":"x"}`)}, Model: "acme/large"},
		{ToolCalls: []model.ToolCall{toolCall("2", "read_file", `{"path":"widget.go"}`)}, Model: "acme/large"},
		// The provider fell back for the last step.
		{ToolCalls: []model.ToolCall{toolCall("3", "submit_review", validSubmitInput)}, Model: "acme/small"},
	}}
	return st, nil, st
}

func checkGrepReadSubmit(t *testing.T, result Result, _ []StepEvent, _ *scriptedStepper) {
	if string(result.Submitted) != validSubmitInput {
		t.Fatalf("Submitted = %s, want %s", result.Submitted, validSubmitInput)
	}
	if result.Model != "acme/small" {
		t.Fatalf("Model = %q, want the model that answered the last step", result.Model)
	}
	want := map[string]int{"grep": 1, "read_file": 1, "submit_review": 1}
	if len(result.ToolCalls) != len(want) {
		t.Fatalf("ToolCalls = %v, want %v", result.ToolCalls, want)
	}
	for name, n := range want {
		if result.ToolCalls[name] != n {
			t.Fatalf("ToolCalls[%q] = %d, want %d (full: %v)", name, result.ToolCalls[name], n, result.ToolCalls)
		}
	}
}

func setupUnknownToolThenSubmit(t *testing.T) (model.Stepper, context.Context, *scriptedStepper) {
	st := &scriptedStepper{steps: []model.StepResponse{
		{ToolCalls: []model.ToolCall{toolCall("1", "does_not_exist", `{}`)}},
		{ToolCalls: []model.ToolCall{toolCall("2", "submit_review", validSubmitInput)}},
	}}
	return st, nil, st
}

func checkUnknownToolThenSubmit(t *testing.T, result Result, _ []StepEvent, scripted *scriptedStepper) {
	if result.ToolCalls["does_not_exist"] != 1 {
		t.Fatalf("ToolCalls = %v, want does_not_exist:1", result.ToolCalls)
	}
	// The unknown-tool step's error result must have reached the model as
	// the next turn's input, not silently vanished.
	last := scripted.calls[len(scripted.calls)-1]
	lastMsg := last.Messages[len(last.Messages)-1]
	if len(lastMsg.ToolResults) != 1 || !lastMsg.ToolResults[0].IsError {
		t.Fatalf("expected an error ToolResult for the unknown tool, got %+v", lastMsg.ToolResults)
	}
}

func setupInvalidSubmitJSONThenValid(t *testing.T) (model.Stepper, context.Context, *scriptedStepper) {
	st := &scriptedStepper{steps: []model.StepResponse{
		{ToolCalls: []model.ToolCall{toolCall("1", "submit_review", `{not json`)}},
		{ToolCalls: []model.ToolCall{toolCall("2", "submit_review", validSubmitInput)}},
	}}
	return st, nil, st
}

func checkInvalidSubmitJSONThenValid(t *testing.T, result Result, _ []StepEvent, _ *scriptedStepper) {
	if string(result.Submitted) != validSubmitInput {
		t.Fatalf("Submitted = %s, want %s", result.Submitted, validSubmitInput)
	}
}

// rejectVerdict is a Validate that refuses the submit inputs without a
// "verdict" key, with a message the model is expected to see.
func rejectVerdict(input json.RawMessage) error {
	if !strings.Contains(string(input), "verdict") {
		return errors.New("verdict is required")
	}
	return nil
}

func setupRejectedSubmitThenValid(t *testing.T) (model.Stepper, context.Context, *scriptedStepper) {
	st := &scriptedStepper{steps: []model.StepResponse{
		{ToolCalls: []model.ToolCall{toolCall("1", "submit_review", `{"wrong":"shape"}`)}},
		{ToolCalls: []model.ToolCall{toolCall("2", "submit_review", validSubmitInput)}},
	}}
	return st, nil, st
}

func checkRejectedSubmitThenValid(t *testing.T, result Result, _ []StepEvent, scripted *scriptedStepper) {
	if string(result.Submitted) != validSubmitInput {
		t.Fatalf("Submitted = %s, want %s", result.Submitted, validSubmitInput)
	}
	last := scripted.calls[len(scripted.calls)-1]
	lastMsg := last.Messages[len(last.Messages)-1]
	if len(lastMsg.ToolResults) != 1 || !lastMsg.ToolResults[0].IsError {
		t.Fatalf("expected an error ToolResult for the rejected submit, got %+v", lastMsg.ToolResults)
	}
	if got := lastMsg.ToolResults[0].Content; !strings.Contains(got, "verdict is required") {
		t.Fatalf("ToolResult = %q, want the validator's message", got)
	}
}

func setupForcedRejectedSubmitRetried(t *testing.T) (model.Stepper, context.Context, *scriptedStepper) {
	st := &scriptedStepper{steps: []model.StepResponse{
		{ToolCalls: []model.ToolCall{toolCall("1", "submit_review", `{"wrong":"shape"}`)}},
		{ToolCalls: []model.ToolCall{toolCall("2", "submit_review", validSubmitInput)}},
	}}
	return st, nil, st
}

func checkForcedRejectedSubmitRetried(t *testing.T, result Result, _ []StepEvent, scripted *scriptedStepper) {
	if string(result.Submitted) != validSubmitInput {
		t.Fatalf("Submitted = %s, want %s", result.Submitted, validSubmitInput)
	}
	// The retry carries the validator's message and is told to submit
	// again.
	last := scripted.calls[1]
	results := last.Messages[len(last.Messages)-1].ToolResults
	if len(results) != 1 || !results[0].IsError || !strings.Contains(results[0].Content, "verdict is required") || !toldToSubmit(last) {
		t.Fatalf("retry request = %+v, want the rejection and the submit instruction", last.Messages)
	}
}

// holdFallback is a Fallback that holds a refused input with a "fallback"
// key to be an answer.
func holdFallback(input json.RawMessage) bool { return strings.Contains(string(input), "fallback") }

const fallbackSubmitInput = `{"fallback":"x"}`

// setupForcedFallbackTaken submits an input rejectVerdict refuses and
// holdFallback holds, then one it does not hold, then prose, so the forced
// retries run out.
func setupForcedFallbackTaken(t *testing.T) (model.Stepper, context.Context, *scriptedStepper) {
	st := &scriptedStepper{steps: []model.StepResponse{
		{ToolCalls: []model.ToolCall{toolCall("1", "submit_review", fallbackSubmitInput)}},
		{ToolCalls: []model.ToolCall{toolCall("2", "submit_review", `{"wrong":"shape"}`)}},
		{Text: "done"},
	}}
	return st, nil, st
}

func checkForcedFallbackTaken(t *testing.T, result Result, _ []StepEvent, _ *scriptedStepper) {
	if string(result.Submitted) != fallbackSubmitInput {
		t.Fatalf("Submitted = %s, want the held input %s", result.Submitted, fallbackSubmitInput)
	}
	if !strings.Contains(result.Refused, "verdict is required") || result.Err != "" {
		t.Fatalf("Refused = %q, Err = %q; want the held input's refusal and no error", result.Refused, result.Err)
	}
	// The conversation ends with the answer that carried the held input,
	// as a carried conversation expects.
	c := result.Conversation
	if c == nil {
		t.Fatal("Conversation = nil")
	}
	last := c.Messages[len(c.Messages)-1]
	if last.Role != model.RoleAssistant || len(last.ToolCalls) != 1 || last.ToolCalls[0].ID != "1" {
		t.Fatalf("last message = %+v, want the answer that carried the held input", last)
	}
}

// setupForcedNothingHeld submits only inputs holdFallback does not hold.
func setupForcedNothingHeld(t *testing.T) (model.Stepper, context.Context, *scriptedStepper) {
	st := &scriptedStepper{steps: []model.StepResponse{
		{ToolCalls: []model.ToolCall{toolCall("1", "submit_review", `{"wrong":"shape"}`)}},
		{ToolCalls: []model.ToolCall{toolCall("2", "submit_review", `{"wrong":"shape"}`)}},
		{ToolCalls: []model.ToolCall{toolCall("3", "submit_review", `{"wrong":"shape"}`)}},
	}}
	return st, nil, st
}

func checkNothingSubmitted(t *testing.T, result Result, _ []StepEvent, _ *scriptedStepper) {
	if result.Submitted != nil || result.Refused != "" {
		t.Fatalf("Submitted = %s, Refused = %q; want neither", result.Submitted, result.Refused)
	}
}

// setupStepperErrorAfterHeldInput submits a held input, then the script
// runs out and the stepper errors.
func setupStepperErrorAfterHeldInput(t *testing.T) (model.Stepper, context.Context, *scriptedStepper) {
	st := &scriptedStepper{steps: []model.StepResponse{
		{ToolCalls: []model.ToolCall{toolCall("1", "submit_review", fallbackSubmitInput)}},
	}}
	return st, nil, st
}

func checkFallbackSubmitted(t *testing.T, result Result, _ []StepEvent, _ *scriptedStepper) {
	if string(result.Submitted) != fallbackSubmitInput || result.Refused == "" {
		t.Fatalf("Submitted = %s, Refused = %q; want the held input and its refusal", result.Submitted, result.Refused)
	}
}

// cancelAfterScriptStepper answers from its script, then cancels the Run's
// ctx and reports it.
type cancelAfterScriptStepper struct {
	scriptedStepper
	cancel context.CancelFunc
}

func (s *cancelAfterScriptStepper) Step(ctx context.Context, req model.StepRequest) (model.StepResponse, error) {
	if len(s.calls) == len(s.steps) {
		s.cancel()
		return model.StepResponse{}, ctx.Err()
	}
	return s.scriptedStepper.Step(ctx, req)
}

func setupCanceledAfterHeldInput(t *testing.T) (model.Stepper, context.Context, *scriptedStepper) {
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	st := &cancelAfterScriptStepper{cancel: cancel, steps: []model.StepResponse{
		{ToolCalls: []model.ToolCall{toolCall("1", "submit_review", fallbackSubmitInput)}},
	}}
	return st, ctx, &st.scriptedStepper
}

// setupForcedNeverSubmits answers every forced step with an invalid
// submission, prose, or a tool call, so the retries run out.
func setupForcedNeverSubmits(t *testing.T) (model.Stepper, context.Context, *scriptedStepper) {
	st := &scriptedStepper{steps: []model.StepResponse{
		{ToolCalls: []model.ToolCall{toolCall("1", "submit_review", `{not json`)}},
		{Text: "let me look once more"},
		{ToolCalls: []model.ToolCall{toolCall("3", "noop", `{}`)}},
	}}
	return st, nil, st
}

func checkForcedNeverSubmits(t *testing.T, result Result, events []StepEvent, scripted *scriptedStepper) {
	if result.Submitted != nil {
		t.Fatalf("Submitted = %s, want nil", result.Submitted)
	}
	if len(scripted.calls) != 1+forcedRetries {
		t.Fatalf("%d calls to the stepper, want the forced step and %d retries", len(scripted.calls), forcedRetries)
	}
	for i, call := range scripted.calls {
		if !toldToSubmit(call) {
			t.Fatalf("request %d = %+v, want the submit instruction", i, call.Messages)
		}
	}
	// The tool the model called instead of submitting was refused, not run.
	if results := scripted.calls[2].Messages[len(scripted.calls[2].Messages)-1].ToolResults; len(results) != 0 {
		t.Fatalf("the prose step's retry carries tool results: %+v", results)
	}
	if outputBytes(events[2]) != 0 || !strings.Contains(result.Err, "3 step(s)") {
		t.Fatalf("event = %+v, err = %q; want the tool refused and the forced steps counted", events[2], result.Err)
	}
}

func setupForcedToolCallRefused(t *testing.T) (model.Stepper, context.Context, *scriptedStepper) {
	st := &scriptedStepper{steps: []model.StepResponse{
		{ToolCalls: []model.ToolCall{toolCall("1", "noop", `{}`)}},
		{ToolCalls: []model.ToolCall{toolCall("2", "submit_review", validSubmitInput)}},
	}}
	return st, nil, st
}

func checkForcedToolCallRefused(t *testing.T, result Result, events []StepEvent, scripted *scriptedStepper) {
	if string(result.Submitted) != validSubmitInput {
		t.Fatalf("Submitted = %s, want %s", result.Submitted, validSubmitInput)
	}
	last := scripted.calls[1]
	results := last.Messages[len(last.Messages)-1].ToolResults
	if len(results) != 1 || !results[0].IsError || results[0].Content != (Run{Submit: testSubmitDef}).onlySubmitText() || outputBytes(events[0]) != 0 {
		t.Fatalf("retry request = %+v, events = %+v; want the tool refused unrun", last.Messages, events)
	}
}

func setupTextOnlyTwiceStopsNoSubmit(t *testing.T) (model.Stepper, context.Context, *scriptedStepper) {
	st := &scriptedStepper{steps: []model.StepResponse{
		{Text: "thinking..."},
		{Text: "still thinking..."},
	}}
	return st, nil, st
}

func checkTextOnlyTwiceStopsNoSubmit(t *testing.T, _ Result, events []StepEvent, scripted *scriptedStepper) {
	if len(events) != 2 || events[0].Index != 0 || events[1].Index != 1 {
		t.Fatalf("events = %+v", events)
	}
	// The nudge must have been sent as a user message after the first
	// text-only turn, or the second turn is not really giving the model a
	// chance to submit.
	last := scripted.calls[len(scripted.calls)-1]
	found := false
	for _, m := range last.Messages {
		if m.Role == model.RoleUser && m.Text == (Run{Submit: testSubmitDef}).nudgeText() {
			found = true
		}
	}
	if !found {
		t.Fatalf("nudge message %q not found in %+v", (Run{Submit: testSubmitDef}).nudgeText(), last.Messages)
	}
}

func setupNudgeOncePerRunNotResetByToolTurn(t *testing.T) (model.Stepper, context.Context, *scriptedStepper) {
	st := &scriptedStepper{steps: []model.StepResponse{
		{Text: "thinking..."},
		{ToolCalls: []model.ToolCall{toolCall("1", "noop", `{}`)}},
		{Text: "still thinking..."},
	}}
	return st, nil, st
}

func setupBudgetTellsToSubmit(t *testing.T) (model.Stepper, context.Context, *scriptedStepper) {
	st := &scriptedStepper{steps: []model.StepResponse{
		{ToolCalls: []model.ToolCall{toolCall("1", "noop", `{}`)}, Usage: model.Usage{Input: 80, Output: 10}},
		{ToolCalls: []model.ToolCall{toolCall("2", "submit_review", validSubmitInput)}},
	}}
	return st, nil, st
}

func checkBudgetTellsToSubmit(t *testing.T, _ Result, _ []StepEvent, scripted *scriptedStepper) {
	if len(scripted.calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(scripted.calls))
	}
	// The step is told to submit in its last user message, with the tool
	// choice left to the model, never forced.
	if !toldToSubmit(scripted.calls[1]) || scripted.calls[1].ToolChoice != (model.ToolChoice{}) {
		t.Fatalf("second request = %+v, want the submit instruction in its last user message and no tool choice", scripted.calls[1])
	}
	// The first request must NOT have been told: the budget only crosses
	// the 90% threshold after step 0's usage lands.
	if toldToSubmit(scripted.calls[0]) {
		t.Fatalf("first request was told to submit: %+v", scripted.calls[0].Messages)
	}
}

// toldToSubmit reports whether req's last message is a user message that
// ends with the submit instruction.
func toldToSubmit(req model.StepRequest) bool {
	last := req.Messages[len(req.Messages)-1]
	return last.Role == model.RoleUser && strings.HasSuffix(last.Text, (Run{Submit: testSubmitDef}).submitNowText())
}

// setupBudgetForcedNeverSubmits crosses the budget's 90% mark on the first
// step, then never submits while staying under the budget itself.
func setupBudgetForcedNeverSubmits(t *testing.T) (model.Stepper, context.Context, *scriptedStepper) {
	st := &scriptedStepper{steps: []model.StepResponse{
		{ToolCalls: []model.ToolCall{toolCall("1", "noop", `{}`)}, Usage: model.Usage{Input: 80, Output: 10}},
		{ToolCalls: []model.ToolCall{toolCall("2", "noop", `{}`)}},
		{ToolCalls: []model.ToolCall{toolCall("3", "noop", `{}`)}},
		{ToolCalls: []model.ToolCall{toolCall("4", "noop", `{}`)}},
	}}
	return st, nil, st
}

func checkBudgetForcedNeverSubmits(t *testing.T, result Result, _ []StepEvent, scripted *scriptedStepper) {
	if len(scripted.calls) != 2+forcedRetries || !strings.Contains(result.Err, "3 step(s)") {
		t.Fatalf("%d calls, err %q; want a free step, a forced one and %d retries", len(scripted.calls), result.Err, forcedRetries)
	}
}

func setupTruncatedSubmitStops(t *testing.T) (model.Stepper, context.Context, *scriptedStepper) {
	st := &scriptedStepper{steps: []model.StepResponse{
		{ToolCalls: []model.ToolCall{toolCall("1", "submit_review", `{"verdict":"app`)}, Stop: model.StopMaxTokens},
		{ToolCalls: []model.ToolCall{toolCall("2", "submit_review", validSubmitInput)}},
	}}
	return st, nil, st
}

func checkTruncatedSubmitStops(t *testing.T, result Result, _ []StepEvent, scripted *scriptedStepper) {
	if len(scripted.calls) != 1 {
		t.Fatalf("expected the loop to stop without retrying, got %d calls", len(scripted.calls))
	}
	if !strings.Contains(result.Err, "cut off") || !strings.Contains(result.Err, "16384") {
		t.Fatalf("Err = %q, want the cap the submission was cut off at", result.Err)
	}
}

// lastUserText is the text of req's last message, "" when it is not a
// user message.
func lastUserText(req model.StepRequest) string {
	last := req.Messages[len(req.Messages)-1]
	if last.Role != model.RoleUser {
		return ""
	}
	return last.Text
}

func setupCutOffStepGoesOn(t *testing.T) (model.Stepper, context.Context, *scriptedStepper) {
	st := &scriptedStepper{steps: []model.StepResponse{
		{Stop: model.StopMaxTokens},
		{Text: "still thinking..."},
		{ToolCalls: []model.ToolCall{toolCall("1", "submit_review", validSubmitInput)}},
	}}
	return st, nil, st
}

func checkCutOffStepGoesOn(t *testing.T, _ Result, _ []StepEvent, scripted *scriptedStepper) {
	run := Run{Submit: testSubmitDef}
	if got, want := lastUserText(scripted.calls[1]), run.cutOffText(DefaultLimits.MaxOutputTokensPerStep); got != want {
		t.Fatalf("after the cut-off step the model was told %q, want %q", got, want)
	}
	// The cut-off step left the nudge for a turn that answered in prose.
	if got := lastUserText(scripted.calls[2]); got != run.nudgeText() {
		t.Fatalf("after the prose turn the model was told %q, want the nudge", got)
	}
}

// setupCutOffStepsInARow is cut off twice, calls a tool, which starts the
// count again, then is cut off until the run ends.
func setupCutOffStepsInARow(t *testing.T) (model.Stepper, context.Context, *scriptedStepper) {
	cut := model.StepResponse{Stop: model.StopMaxTokens}
	st := &scriptedStepper{steps: []model.StepResponse{
		cut, cut,
		{ToolCalls: []model.ToolCall{toolCall("1", "noop", `{}`)}},
		cut, cut, cut, cut,
	}}
	return st, nil, st
}

// setupCutOffAfterProse is cut off twice, answers in prose, which starts
// the count again, then is cut off before it submits.
func setupCutOffAfterProse(t *testing.T) (model.Stepper, context.Context, *scriptedStepper) {
	cut := model.StepResponse{Stop: model.StopMaxTokens}
	st := &scriptedStepper{steps: []model.StepResponse{
		cut, cut,
		{Text: "still thinking..."},
		cut,
		{ToolCalls: []model.ToolCall{toolCall("1", "submit_review", validSubmitInput)}},
	}}
	return st, nil, st
}

func checkCutOffAfterProse(t *testing.T, _ Result, _ []StepEvent, scripted *scriptedStepper) {
	run := Run{Submit: testSubmitDef}
	goOn := run.cutOffText(DefaultLimits.MaxOutputTokensPerStep)
	want := []string{"", goOn, goOn, run.nudgeText(), goOn}
	for i, call := range scripted.calls {
		if got := lastUserText(call); got != want[i] {
			t.Fatalf("request %d ends with %q, want %q", i, got, want[i])
		}
	}
}

func checkCutOffStepsInARow(t *testing.T, _ Result, _ []StepEvent, scripted *scriptedStepper) {
	run := Run{Submit: testSubmitDef}
	goOn := run.cutOffText(DefaultLimits.MaxOutputTokensPerStep)
	// The opening message and the tool's results carry no text.
	want := []string{"", goOn, goOn, "", goOn, goOn, run.nudgeText()}
	for i, call := range scripted.calls {
		if got := lastUserText(call); got != want[i] {
			t.Fatalf("request %d ends with %q, want %q", i, got, want[i])
		}
	}
}

func setupBudgetExhaustedStopsImmediately(t *testing.T) (model.Stepper, context.Context, *scriptedStepper) {
	st := &scriptedStepper{steps: []model.StepResponse{
		{ToolCalls: []model.ToolCall{toolCall("1", "noop", `{}`)}, Usage: model.Usage{Input: 60}},
		{ToolCalls: []model.ToolCall{toolCall("2", "submit_review", validSubmitInput)}},
	}}
	return st, nil, st
}

func checkBudgetExhaustedStopsImmediately(t *testing.T, _ Result, _ []StepEvent, scripted *scriptedStepper) {
	if len(scripted.calls) != 1 {
		t.Fatalf("expected the loop to stop before a second call, got %d calls", len(scripted.calls))
	}
}

func setupMaxStepsReachedWithoutSubmit(t *testing.T) (model.Stepper, context.Context, *scriptedStepper) {
	st := &scriptedStepper{steps: []model.StepResponse{
		{ToolCalls: []model.ToolCall{toolCall("1", "noop", `{}`)}},
		{ToolCalls: []model.ToolCall{toolCall("2", "noop", `{}`)}},
		{ToolCalls: []model.ToolCall{toolCall("3", "noop", `{}`)}},
		{ToolCalls: []model.ToolCall{toolCall("4", "noop", `{}`)}},
	}}
	return st, nil, st
}

func checkMaxStepsReachedWithoutSubmit(t *testing.T, result Result, _ []StepEvent, scripted *scriptedStepper) {
	// The last step and each retry must have been told to submit, even
	// though the model chose not to; the first, free, step must not.
	if toldToSubmit(scripted.calls[0]) {
		t.Fatalf("first request was told to submit: %+v", scripted.calls[0].Messages)
	}
	for i, call := range scripted.calls[1:] {
		if !toldToSubmit(call) {
			t.Fatalf("request %d = %+v, want the submit instruction in its last user message", i+1, call.Messages)
		}
	}
	if !strings.Contains(result.Err, "3 step(s)") {
		t.Fatalf("Err = %q, want the forced steps counted", result.Err)
	}
}

func setupContextCanceledBeforeFirstStep(t *testing.T) (model.Stepper, context.Context, *scriptedStepper) {
	st := &scriptedStepper{}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	return st, ctx, st
}

func checkContextCanceledBeforeFirstStep(t *testing.T, _ Result, _ []StepEvent, scripted *scriptedStepper) {
	if len(scripted.calls) != 0 {
		t.Fatalf("stepper was called %d times, want 0", len(scripted.calls))
	}
}

func setupStepperError(t *testing.T) (model.Stepper, context.Context, *scriptedStepper) {
	st := &scriptedStepper{}
	return st, nil, st
}

func checkStepperError(t *testing.T, result Result, _ []StepEvent, _ *scriptedStepper) {
	if result.Err == "" {
		t.Fatal("Err is empty, want the stepper's error message")
	}
}

// budgetStepper refuses like the gateway once a run's budget is spent.
type budgetStepper struct{}

func (budgetStepper) Step(context.Context, model.StepRequest) (model.StepResponse, error) {
	return model.StepResponse{}, fmt.Errorf("model: review: %w: run budget spent", model.ErrBudget)
}

func setupGatewayBudgetRefusal(t *testing.T) (model.Stepper, context.Context, *scriptedStepper) {
	return budgetStepper{}, nil, nil
}

func checkGatewayBudgetRefusal(t *testing.T, result Result, _ []StepEvent, _ *scriptedStepper) {
	if !strings.Contains(result.Err, "run budget spent") {
		t.Fatalf("Err = %q, want the gateway's refusal", result.Err)
	}
}

func setupStepperErrorWhileCtxCanceled(t *testing.T) (model.Stepper, context.Context, *scriptedStepper) {
	ctx, cancel := context.WithCancel(t.Context())
	return &ctxCancelStepper{cancel: cancel}, ctx, nil
}

func setupStepperErrorOnCtxDeadlineExceeded(t *testing.T) (model.Stepper, context.Context, *scriptedStepper) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	t.Cleanup(cancel)
	return ctxDeadlineStepper{}, ctx, nil
}

// checkCanceledRunHasNoErr is shared by both ctx-cancellation cases: a Run
// that ends StopCanceled must never also carry a Stepper error message.
func checkCanceledRunHasNoErr(t *testing.T, result Result, _ []StepEvent, _ *scriptedStepper) {
	if result.Err != "" {
		t.Fatalf("Err = %q, want empty for a canceled run", result.Err)
	}
}

func setupUsageSummedAcrossSteps(t *testing.T) (model.Stepper, context.Context, *scriptedStepper) {
	st := &scriptedStepper{steps: []model.StepResponse{
		{ToolCalls: []model.ToolCall{toolCall("1", "noop", `{}`)}, Usage: model.Usage{Input: 100, Output: 10}, CostUSD: 0.01},
		{ToolCalls: []model.ToolCall{toolCall("2", "submit_review", validSubmitInput)}, Usage: model.Usage{Input: 50, Output: 5}, CostUSD: 0.02},
	}}
	return st, nil, st
}

func checkUsageSummedAcrossSteps(t *testing.T, result Result, _ []StepEvent, _ *scriptedStepper) {
	want := model.Usage{Input: 150, Output: 15}
	if result.Usage != want {
		t.Fatalf("Usage = %+v, want %+v", result.Usage, want)
	}
	if result.CostUSD != 0.03 {
		t.Fatalf("CostUSD = %v, want 0.03", result.CostUSD)
	}
}

func setupToolOutputTruncated(t *testing.T) (model.Stepper, context.Context, *scriptedStepper) {
	st := &scriptedStepper{steps: []model.StepResponse{
		{ToolCalls: []model.ToolCall{toolCall("1", "big", `{}`)}},
		{ToolCalls: []model.ToolCall{toolCall("2", "submit_review", validSubmitInput)}},
	}}
	return st, nil, st
}

func checkToolOutputTruncated(t *testing.T, _ Result, _ []StepEvent, scripted *scriptedStepper) {
	last := scripted.calls[len(scripted.calls)-1]
	lastMsg := last.Messages[len(last.Messages)-1]
	if len(lastMsg.ToolResults) != 1 {
		t.Fatalf("ToolResults = %+v, want exactly 1", lastMsg.ToolResults)
	}
	content := lastMsg.ToolResults[0].Content
	kept := strings.LastIndex(content, "\n[truncated ")
	if len(content) > 40 || !strings.HasPrefix(content, "abcdefghij") || kept < 0 ||
		!strings.HasSuffix(content, fmt.Sprintf("\n[truncated %d bytes]", 100-kept)) {
		t.Fatalf("content = %q, want at most 40 bytes: a prefix and a note of the rest", content)
	}
}

func setupSubmitEndsMultiCallTurnEarly(t *testing.T) (model.Stepper, context.Context, *scriptedStepper) {
	st := &scriptedStepper{steps: []model.StepResponse{
		{ToolCalls: []model.ToolCall{
			toolCall("1", "noop", `{}`),
			toolCall("2", "submit_review", validSubmitInput),
			toolCall("3", "noop2", `{}`),
		}},
	}}
	return st, nil, st
}

func checkSubmitEndsMultiCallTurnEarly(t *testing.T, result Result, _ []StepEvent, _ *scriptedStepper) {
	want := map[string]int{"noop": 1, "submit_review": 1}
	if len(result.ToolCalls) != len(want) {
		t.Fatalf("ToolCalls = %v, want %v (noop2 must not be reached)", result.ToolCalls, want)
	}
	for name, n := range want {
		if result.ToolCalls[name] != n {
			t.Fatalf("ToolCalls[%q] = %d, want %d", name, result.ToolCalls[name], n)
		}
	}
}

func setupMultipleToolCallsGetMatchingCallIDs(t *testing.T) (model.Stepper, context.Context, *scriptedStepper) {
	st := &scriptedStepper{steps: []model.StepResponse{
		{ToolCalls: []model.ToolCall{
			toolCall("a1", "alpha", `{}`),
			toolCall("b2", "beta", `{}`),
			toolCall("c3", "gamma", `{}`),
		}},
		{ToolCalls: []model.ToolCall{toolCall("4", "submit_review", validSubmitInput)}},
	}}
	return st, nil, st
}

func checkMultipleToolCallsGetMatchingCallIDs(t *testing.T, _ Result, _ []StepEvent, scripted *scriptedStepper) {
	last := scripted.calls[len(scripted.calls)-1]
	lastMsg := last.Messages[len(last.Messages)-1]
	want := map[string]string{"a1": "alpha-out", "b2": "beta-out", "c3": "gamma-out"}
	if len(lastMsg.ToolResults) != len(want) {
		t.Fatalf("ToolResults = %+v, want %d entries", lastMsg.ToolResults, len(want))
	}
	for _, tr := range lastMsg.ToolResults {
		wantContent, ok := want[tr.CallID]
		if !ok {
			t.Fatalf("unexpected CallID %q in %+v", tr.CallID, lastMsg.ToolResults)
		}
		if tr.Content != wantContent {
			t.Fatalf("ToolResult[%q].Content = %q, want %q", tr.CallID, tr.Content, wantContent)
		}
	}
}

func setupStepEventToolsAndOutputBytesPopulated(t *testing.T) (model.Stepper, context.Context, *scriptedStepper) {
	st := &scriptedStepper{steps: []model.StepResponse{
		{ToolCalls: []model.ToolCall{toolCall("1", "alpha", `{}`), toolCall("2", "beta", `{}`)}},
		{ToolCalls: []model.ToolCall{toolCall("3", "submit_review", validSubmitInput)}},
	}}
	return st, nil, st
}

func checkStepEventToolsAndOutputBytesPopulated(t *testing.T, _ Result, events []StepEvent, _ *scriptedStepper) {
	if len(events) == 0 {
		t.Fatal("events is empty, want at least 1")
	}
	want := []StepCall{{Name: "alpha", OutputBytes: len("aaaaa")}, {Name: "beta", OutputBytes: len("bbb")}}
	if !slices.Equal(events[0].Calls, want) {
		t.Fatalf("Calls = %+v, want %+v", events[0].Calls, want)
	}
}

func setupLargeResultDroppedAfterRead(t *testing.T) (model.Stepper, context.Context, *scriptedStepper) {
	st := &scriptedStepper{steps: []model.StepResponse{
		{ToolCalls: []model.ToolCall{toolCall("1", "big", `{}`), toolCall("2", "small", `{}`)}},
		{ToolCalls: []model.ToolCall{toolCall("3", "big", `{}`)}},
		{ToolCalls: []model.ToolCall{toolCall("4", "submit_review", validSubmitInput)}},
	}}
	return st, nil, st
}

func checkLargeResultDroppedAfterRead(t *testing.T, result Result, _ []StepEvent, scripted *scriptedStepper) {
	big := strings.Repeat("x", dropOutputBytes)
	// The step after the calls read the big result whole.
	if results := scripted.calls[1].Messages[2].ToolResults; results[0].Content != big || results[1].Content != "small" {
		t.Fatalf("step 1's results = %+v, want both whole", results)
	}
	// From the step after that on, the big result is a note and the small
	// one stays; the step's own results are whole, however big.
	dropped := droppedText("big", dropOutputBytes)
	for i, msgs := range [][]model.Message{scripted.calls[2].Messages, result.Conversation.Messages} {
		if results := msgs[2].ToolResults; results[0].Content != dropped || results[1].Content != "small" {
			t.Fatalf("%d: step 0's results = %+v, want the big one dropped", i, results)
		}
		if results := msgs[4].ToolResults; results[0].Content != big {
			t.Fatalf("%d: step 1's result = %q, want it whole", i, results[0].Content)
		}
	}
}

// outageStepper fails transiently outage times, then answers with a
// submit_review.
type outageStepper struct {
	outage, calls int
}

func (s *outageStepper) Step(context.Context, model.StepRequest) (model.StepResponse, error) {
	s.calls++
	if s.calls <= s.outage {
		return model.StepResponse{}, fmt.Errorf("model: review: %w", io.ErrUnexpectedEOF)
	}
	return model.StepResponse{ToolCalls: []model.ToolCall{{ID: "1", Name: testSubmitDef.Name, Input: json.RawMessage(`{}`)}}}, nil
}

func TestRunWaitsOutATransientStepFailure(t *testing.T) {
	old := stepRetryWaits
	stepRetryWaits = []time.Duration{time.Millisecond, time.Millisecond}
	t.Cleanup(func() { stepRetryWaits = old })
	tests := []struct {
		name      string
		outage    int
		wantStop  StopReason
		wantCalls int
	}{
		{"an outage shorter than the waits is answered", 2, StopSubmitted, 3},
		{"an outage past the waits ends the run", 5, StopError, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &outageStepper{outage: tt.outage}
			res := Run{Stepper: s, Model: "m", User: "review", Submit: testSubmitDef}.Do(t.Context())
			if res.Stop != tt.wantStop || s.calls != tt.wantCalls {
				t.Fatalf("stop = %s (%s) after %d calls, want %s after %d", res.Stop, res.Err, s.calls, tt.wantStop, tt.wantCalls)
			}
		})
	}

	t.Run("a wait the ctx cut ends the run canceled", func(t *testing.T) {
		stepRetryWaits = []time.Duration{time.Hour}
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
		defer cancel()
		s := &outageStepper{outage: 5}
		if res := (Run{Stepper: s, Model: "m", User: "review", Submit: testSubmitDef}).Do(ctx); res.Stop != StopCanceled || s.calls != 1 {
			t.Fatalf("stop = %s after %d calls, want canceled after one", res.Stop, s.calls)
		}
	})
}

func TestRun(t *testing.T) {
	cases := []runCase{
		{
			name: "grep_read_submit",
			tools: []Tool{
				&fakeTool{name: "grep", output: "widget.go:1: match"},
				&fakeTool{name: "read_file", output: "1\tpackage main"},
			},
			setup:     setupGrepReadSubmit,
			wantStop:  StopSubmitted,
			wantSteps: 3,
			check:     checkGrepReadSubmit,
		},
		{
			name:      "unknown_tool_then_submit",
			setup:     setupUnknownToolThenSubmit,
			wantStop:  StopSubmitted,
			wantSteps: 2,
			check:     checkUnknownToolThenSubmit,
		},
		{
			name:      "invalid_submit_json_then_valid",
			setup:     setupInvalidSubmitJSONThenValid,
			wantStop:  StopSubmitted,
			wantSteps: 2,
			check:     checkInvalidSubmitJSONThenValid,
		},
		{
			// MaxSteps: 1 makes the only step the last step, which forces
			// submit_review; a forced step that does not submit is retried
			// until the retries run out, each told to submit again, and a
			// tool called instead is refused rather than run.
			name:      "forced_never_submits_stops_after_retries",
			tools:     []Tool{&fakeTool{name: "noop", output: "ok"}},
			limits:    Limits{MaxSteps: 1},
			setup:     setupForcedNeverSubmits,
			wantStop:  StopMaxSteps,
			wantSteps: 1 + forcedRetries,
			check:     checkForcedNeverSubmits,
		},
		{
			name:      "forced_tool_call_refused_then_submit",
			tools:     []Tool{&fakeTool{name: "noop", output: "ok"}},
			limits:    Limits{MaxSteps: 1},
			setup:     setupForcedToolCallRefused,
			wantStop:  StopSubmitted,
			wantSteps: 2,
			check:     checkForcedToolCallRefused,
		},
		{
			name:      "rejected_submit_then_valid",
			validate:  rejectVerdict,
			setup:     setupRejectedSubmitThenValid,
			wantStop:  StopSubmitted,
			wantSteps: 2,
			check:     checkRejectedSubmitThenValid,
		},
		{
			name:      "forced_rejected_submit_retried",
			limits:    Limits{MaxSteps: 1},
			validate:  rejectVerdict,
			setup:     setupForcedRejectedSubmitRetried,
			wantStop:  StopSubmitted,
			wantSteps: 2,
			check:     checkForcedRejectedSubmitRetried,
		},
		{
			// The forced retries run out, and the Run ends with the last
			// refused input Fallback held rather than with none.
			name:      "forced_retries_spent_takes_fallback",
			limits:    Limits{MaxSteps: 1},
			validate:  rejectVerdict,
			fallback:  holdFallback,
			setup:     setupForcedFallbackTaken,
			wantStop:  StopSubmitted,
			wantSteps: 1 + forcedRetries,
			check:     checkForcedFallbackTaken,
		},
		{
			name:      "forced_retries_spent_nothing_held",
			limits:    Limits{MaxSteps: 1},
			validate:  rejectVerdict,
			fallback:  holdFallback,
			setup:     setupForcedNothingHeld,
			wantStop:  StopMaxSteps,
			wantSteps: 1 + forcedRetries,
			check:     checkNothingSubmitted,
		},
		{
			name:      "stepper_error_takes_fallback",
			validate:  rejectVerdict,
			fallback:  holdFallback,
			setup:     setupStepperErrorAfterHeldInput,
			wantStop:  StopSubmitted,
			wantSteps: 1,
			check:     checkFallbackSubmitted,
		},
		{
			// A canceled Run posts nothing, whatever it held.
			name:      "canceled_does_not_take_fallback",
			validate:  rejectVerdict,
			fallback:  holdFallback,
			setup:     setupCanceledAfterHeldInput,
			wantStop:  StopCanceled,
			wantSteps: 1,
			check:     checkNothingSubmitted,
		},
		{
			name:      "text_only_twice_stops_no_submit",
			setup:     setupTextOnlyTwiceStopsNoSubmit,
			wantStop:  StopNoSubmit,
			wantSteps: 2,
			check:     checkTextOnlyTwiceStopsNoSubmit,
		},
		{
			// A tool-call turn between the two text-only turns must not
			// reset the once-per-Run nudge: a second text-only turn still
			// ends the Run rather than sending a second nudge.
			name:      "nudge_is_once_per_run_not_reset_by_tool_turn",
			tools:     []Tool{&fakeTool{name: "noop", output: "ok"}},
			setup:     setupNudgeOncePerRunNotResetByToolTurn,
			wantStop:  StopNoSubmit,
			wantSteps: 3,
		},
		{
			name:      "budget_tells_to_submit",
			tools:     []Tool{&fakeTool{name: "noop", output: "ok"}},
			limits:    Limits{MaxTokens: 100},
			setup:     setupBudgetTellsToSubmit,
			wantStop:  StopSubmitted,
			wantSteps: 2,
			check:     checkBudgetTellsToSubmit,
		},
		{
			// Forced by the budget rather than the step cap, the retries
			// still run out, and the run is no submit: the budget itself
			// was not reached.
			name:      "budget_forced_never_submits_stops_no_submit",
			tools:     []Tool{&fakeTool{name: "noop", output: "ok"}},
			limits:    Limits{MaxTokens: 100},
			setup:     setupBudgetForcedNeverSubmits,
			wantStop:  StopNoSubmit,
			wantSteps: 2 + forcedRetries,
			check:     checkBudgetForcedNeverSubmits,
		},
		{
			// A submission the output cap cut off ends the run at once:
			// sent back as an error, the model would only be cut off
			// again.
			name:      "truncated_submit_stops",
			setup:     setupTruncatedSubmitStops,
			wantStop:  StopTruncated,
			wantSteps: 1,
			check:     checkTruncatedSubmitStops,
		},
		{
			// A step the output cap cut off before any tool call had not
			// finished: it is told to go on, and the nudge is kept for a
			// turn that answers in prose.
			name:      "cut_off_step_goes_on",
			setup:     setupCutOffStepGoesOn,
			wantStop:  StopSubmitted,
			wantSteps: 3,
			check:     checkCutOffStepGoesOn,
		},
		{
			// Cut off more than cutOffRetries times in a row, the model is
			// nudged to submit, and the next cut-off ends the run as a turn
			// with no tool call would.
			name:      "cut_off_steps_in_a_row_fall_back_to_the_nudge",
			tools:     []Tool{&fakeTool{name: "noop", output: "ok"}},
			setup:     setupCutOffStepsInARow,
			wantStop:  StopNoSubmit,
			wantSteps: 7,
			check:     checkCutOffStepsInARow,
		},
		{
			// A turn in prose ends a run of cut-off steps as a tool call
			// does: the cut-off after it is told to go on.
			name:      "prose_ends_a_run_of_cut_off_steps",
			setup:     setupCutOffAfterProse,
			wantStop:  StopSubmitted,
			wantSteps: 5,
			check:     checkCutOffAfterProse,
		},
		{
			name:      "budget_exhausted_stops_immediately",
			tools:     []Tool{&fakeTool{name: "noop", output: "ok"}},
			limits:    Limits{MaxTokens: 50},
			setup:     setupBudgetExhaustedStopsImmediately,
			wantStop:  StopBudget,
			wantSteps: 1,
			check:     checkBudgetExhaustedStopsImmediately,
		},
		{
			name:      "max_steps_reached_without_submit",
			tools:     []Tool{&fakeTool{name: "noop", output: "ok"}},
			limits:    Limits{MaxSteps: 2},
			setup:     setupMaxStepsReachedWithoutSubmit,
			wantStop:  StopMaxSteps,
			wantSteps: 2 + forcedRetries,
			check:     checkMaxStepsReachedWithoutSubmit,
		},
		{
			name:      "context_canceled_before_first_step",
			setup:     setupContextCanceledBeforeFirstStep,
			wantStop:  StopCanceled,
			wantSteps: 0,
			check:     checkContextCanceledBeforeFirstStep,
		},
		{
			name:      "stepper_error",
			setup:     setupStepperError,
			wantStop:  StopError,
			wantSteps: 0,
			check:     checkStepperError,
		},
		{
			// The gateway refusing a step for the budget ends the run the
			// way the loop's own budget check does.
			name:      "gateway_budget_refusal_is_stop_budget",
			setup:     setupGatewayBudgetRefusal,
			wantStop:  StopBudget,
			wantSteps: 0,
			check:     checkGatewayBudgetRefusal,
		},
		{
			// A Stepper error correlated with the Run's own ctx cancellation
			// must end as StopCanceled, not StopError: the Job asked for
			// this, it wasn't a genuine Stepper failure.
			name:      "stepper_error_while_ctx_canceled_is_stop_canceled",
			setup:     setupStepperErrorWhileCtxCanceled,
			wantStop:  StopCanceled,
			wantSteps: 0,
			check:     checkCanceledRunHasNoErr,
		},
		{
			// A job deadline arriving as context.DeadlineExceeded inside
			// Step must also end as StopCanceled, not StopError.
			name:      "stepper_error_on_ctx_deadline_exceeded_is_stop_canceled",
			setup:     setupStepperErrorOnCtxDeadlineExceeded,
			wantStop:  StopCanceled,
			wantSteps: 0,
			check:     checkCanceledRunHasNoErr,
		},
		{
			name:      "usage_summed_across_steps",
			tools:     []Tool{&fakeTool{name: "noop", output: "ok"}},
			setup:     setupUsageSummedAcrossSteps,
			wantStop:  StopSubmitted,
			wantSteps: 2,
			check:     checkUsageSummedAcrossSteps,
		},
		{
			name:      "tool_output_truncated",
			tools:     []Tool{&fakeTool{name: "big", output: strings.Repeat("abcdefghij", 10)}},
			limits:    Limits{MaxToolOutputBytes: 40},
			setup:     setupToolOutputTruncated,
			wantStop:  StopSubmitted,
			wantSteps: 2,
			check:     checkToolOutputTruncated,
		},
		{
			name:      "submit_ends_multi_call_turn_early",
			tools:     []Tool{&fakeTool{name: "noop", output: "ok"}, &fakeTool{name: "noop2", output: "ok"}},
			setup:     setupSubmitEndsMultiCallTurnEarly,
			wantStop:  StopSubmitted,
			wantSteps: 1,
			check:     checkSubmitEndsMultiCallTurnEarly,
		},
		{
			// Several non-submit tool calls in one turn must each get a
			// ToolResult carrying that call's own CallID and output, not a
			// mixed-up or missing one.
			name: "multiple_tool_calls_get_matching_call_ids",
			tools: []Tool{
				&fakeTool{name: "alpha", output: "alpha-out"},
				&fakeTool{name: "beta", output: "beta-out"},
				&fakeTool{name: "gamma", output: "gamma-out"},
			},
			setup:     setupMultipleToolCallsGetMatchingCallIDs,
			wantStop:  StopSubmitted,
			wantSteps: 2,
			check:     checkMultipleToolCallsGetMatchingCallIDs,
		},
		{
			// A step's StepEvent must list every tool called during it and
			// the total (post-truncation) bytes of tool output it produced.
			name: "step_event_tools_and_output_bytes_populated",
			tools: []Tool{
				&fakeTool{name: "alpha", output: "aaaaa"},
				&fakeTool{name: "beta", output: "bbb"},
			},
			setup:     setupStepEventToolsAndOutputBytesPopulated,
			wantStop:  StopSubmitted,
			wantSteps: 2,
			check:     checkStepEventToolsAndOutputBytesPopulated,
		},
		{
			// A result of dropOutputBytes or more is sent whole to the step
			// after it and as a note to every later one.
			name: "large_result_dropped_after_read",
			tools: []Tool{
				&fakeTool{name: "big", output: strings.Repeat("x", dropOutputBytes)},
				&fakeTool{name: "small", output: "small"},
			},
			setup:     setupLargeResultDroppedAfterRead,
			wantStop:  StopSubmitted,
			wantSteps: 3,
			check:     checkLargeResultDroppedAfterRead,
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			stepper, ctx, scripted := tt.setup(t)
			if ctx == nil {
				ctx = t.Context()
			}
			var events []StepEvent
			run := Run{
				Stepper:  stepper,
				Tools:    tt.tools,
				Submit:   testSubmitDef,
				Validate: tt.validate,
				Fallback: tt.fallback,
				Limits:   tt.limits,
				OnStep:   func(e StepEvent) { events = append(events, e) },
			}

			result := run.Do(ctx)

			if result.Stop != tt.wantStop {
				t.Fatalf("Stop = %v, want %v", result.Stop, tt.wantStop)
			}
			if result.Steps != tt.wantSteps {
				t.Fatalf("Steps = %d, want %d", result.Steps, tt.wantSteps)
			}
			if tt.check != nil {
				tt.check(t, result, events, scripted)
			}
		})
	}
}

func TestLimitsWithDefaults(t *testing.T) {
	t.Run("all zero", func(t *testing.T) {
		got := Limits{}.WithDefaults()
		want := Limits{MaxSteps: 60, MaxToolOutputBytes: 32 << 10, MaxTokens: 4_000_000, MaxOutputTokensPerStep: 16384}
		if got != want {
			t.Fatalf("WithDefaults() = %+v, want %+v", got, want)
		}
	})

	t.Run("set fields untouched", func(t *testing.T) {
		got := Limits{MaxSteps: 5, MaxTokens: 10}.WithDefaults()
		if got.MaxSteps != 5 || got.MaxTokens != 10 {
			t.Fatalf("WithDefaults() = %+v, want set fields preserved", got)
		}
		if got.MaxToolOutputBytes != 32<<10 || got.MaxOutputTokensPerStep != 16384 {
			t.Fatalf("WithDefaults() = %+v, want zero fields filled", got)
		}
	})
}
