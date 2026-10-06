package agent

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/textcut"
)

// Limits bounds a Run: how many steps it may take, how much of a tool's
// output it keeps, and the token and per-step output budgets that force an
// early submit_review.
type Limits struct {
	MaxSteps               int
	MaxToolOutputBytes     int
	MaxTokens              int64 // total prompt+output budget
	MaxOutputTokensPerStep int64
}

// DefaultLimits are the fleet defaults. The configuration's agent defaults
// take theirs from here.
var DefaultLimits = Limits{MaxSteps: 60, MaxToolOutputBytes: 32 << 10, MaxTokens: 4_000_000, MaxOutputTokensPerStep: 16384}

// WithDefaults fills every zero-valued field of l from DefaultLimits,
// leaving any field the caller already set untouched.
func (l Limits) WithDefaults() Limits {
	l.MaxSteps = cmp.Or(l.MaxSteps, DefaultLimits.MaxSteps)
	l.MaxToolOutputBytes = cmp.Or(l.MaxToolOutputBytes, DefaultLimits.MaxToolOutputBytes)
	l.MaxTokens = cmp.Or(l.MaxTokens, DefaultLimits.MaxTokens)
	l.MaxOutputTokensPerStep = cmp.Or(l.MaxOutputTokensPerStep, DefaultLimits.MaxOutputTokensPerStep)
	return l
}

// StopReason is why a Run ended.
type StopReason string

// Stop reasons a Run can end with.
const (
	StopSubmitted StopReason = "submitted"
	StopMaxSteps  StopReason = "max_steps"
	StopBudget    StopReason = "budget"
	StopNoSubmit  StopReason = "no_submit"
	// StopTruncated is a submit_review cut off at the step's output cap.
	StopTruncated StopReason = "truncated"
	StopCanceled  StopReason = "canceled"
	StopError     StopReason = "error"
)

// Tool is one function the loop offers the model.
type Tool interface {
	Def() model.ToolDef
	Run(ctx context.Context, input json.RawMessage) (string, error)
}

// StepEvent reports one completed step, for a timeline or a heartbeat.
type StepEvent struct {
	Index       int
	Tools       []string
	Duration    time.Duration
	OutputBytes int
	Usage       model.Usage
}

// Result is how a Run ended.
type Result struct {
	Stop StopReason
	// Submitted is the submit_review input, set iff Stop == StopSubmitted.
	Submitted json.RawMessage
	Steps     int
	ToolCalls map[string]int
	Usage     model.Usage
	CostUSD   float64
	// Model is the model that answered the last step, empty before one
	// has.
	Model string
	// Err says why the Run stopped where the reason alone does not: Do
	// sets it to the Stepper's error for StopError, and a caller that
	// bounds ctx may set it to explain a StopCanceled.
	Err string
}

// Run is a bounded, read-only tool loop over a git commit's tree: on each
// step the Stepper may call one of Tools or Submit, until it submits, a
// limit is reached, or ctx ends.
type Run struct {
	Stepper model.Stepper
	Model   string
	System  string
	User    string
	Tools   []Tool
	// Submit is the submit_review tool; its schema is the review contract.
	// The loop never runs it: a call to Submit ends the Run.
	Submit model.ToolDef
	// Validate, if set, checks a Submit input against the contract beyond
	// its being JSON. A rejected input goes back to the model as the
	// tool's error, so it can correct it, like invalid JSON.
	Validate func(input json.RawMessage) error
	Limits   Limits
	// OnStep, if set, is called after each step completes.
	OnStep func(StepEvent)
}

// nudgeText is appended once, as a user message, after the first turn with
// no tool call, before a second such turn ends the Run.
const nudgeText = "call submit_review"

// submitNowText ends the last user message of a step the model must submit
// on. It is told rather than forced through tool_choice, which the newest
// models reject.
const submitNowText = "Call submit_review now with the summary and findings you have, and call nothing else."

