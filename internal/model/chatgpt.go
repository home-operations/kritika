package model

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
)

// TokenSource is where the ChatGPT adapter gets the bearer token each
// request carries. A plan's access token lasts an hour and the source
// renews it, where a provider's API key is fixed for the process.
type TokenSource interface {
	// Token is the token to send now, renewed if need be.
	Token(ctx context.Context) (string, error)
	// Expire marks the token spent after the provider refused it, so the
	// next Token renews it rather than send it for the rest of its hour.
	Expire()
}

// ErrPlanLimit is a step the ChatGPT plan refused because its usage limit
// is reached. The adapter then sends nothing on the plan for PlanPause, as
// OpenAI asks, failing every step the same way, which the fallback takes;
// the plan's owner sees the limit under ChatGPT's usage settings.
var ErrPlanLimit = errors.New("model: chatgpt: the plan's usage limit is reached")

// ErrUnavailable is a provider that could not serve a request for a
// passing reason of its own: the ChatGPT route that could not check the
// plan's usage. Transient.
var ErrUnavailable = errors.New("model: provider unavailable")

// PlanPause is how long the adapter sends nothing on the plan after
// ErrPlanLimit. The error says nothing of when the limit resets, and a
// step sent meanwhile costs the plan nothing but fails. A variable for the
// tests.
var PlanPause = 15 * time.Minute

// Error codes of the ChatGPT plan route.
const (
	planLimitCode           = "subscription_sharing_usage_limit_exceeded"
	planUnavailableCode     = "subscription_sharing_usage_unavailable"
	planUserUnavailableCode = "subscription_sharing_user_unavailable"
)

// toolNamespace groups kritika's tools: the route takes function tools
// in a namespace, not at the top level.
const toolNamespace = "kritika"

// messageItem is the output item that carries the model's text.
const messageItem = "message"

// ChatGPTConfig configures a ChatGPT plan adapter.
type ChatGPTConfig struct {
	// BaseURL is the API root, ".../v1"; empty means OpenAI's.
	BaseURL string
	Tokens  TokenSource
	// HTTPClient may be nil.
	HTTPClient *http.Client
	Pricing    Pricing
}

// ChatGPT is a Stepper over the Responses API on a ChatGPT Plus or Pro
// plan, signed in through Sign in with ChatGPT (internal/chatgpt). The
// route takes streamed, unstored requests with the conversation sent whole
// each step, as kritika sends it, and refuses max_output_tokens, so
// MaxTokens is not sent. Every request is sent once; the gateway owns
// retrying.
type ChatGPT struct {
	client  openai.Client
	tokens  TokenSource
	pricing Pricing
	now     func() time.Time

	mu          sync.Mutex
	pausedUntil time.Time
}

// NewChatGPT builds the adapter.
func NewChatGPT(cfg ChatGPTConfig) (*ChatGPT, error) {
	if cfg.Tokens == nil {
		return nil, errors.New("model: chatgpt: a token source is required")
	}
	baseURL := cmp.Or(cfg.BaseURL, OpenAIBaseURL)
	if err := checkBaseURL(baseURL); err != nil {
		return nil, fmt.Errorf("model: chatgpt: %w", err)
	}
	// The empty key keeps the client from reading OPENAI_API_KEY; each
	// request carries the plan's token instead.
	opts := []option.RequestOption{
		option.WithBaseURL(baseURL), option.WithAPIKey(""), option.WithHeader("User-Agent", userAgent()),
		option.WithMaxRetries(0), option.WithRequestTimeout(StepTimeout),
	}
	if cfg.HTTPClient != nil {
		opts = append(opts, option.WithHTTPClient(cfg.HTTPClient))
	}
	return &ChatGPT{client: openai.NewClient(opts...), tokens: cfg.Tokens, pricing: cfg.Pricing, now: time.Now}, nil
}

