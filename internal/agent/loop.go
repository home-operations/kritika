package agent

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/textcut"
)

// Limits bounds a Run: how many steps it may take, how much of a tool's
// output it keeps, and the token and per-step output budgets that force an
// early submission.
type Limits struct {
	MaxSteps               int
	MaxToolOutputBytes     int
	MaxTokens              int64 // total prompt+output budget
	MaxOutputTokensPerStep int64
}

// DefaultLimits are the fleet defaults. The configuration's agent defaults
// take theirs from here.
var DefaultLimits = Limits{MaxSteps: 60, MaxToolOutputBytes: 32 << 10, MaxTokens: 6_000_000, MaxOutputTokensPerStep: 16384}

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
	// StopTruncated is a submission cut off at the step's output cap.
	StopTruncated StopReason = "truncated"
	StopCanceled  StopReason = "canceled"
	StopError     StopReason = "error"
)

// ForcedBy is the limit that made the loop tell the model to submit with
// what it had, before it chose to.
type ForcedBy string

// Limits a Run can be forced to submit by.
const (
	ForcedBySteps  ForcedBy = "steps"
	ForcedByTokens ForcedBy = "tokens"
	ForcedByTime   ForcedBy = "time"
)

// Tool is one function the loop offers the model.
type Tool interface {
	Def() model.ToolDef
	Run(ctx context.Context, input json.RawMessage) (string, error)
}

// StepEvent reports one completed step, for a timeline or a heartbeat.
type StepEvent struct {
	Index    int
	Calls    []StepCall
	Duration time.Duration
	Usage    model.Usage
	// Items are the kinds of the step's Responses output items, on the
	// ChatGPT route; nil elsewhere.
	Items []string
}

// StepCall is one tool call of a step: the tool, and how many bytes its
// result had, 0 for an error or a call the loop did not run.
type StepCall struct {
	Name        string `json:"name"`
	OutputBytes int    `json:"output_bytes"`
}

// Result is how a Run ended.
type Result struct {
	Stop StopReason
	// Submitted is the Submit tool's input, and Conversation the exchange
	// that ended with it, both set iff Stop == StopSubmitted.
	Submitted    json.RawMessage
	Conversation *Conversation
	Steps        int
	ToolCalls    map[string]int
	Usage        model.Usage
	CostUSD      float64
	// UnpricedSteps counts the steps nothing gave a cost, which CostUSD
	// leaves out.
	UnpricedSteps int
	// Model is the model that answered the last step, empty before one
	// has.
	Model string
	// Err says why the Run stopped where the reason alone does not: Do
	// sets it to the Stepper's error for StopError, and a caller that
	// bounds ctx may set it to explain a StopCanceled.
	Err string
	// Refused is Validate's refusal of Submitted when the Run took it as
	// its Fallback, empty when Submitted was accepted.
	Refused string
	// ForcedBy is the limit that first made the loop tell the model to
	// submit, empty when it never did or the Run had one step to take: a
	// submission then may not cover all the model meant to read.
	ForcedBy ForcedBy
}

// add counts resp, the answer to one of the Run's steps.
func (r *Result) add(resp model.StepResponse) {
	r.Steps++
	r.Usage = r.Usage.Add(resp.Usage)
	r.Model = resp.Model
	r.CostUSD += resp.CostUSD
	if resp.Unpriced {
		r.UnpricedSteps++
	}
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
	// Submit is the tool the model answers with, a review's submit_review
	// or a follow-up's submit_reply; its schema is the answer's contract.
	// The loop never runs it: a call to Submit ends the Run.
	Submit model.ToolDef
	// Validate, if set, checks a Submit input against the contract beyond
	// its being JSON. A rejected input goes back to the model as the
	// tool's error, so it can correct it, like invalid JSON.
	Validate func(input json.RawMessage) error
	// Fallback, if set, says whether an input Validate refused is still an
	// answer. A Run that would end without a valid submission, other than
	// by cancellation, ends with the last such input instead, so a slip the
	// model never corrects does not cost the whole answer.
	Fallback func(input json.RawMessage) bool
	// Evidence, if set, checks a Submit input Validate accepted on a step
	// the model was not told to submit on. A refusal goes back as the
	// tool's error, like Validate's, and asks for what the model can still
	// do, such as read a file; on a step it must submit on, where it can
	// call no tool, the input is taken as it is.
	Evidence func(input json.RawMessage) error
	Limits   Limits
	// OnStep, if set, is called after each step completes.
	OnStep func(StepEvent)
	// Carried, when set, is an earlier Run's conversation this one carries
	// on, which must have offered this Run's System and tools (see
	// Carries). User is then the next user turn, which first answers each
	// call of the conversation's last answer.
	Carried *Conversation
}