// forcedRetries is how many more steps a model that was told to submit and
// did not gets, each told again: a rejected submission goes back with its
// error, prose and other tool calls with onlySubmitText. The steps spent
// exploring are worth more than one slip at the end; the token budget
// bounds the retries where the step cap does not.
const forcedRetries = 2

// onlySubmitText answers a tool call other than submit_review on a step the
// model was told to submit on; the tool is not run.
const onlySubmitText = "agent: only submit_review is available now"

// noResponseText replaces an empty Text on an appended assistant message, so
// the conversation never carries a message with neither text nor tool calls.
const noResponseText = "(no response)"

// stepRetryWaits are the waits before a step that failed transiently
// (model.Transient) is sent again, while ctx lives. The Stepper has already
// retried on its own schedule, so these outlast a provider outage that
// schedule did not: the conversation so far is kept rather than the Run
// ending with its steps spent. A variable for the tests.
var stepRetryWaits = []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute}

// checkSubmit says why input is not an acceptable Submit input.
func (r Run) checkSubmit(input json.RawMessage) error {
	var scratch any
	if err := json.Unmarshal(input, &scratch); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	if r.Validate != nil {
		return r.Validate(input)
	}
	return nil
}

// Do runs the loop to completion.
func (r Run) Do(ctx context.Context) Result {
	limits := r.Limits.WithDefaults()

	toolDefs := make([]model.ToolDef, 0, len(r.Tools)+1)
	toolsByName := make(map[string]Tool, len(r.Tools))
	for _, t := range r.Tools {
		d := t.Def()
		toolDefs = append(toolDefs, d)
		toolsByName[d.Name] = t
	}
	toolDefs = append(toolDefs, r.Submit)

	messages := []model.Message{{Role: model.RoleUser, Text: r.User}}

	result := Result{ToolCalls: map[string]int{}}
	nudged := false
	// forcedSteps counts the steps the model was told to submit on; the
	// Run ends when the retries after the first are spent.
	forcedSteps := 0
	forcedEnd := func(lastStep bool) Result {
		result.Err = fmt.Sprintf("no valid submit_review in the %d step(s) it was told to submit on", forcedSteps)
		result.Stop = StopNoSubmit
		if lastStep {
			result.Stop = StopMaxSteps
		}
		return result
	}

	for step := 0; ; step++ {
		if err := ctx.Err(); err != nil {
			result.Stop = StopCanceled
			return result
		}

		total := result.Usage.Prompt() + result.Usage.Output
		if total >= limits.MaxTokens {
			result.Stop = StopBudget
			return result
		}

		lastStep := step >= limits.MaxSteps-1
		forced := lastStep || total*10 >= limits.MaxTokens*9
		if forced {
			forcedSteps++
			last := &messages[len(messages)-1]
			if last.Text != "" {
				last.Text += "\n\n"
			}
			last.Text += submitNowText
		}

		req := model.StepRequest{
			Model:     r.Model,
			System:    r.System,
			Messages:  messages,
			Tools:     toolDefs,
			MaxTokens: limits.MaxOutputTokensPerStep,
		}

		start := time.Now()
		resp, err := r.step(ctx, req)
		if err != nil {
			if ctx.Err() != nil {
				result.Stop = StopCanceled
				return result
			}
			// The gateway's count is the one the caps see: its refusal ends
			// the run as the loop's own budget check would.
			result.Stop = StopError
			if errors.Is(err, model.ErrBudget) {
				result.Stop = StopBudget
			}
			result.Err = err.Error()
			return result
		}
		result.Steps++
		result.Usage = result.Usage.Add(resp.Usage)
		result.Model = resp.Model
		result.CostUSD += resp.CostUSD

		event := StepEvent{Index: step, Usage: resp.Usage}

		if len(resp.ToolCalls) == 0 {
			event.Duration = time.Since(start)
			r.reportStep(event)

			text := cmp.Or(resp.Text, noResponseText)
			if forced {
				if forcedSteps > forcedRetries {
					return forcedEnd(lastStep)
				}
				// The next step is told to submit again, in the user
				// message the loop's head completes.
				messages = append(messages, model.Message{Role: model.RoleAssistant, Text: text})
				messages = append(messages, model.Message{Role: model.RoleUser})
				continue
			}
			if nudged {
				result.Stop = StopNoSubmit
				return result
			}
			nudged = true
			messages = append(messages, model.Message{Role: model.RoleAssistant, Text: text})
			messages = append(messages, model.Message{Role: model.RoleUser, Text: nudgeText})
			continue
		}

		var toolResults []model.ToolResult
		var submitted json.RawMessage
		var truncated bool

		for _, call := range resp.ToolCalls {
			event.Tools = append(event.Tools, call.Name)
			result.ToolCalls[call.Name]++

			if call.Name == r.Submit.Name {
				if err := r.checkSubmit(call.Input); err != nil {
					// A submission the output cap cut off is not sent back
					// as an error: the model would re-emit it whole and be
					// cut off again, step after step.
					if resp.Stop == model.StopMaxTokens {
						truncated = true
						break
					}
					toolResults = append(toolResults, model.ToolResult{
						CallID: call.ID, IsError: true,
						Content: textcut.Truncate(fmt.Sprintf("agent: submit_review: %s", err), limits.MaxToolOutputBytes),
					})
					continue
				}
				submitted = call.Input
				break
			}

			if forced {
				toolResults = append(toolResults, model.ToolResult{CallID: call.ID, IsError: true, Content: onlySubmitText})
				continue
			}
			res := runTool(ctx, toolsByName[call.Name], call, limits.MaxToolOutputBytes)
			if !res.IsError {
				event.OutputBytes += len(res.Content)
			}
			toolResults = append(toolResults, res)
		}

		event.Duration = time.Since(start)
		r.reportStep(event)

		if submitted != nil {
			result.Stop = StopSubmitted
			result.Submitted = submitted
			return result
		}
		if truncated {
			result.Stop = StopTruncated
			result.Err = fmt.Sprintf("submit_review was cut off at the %d output tokens a step may produce", limits.MaxOutputTokensPerStep)
			return result
		}
		if forced && forcedSteps > forcedRetries {
			return forcedEnd(lastStep)
		}

		messages = append(messages, model.Message{Role: model.RoleAssistant, Text: resp.Text, ToolCalls: resp.ToolCalls})
		messages = append(messages, model.Message{Role: model.RoleUser, ToolResults: toolResults})
	}
}