// Step implements Stepper.
func (c *ChatGPT) Step(ctx context.Context, req StepRequest) (StepResponse, error) {
	if err := checkRequest(req); err != nil {
		return StepResponse{}, err
	}
	if until, paused := c.paused(); paused {
		return StepResponse{}, fmt.Errorf("%w; nothing is sent on the plan until %s", ErrPlanLimit, until.UTC().Format(time.TimeOnly))
	}
	params, err := chatGPTParams(req)
	if err != nil {
		return StepResponse{}, err
	}
	token, err := c.tokens.Token(ctx)
	if err != nil {
		return StepResponse{}, fmt.Errorf("model: chatgpt: %w", err)
	}
	return eachModel(ctx, req, func(id string) (StepResponse, error) { return c.step(ctx, params, id, token) })
}

// step sends one request and reads its stream to the end. The step
// succeeds only on response.completed; a stream that ends before it is a
// cut connection, which is transient.
func (c *ChatGPT) step(ctx context.Context, params responses.ResponseNewParams, modelID, token string) (StepResponse, error) {
	params.Model = modelID
	stream := c.client.Responses.NewStreaming(ctx, params, option.WithAPIKey(token))
	defer func() { _ = stream.Close() }()
	var completed *responses.Response
	for stream.Next() {
		ev := stream.Current()
		switch ev.Type {
		case "response.completed":
			completed = &ev.Response
		case "response.failed":
			return StepResponse{}, c.failed(ev.Response.Error)
		case "response.incomplete":
			return StepResponse{}, fmt.Errorf("response incomplete: %s", cmp.Or(ev.Response.IncompleteDetails.Reason, "no reason given"))
		case "error":
			return StepResponse{}, fmt.Errorf("stream error %s: %s", ev.Code, ev.Message)
		}
	}
	if err := stream.Err(); err != nil {
		return StepResponse{}, c.refused(err)
	}
	if completed == nil {
		return StepResponse{}, fmt.Errorf("stream ended before response.completed: %w", io.ErrUnexpectedEOF)
	}
	return c.response(*completed, modelID), nil
}

// response reads the completed response's output: its message's text and
// refusal, and its function calls, whose call ids the tool results answer.
func (c *ChatGPT) response(r responses.Response, modelID string) StepResponse {
	out := StepResponse{Stop: StopEndTurn, Model: modelID}
	for _, item := range r.Output {
		switch item.Type {
		case messageItem:
			for _, part := range item.AsMessage().Content {
				out.Text += part.Text + part.Refusal
			}
		case "function_call":
			call := item.AsFunctionCall()
			out.ToolCalls = append(out.ToolCalls, ToolCall{ID: call.CallID, Name: call.Name, Input: json.RawMessage(cmp.Or(call.Arguments, "{}"))})
		}
	}
	if len(out.ToolCalls) > 0 {
		out.Stop = StopToolUse
	}
	u := r.Usage
	cached := u.InputTokensDetails.CachedTokens
	out.Usage = Usage{Input: max(u.InputTokens-cached, 0), CacheRead: cached, Output: u.OutputTokens}
	out.CostUSD = c.pricing.cost(modelID, out.Usage)
	return out
}

// failed classifies a response.failed, which the route may send after the
// stream opened, by its code.
func (c *ChatGPT) failed(e responses.ResponseError) error {
	switch code := string(e.Code); code {
	case planLimitCode:
		c.pause()
		return fmt.Errorf("%w: %s", ErrPlanLimit, e.Message)
	case planUnavailableCode, planUserUnavailableCode:
		return fmt.Errorf("%w: %s: %s", ErrUnavailable, code, e.Message)
	default:
		return fmt.Errorf("response failed: %s: %s", code, e.Message)
	}
}

// refused classifies a request the server refused before the stream
// opened. A plan at its limit pauses the adapter; a token the server
// rejected is renewed before the next step; everything else keeps the
// SDK's error, whose status Transient reads.
func (c *ChatGPT) refused(err error) error {
	apiErr, ok := errors.AsType[*openai.Error](err)
	if !ok {
		return err
	}
	switch {
	case apiErr.Code == planLimitCode:
		c.pause()
		return fmt.Errorf("%w: %s", ErrPlanLimit, apiErr.Message)
	case apiErr.StatusCode == http.StatusUnauthorized:
		c.tokens.Expire()
	}
	if apiErr.RawJSON() == "" && apiErr.Response != nil && apiErr.Response.Body != nil {
		// A refusal before the Responses layer, admission or routing, comes
		// as {"detail": "..."} rather than the API's error object, which
		// the SDK keeps only as the response's body.
		if body, rerr := io.ReadAll(io.LimitReader(apiErr.Response.Body, 4096)); rerr == nil && len(body) > 0 {
			return fmt.Errorf("%w %s", err, strings.TrimSpace(string(body)))
		}
	}
	return openAIError(err)
}