// Texts a carried conversation's last answer is answered with: its
// submission was taken, and the calls beside it were never run.
const (
	carriedSubmitted = "Submitted."
	carriedNotRun    = "agent: not run: the answer was submitted in the same turn"
)

// ToolDefs are the tools as the model is offered them: Tools in order,
// then Submit.
func (r Run) ToolDefs() []model.ToolDef {
	defs := make([]model.ToolDef, 0, len(r.Tools)+1)
	for _, t := range r.Tools {
		defs = append(defs, t.Def())
	}
	return append(defs, r.Submit)
}

// Carries reports whether r can carry on c: a provider's cache holds it
// only for the system prompt and tools it was sent with, byte for byte.
func (r Run) Carries(c *Conversation) bool {
	if c == nil || c.System != r.System {
		return false
	}
	had, err := json.Marshal(c.Tools)
	if err != nil {
		return false
	}
	offered, err := json.Marshal(r.ToolDefs())
	return err == nil && bytes.Equal(had, offered)
}

// carriedTurn is the user turn that carries c on with text: a result for
// each call of the answer c ended with, then text.
func carriedTurn(c *Conversation, submit, text string) model.Message {
	turn := model.Message{Role: model.RoleUser, Text: text}
	if n := len(c.Messages); n > 0 {
		for _, call := range c.Messages[n-1].ToolCalls {
			result := model.ToolResult{CallID: call.ID, Content: carriedSubmitted}
			if call.Name != submit {
				result.IsError, result.Content = true, carriedNotRun
			}
			turn.ToolResults = append(turn.ToolResults, result)
		}
	}
	return turn
}

// nudgeText is appended, as a user message, after a turn with no tool
// call: once after a turn with text, before a second such turn ends the
// Run, and after each turn with no text either, while emptyRetries allows,
// without spending that one nudge. A turn the output cap cut off before
// any tool call is answered with cutOffText instead, while cutOffRetries
// allows.
func (r Run) nudgeText() string { return "call " + r.Submit.Name }

// emptyRetries is how many steps in a row may end with neither text nor a
// tool call and each be nudged without spending the once-per-Run nudge. A
// reasoning model on the ChatGPT route sometimes ends the step before its
// submission so, and the nudge has recovered every one; the once-per-Run
// nudge is kept for a turn that answers in prose. One more is taken as a
// turn with no tool call.
const emptyRetries = 2

// goOnText answers a step with no tool call that spends no nudge: a step
// the output cap cut off, while cutOffRetries allows, or one that ended
// with nothing, while emptyRetries allows. It is "" for a step that does.
func (r Run) goOnText(limit int64, cutOffs, empties int) string {
	if 0 < cutOffs && cutOffs <= cutOffRetries {
		return r.cutOffText(limit)
	}
	if 0 < empties && empties <= emptyRetries {
		return r.nudgeText()
	}
	return ""
}

// emptyStreak is how many steps in a row, resp's last, ended with neither
// text nor a tool call, other than by the output cap, given n before
// resp's.
func emptyStreak(n int, resp model.StepResponse) int {
	if resp.Text == "" && len(resp.ToolCalls) == 0 && resp.Stop != model.StopMaxTokens {
		return n + 1
	}
	return 0
}

// cutOffText answers a step the output cap cut off before it called a tool,
// whose reasoning was spent without an answer: the model goes on, where the
// nudge would end its review on a step it had not finished. limit is the
// cap.
func (r Run) cutOffText(limit int64) string {
	return fmt.Sprintf("Your last step reached the %d output tokens a step may produce before it called a tool. "+
		"Go on with your next tool call, or call %s when you are done.", limit, r.Submit.Name)
}

// cutOffRetries is how many steps in a row the output cap may cut off
// before any tool call and the model still be told to go on. One more is
// taken as a turn with no tool call: a model that cannot fit a step into the
// cap is better asked for what it has.
const cutOffRetries = 2