// step sends req, again after each of stepRetryWaits while the failure is
// transient and ctx lives.
func (r Run) step(ctx context.Context, req model.StepRequest) (model.StepResponse, error) {
	resp, err := r.Stepper.Step(ctx, req)
	for _, wait := range stepRetryWaits {
		if err == nil || !model.Transient(err) {
			break
		}
		select {
		case <-ctx.Done():
		case <-time.After(wait):
			resp, err = r.Stepper.Step(ctx, req)
		}
	}
	return resp, err
}

// runTool runs call on tool, nil for a tool the loop does not offer, and
// returns its result for the model, cut to maxBytes.
func runTool(ctx context.Context, tool Tool, call model.ToolCall, maxBytes int) model.ToolResult {
	if tool == nil {
		return model.ToolResult{
			CallID: call.ID, IsError: true,
			Content: textcut.Truncate(fmt.Sprintf("agent: unknown tool %q", call.Name), maxBytes),
		}
	}
	out, err := tool.Run(ctx, call.Input)
	if err != nil {
		return model.ToolResult{CallID: call.ID, IsError: true, Content: textcut.Truncate(err.Error(), maxBytes)}
	}
	return model.ToolResult{CallID: call.ID, Content: textcut.Truncate(out, maxBytes)}
}

func (r Run) reportStep(e StepEvent) {
	if r.OnStep != nil {
		r.OnStep(e)
	}
}