func (c *ChatGPT) pause() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pausedUntil = c.now().Add(PlanPause)
}

func (c *ChatGPT) paused() (time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pausedUntil, c.now().Before(c.pausedUntil)
}

// chatGPTParams maps everything but the model, which each attempt sets.
// The system prompt goes as instructions, since the route rejects system
// messages, and the tools in one namespace.
func chatGPTParams(req StepRequest) (responses.ResponseNewParams, error) {
	p := responses.ResponseNewParams{Store: openai.Bool(false)}
	if req.System != "" {
		p.Instructions = openai.String(req.System)
	}
	items := make(responses.ResponseInputParam, 0, len(req.Messages))
	for _, m := range req.Messages {
		items = append(items, chatGPTItems(m)...)
	}
	p.Input = responses.ResponseNewParamsInputUnion{OfInputItemList: items}
	if req.Effort != "" {
		p.Reasoning = shared.ReasoningParam{Effort: shared.ReasoningEffort(req.Effort)}
	}
	if len(req.Tools) == 0 {
		return p, nil
	}
	ns := responses.NamespaceToolParam{Name: toolNamespace, Description: "The tools of kritika, the code reviewer running this conversation."}
	for _, t := range req.Tools {
		fn := responses.NamespaceToolToolFunctionParam{Name: t.Name}
		if t.Description != "" {
			fn.Description = openai.String(t.Description)
		}
		if len(t.InputSchema) > 0 {
			var schema map[string]any
			if err := json.Unmarshal(t.InputSchema, &schema); err != nil {
				return p, fmt.Errorf("model: tool %s: input schema: %w", t.Name, err)
			}
			fn.Parameters = schema
		}
		ns.Tools = append(ns.Tools, responses.NamespaceToolToolUnionParam{OfFunction: &fn})
	}
	p.Tools = []responses.ToolUnionParam{{OfNamespace: &ns}}
	switch req.ToolChoice.Mode {
	case ToolChoiceRequired:
		p.ToolChoice.OfToolChoiceMode = openai.Opt(responses.ToolChoiceOptionsRequired)
	case ToolChoiceTool:
		p.ToolChoice.OfFunctionTool = &responses.ToolChoiceFunctionParam{Name: req.ToolChoice.Name}
	default:
		p.ToolChoice.OfToolChoiceMode = openai.Opt(responses.ToolChoiceOptionsAuto)
	}
	return p, nil
}

// chatGPTItems maps one message to input items: tool results first, as
// function call outputs, then the text, then an assistant's calls, in the
// namespace they were made in. A tool error is marked in the output's
// text, as chat completions marks it.
func chatGPTItems(m Message) []responses.ResponseInputItemUnionParam {
	items := make([]responses.ResponseInputItemUnionParam, 0, 1+len(m.ToolCalls)+len(m.ToolResults))
	for _, r := range m.ToolResults {
		content := r.Content
		if r.IsError {
			content = toolErrorPrefix + content
		}
		item := responses.ResponseInputItemParamOfFunctionCallOutput(content)
		item.OfFunctionCallOutput.CallID = openai.String(r.CallID)
		item.OfFunctionCallOutput.Namespace = openai.String(toolNamespace)
		items = append(items, item)
	}
	if m.Text != "" || (m.Role == RoleUser && len(m.ToolResults) == 0) {
		items = append(items, responses.ResponseInputItemParamOfMessage(m.Text, responses.EasyInputMessageRole(m.Role)))
	}
	for _, call := range m.ToolCalls {
		item := responses.ResponseInputItemParamOfFunctionCall(cmp.Or(string(call.Input), "{}"), call.ID, call.Name)
		item.OfFunctionCall.Namespace = openai.String(toolNamespace)
		items = append(items, item)
	}
	return items
}