// cutOffStreak is how many steps in a row, resp's last, the output cap has
// cut off before any tool call, given n before resp's.
func cutOffStreak(n int, resp model.StepResponse) int {
	if len(resp.ToolCalls) == 0 && resp.Stop == model.StopMaxTokens {
		return n + 1
	}
	return 0
}

// submitNowText ends the last user message of a step the model must submit
// on. It is told rather than forced through tool_choice, which the newest
// models reject.
func (r Run) submitNowText() string {
	return "Call " + r.Submit.Name + " now with what you have, and call nothing else."
}

// forcedRetries is how many more steps a model that was told to submit and
// did not gets, each told again: a rejected submission goes back with its
// error, prose and other tool calls with onlySubmitText. The steps spent
// exploring are worth more than one slip at the end; the token budget
// bounds the retries where the step cap does not.
const forcedRetries = 2

// wrapUpFloor is the least time a Run keeps, before its context's
// deadline, for the steps it tells the model to submit on, unless the Run
// was given less than four times that in all. A submission writes the
// whole answer, so it can take longer than any step before it.
const wrapUpFloor = 2 * time.Minute

// wrapUpReserve is the time left before the deadline at which a Run that
// was given total time in all, and whose slowest answer so far took
// slowest, tells the model to submit: two such answers, one for the
// submission and one for a retry, and at least wrapUpFloor, or a quarter
// of total when that is less.
func wrapUpReserve(slowest, total time.Duration) time.Duration {
	return max(2*slowest, min(wrapUpFloor, total/4))
}

// clock is a Run's time: the deadline its ctx has, when timed, when it
// began, and the longest a step's answer has taken.
type clock struct {
	deadline, begun time.Time
	timed           bool
	slowest         time.Duration
}

// due says whether the time left is no more than what wrapUpReserve keeps.
func (c clock) due() bool { return c.timed && c.dueIn() <= 0 }

// dueIn is how long until the time left is no more than what wrapUpReserve
// keeps, 0 or less once it is.
func (c clock) dueIn() time.Duration {
	return time.Until(c.deadline) - wrapUpReserve(c.slowest, c.deadline.Sub(c.begun))
}

// errWrapUp ends a step's retries once the Run's time is due for a
// submission its request does not ask for: the step is sent again, told to
// submit, rather than letting the model read on into its last minutes.
var errWrapUp = errors.New("agent: time to submit")

// forcedBy is the limit that has step, from 0, told to submit, "" for
// none: the last step, nine tenths of the tokens spent, or no more time
// left than wrapUpReserve keeps.
func forcedBy(step int, spent int64, limits Limits, c clock) ForcedBy {
	switch {
	case step >= limits.MaxSteps-1:
		return ForcedBySteps
	case spent*10 >= limits.MaxTokens*9:
		return ForcedByTokens
	case c.due():
		return ForcedByTime
	}
	return ""
}

// onlySubmitText answers a tool call other than Submit on a step the model
// was told to submit on; the tool is not run.
func (r Run) onlySubmitText() string { return "agent: only " + r.Submit.Name + " is available now" }

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

// holds says whether Fallback holds input, which Validate refused, to be
// an answer.
func (r Run) holds(input json.RawMessage) bool { return r.Fallback != nil && r.Fallback(input) }

// fallback is the last input Validate refused and Fallback held to be an
// answer, with the conversation its answer ends and its refusal.
type fallback struct {
	input   json.RawMessage
	conv    *Conversation
	refused string
}

// end ends result with fb when the Run is ending without a valid
// submission, other than by cancellation, and fb holds an input.
func (fb *fallback) end(result *Result) {
	if fb.input == nil || result.Stop == StopSubmitted || result.Stop == StopCanceled {
		return
	}
	result.Stop, result.Err = StopSubmitted, ""
	result.Submitted, result.Conversation, result.Refused = fb.input, fb.conv, fb.refused
}

// endedWith is the conversation of messages ended by resp, the answer that
// carries the submission.
func (r Run) endedWith(toolDefs []model.ToolDef, messages []model.Message, resp model.StepResponse) *Conversation {
	return &Conversation{
		System: r.System, Tools: toolDefs, Tokens: resp.Usage.Prompt() + resp.Usage.Output,
		Messages: append(slices.Clone(messages), model.Message{
			Role: model.RoleAssistant, Text: resp.Text, ToolCalls: resp.ToolCalls, ChatGPTOutput: resp.ChatGPTOutput,
		}),
	}
}

// Do runs the loop to completion. The model is told to submit with what it
// has on the last step it may take, once 90% of its tokens are spent, and,
// when ctx has a deadline, once the time left is no more than wrapUpReserve.
func (r Run) Do(ctx context.Context) (result Result) {
	limits := r.Limits.WithDefaults()

	toolDefs := r.ToolDefs()
	toolsByName := make(map[string]Tool, len(r.Tools))
	for i, t := range r.Tools {
		toolsByName[toolDefs[i].Name] = t
	}

	messages := []model.Message{{Role: model.RoleUser, Text: r.User}}
	if c := r.Carried; c != nil {
		messages = append(slices.Clone(c.Messages), carriedTurn(c, r.Submit.Name, r.User))
	}

	result = Result{ToolCalls: map[string]int{}}
	// Every return below that is not a submission or a cancellation is
	// rewritten on the way out, into a submission of the fallback's input,
	// when one was held.
	var fb fallback
	defer fb.end(&result)
	nudged := false
	// cutOffs counts the steps in a row, up to the last, that the output
	// cap cut off before any tool call.
	cutOffs := 0
	// empties counts the steps in a row, up to the last, that ended with
	// neither text nor a tool call.
	empties := 0
	// forcedSteps counts the steps the model was told to submit on; the
	// Run ends when the retries after the first are spent.
	forcedSteps := 0
	c := clock{begun: time.Now()}
	c.deadline, c.timed = ctx.Deadline()
	forcedEnd := func(lastStep bool) Result {
		result.Err = fmt.Sprintf("no valid %s in the %d step(s) it was told to submit on", r.Submit.Name, forcedSteps)
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
		by := forcedBy(step, total, limits, c)
		forced := by != ""
		if forced {
			if limits.MaxSteps > 1 {
				// A Run of one step submits on it by design.
				result.ForcedBy = cmp.Or(result.ForcedBy, by)
			}
			forcedSteps++
			last := &messages[len(messages)-1]
			if last.Text != "" {
				last.Text += "\n\n"
			}
			last.Text += r.submitNowText()
		}

		req := model.StepRequest{
			Model:     r.Model,
			System:    r.System,
			Messages:  messages,
			Tools:     toolDefs,
			MaxTokens: limits.MaxOutputTokensPerStep,
		}

		start := time.Now()
		resp, answered, err := r.step(ctx, req, c, forced)
		c.slowest = max(c.slowest, answered)
		if errors.Is(err, errWrapUp) {
			// The same step goes again, told to submit.
			step--
			continue
		}
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
		result.add(resp)

		event := StepEvent{Index: step, Usage: resp.Usage, Items: resp.ChatGPTOutput.Kinds()}
		cutOffs = cutOffStreak(cutOffs, resp)
		empties = emptyStreak(empties, resp)
		assistant := model.Message{Role: model.RoleAssistant, Text: resp.Text, ToolCalls: resp.ToolCalls, ChatGPTOutput: resp.ChatGPTOutput}

		if len(resp.ToolCalls) == 0 {
			event.Duration = time.Since(start)
			r.reportStep(event)

			assistant.Text = cmp.Or(resp.Text, noResponseText)
			if forced {
				if forcedSteps > forcedRetries {
					return forcedEnd(lastStep)
				}
				// The next step is told to submit again, in the user
				// message the loop's head completes.
				messages = append(messages, assistant)
				messages = append(messages, model.Message{Role: model.RoleUser})
				continue
			}
			if text := r.goOnText(limits.MaxOutputTokensPerStep, cutOffs, empties); text != "" {
				messages = append(messages, assistant)
				messages = append(messages, model.Message{Role: model.RoleUser, Text: text})
				continue
			}
			if nudged {
				result.Stop = StopNoSubmit
				return result
			}
			nudged = true
			messages = append(messages, assistant)
			messages = append(messages, model.Message{Role: model.RoleUser, Text: r.nudgeText()})
			continue
		}

		var toolResults []model.ToolResult
		var submitted json.RawMessage
		var truncated bool

		for _, call := range resp.ToolCalls {
			event.Calls = append(event.Calls, StepCall{Name: call.Name})
			result.ToolCalls[call.Name]++

			if call.Name == r.Submit.Name {
				refusal, cutOff := r.takeSubmission(call, resp, forced, limits, &fb, func() *Conversation {
					return r.endedWith(toolDefs, messages, resp)
				})
				if cutOff {
					truncated = true
					break
				}
				if refusal != nil {
					toolResults = append(toolResults, *refusal)
					continue
				}
				submitted = call.Input
				break
			}

			if forced {
				toolResults = append(toolResults, model.ToolResult{CallID: call.ID, IsError: true, Content: r.onlySubmitText()})
				continue
			}
			res := runTool(ctx, toolsByName[call.Name], call, limits.MaxToolOutputBytes)
			if !res.IsError {
				event.Calls[len(event.Calls)-1].OutputBytes = len(res.Content)
			}
			toolResults = append(toolResults, res)
		}

		event.Duration = time.Since(start)
		r.reportStep(event)

		if submitted != nil {
			result.Stop = StopSubmitted
			result.Submitted = submitted
			result.Conversation = r.endedWith(toolDefs, messages, resp)
			return result
		}
		if truncated {
			result.Stop = StopTruncated
			result.Err = fmt.Sprintf("%s was cut off at the %d output tokens a step may produce", r.Submit.Name, limits.MaxOutputTokensPerStep)
			return result
		}
		if forced && forcedSteps > forcedRetries {
			return forcedEnd(lastStep)
		}

		messages = append(messages, assistant)
		messages = append(messages, model.Message{Role: model.RoleUser, ToolResults: toolResults})
	}
}

// takeSubmission judges call, a call of Submit on a step answered by resp:
// an input Validate, and Evidence on a step not forced, accept is the
// Run's answer, and neither is returned; one refused goes back to the
// model as the refusal, held as the fallback when Fallback holds it, with
// ended the conversation the step ends; one the output cap cut off is
// neither, since sent back as an error the model would re-emit it whole
// and be cut off again, step after step.
func (r Run) takeSubmission(
	call model.ToolCall, resp model.StepResponse, forced bool, limits Limits, fb *fallback, ended func() *Conversation,
) (refusal *model.ToolResult, cutOff bool) {
	err := r.checkSubmit(call.Input)
	switch {
	case err != nil && resp.Stop == model.StopMaxTokens:
		return nil, true
	case err != nil:
		if r.holds(call.Input) {
			*fb = fallback{input: call.Input, conv: ended(), refused: err.Error()}
		}
	case !forced && r.Evidence != nil:
		err = r.Evidence(call.Input)
	}
	if err == nil {
		return nil, false
	}
	return &model.ToolResult{
		CallID: call.ID, IsError: true,
		Content: textcut.Truncate(fmt.Sprintf("agent: %s: %s", r.Submit.Name, err), limits.MaxToolOutputBytes),
	}, false
}

// step sends req, again after each of stepRetryWaits while the failure is
// transient and ctx lives, and says how long the last attempt took: the
// waits between attempts are not the model's, and would make a Run keep
// far more time to submit in than a submission takes. A req not told to
// submit, forced false, is not sent again into the time c keeps to submit
// in: a wait ends where that time starts, and step returns errWrapUp.
func (r Run) step(ctx context.Context, req model.StepRequest, c clock, forced bool) (model.StepResponse, time.Duration, error) {
	attempt := func() (model.StepResponse, time.Duration, error) {
		start := time.Now()
		resp, err := r.Stepper.Step(ctx, req)
		return resp, time.Since(start), err
	}
	resp, took, err := attempt()
	for _, wait := range stepRetryWaits {
		if err == nil || !model.Transient(err) {
			break
		}
		var wrapUp <-chan time.Time
		if !forced && c.timed {
			in := c.dueIn()
			if in <= 0 {
				return resp, took, errWrapUp
			}
			wrapUp = time.After(in)
		}
		select {
		case <-ctx.Done():
			return resp, took, err
		case <-wrapUp:
			return resp, took, errWrapUp
		case <-time.After(wait):
			resp, took, err = attempt()
		}
	}
	return resp, took, err
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
