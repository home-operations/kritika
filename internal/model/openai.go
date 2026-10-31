package model

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/shared"
)

// Attribution headers that OpenRouter shows in its dashboard. They are
// harmless elsewhere.
var attribution = map[string]string{
	"HTTP-Referer": "https://github.com/home-operations/kritika",
	"X-Title":      "kritika",
}

// Version is the kritika version that the adapters identify themselves with
// to a provider. main sets it once at startup. Model gateways that
// distinguish coding agents from generic SDK traffic ask for a client's own
// user agent rather than the SDK's.
var Version = "dev"

func userAgent() string { return "kritika/" + Version }

// OpenAIConfig configures an OpenAI chat-completions adapter.
type OpenAIConfig struct {
	// BaseURL is the API root, ".../v1". Empty means the SDK's default.
	BaseURL string
	APIKey  string
	// Headers are the provider's configured headers, which replace kritika's
	// own of the same name. See SessionPlaceholder.
	Headers map[string]string
	// HTTPClient can be nil.
	HTTPClient *http.Client
	// OpenRouter enables OpenRouter's extensions: server-side fallback
	// through the models list, and usage accounting. It implies
	// ReportsModel.
	OpenRouter bool
	// ReportsModel trusts the response's model field to name the model
	// that answered, as kritika's model gateway sets it.
	ReportsModel bool
	// Gateway carries the ChatGPT output kritika's model gateway returns
	// with a turn back to it on the turn's later steps.
	Gateway bool
	// OpenCode names the step's conversation in the header that OpenCode Go
	// and Zen route and cache prompts by. Without it they refuse the request.
	OpenCode bool
	Pricing  Pricing
	// Retries is how many times the SDK's client sends again, on its own, a
	// request that the server failed. The client minds the server's
	// Retry-After and x-should-retry headers. Zero sends the request once. A
	// provider's adapter leaves it at zero: the gateway owns retrying, with
	// the provider's retries, and the SDK's own retries will otherwise
	// multiply its attempts.
	Retries int
	// RequestTimeout bounds one request. Zero is StepTimeout, a provider's
	// timeout. The runner's client of the gateway allows the gateway's whole
	// step budget.
	RequestTimeout time.Duration
	// Part names, in PartHeader on every request, the part of a split review
	// that the runner's client of the gateway steps for. Zero sends none.
	Part int
	// Reviewer names, in ReviewerHeader on every request, the reviewer other
	// than the default one that the runner's client of the gateway steps for.
	// "" sends none.
	Reviewer string
}

// OpenCodeSessionHeader carries StepRequest.Session to OpenCode.
const OpenCodeSessionHeader = "x-opencode-session"

// PartHeader carries, from a runner to the gateway, the part of a split
// review a step is for.
const PartHeader = "X-Kritika-Part"

// ReviewerHeader carries, from a runner to the gateway, the reviewer other
// than the default one a step is for, whose models the gateway calls.
const ReviewerHeader = "X-Kritika-Reviewer"

// OpenAI is a Stepper over the chat completions API of OpenAI or any server
// compatible with it.
type OpenAI struct {
	client       openai.Client
	openRouter   bool
	reportsModel bool
	gateway      bool
	openCode     bool
	// sessionHeaders are the configured headers that carry a step's
	// session.
	sessionHeaders map[string]string
	pricing        Pricing
	now            func() time.Time
	catalogCache   modelCatalog[string]
}

// withoutEnvironment deletes the headers that openai.NewClient adds from the
// environment: OPENAI_ORG_ID, OPENAI_PROJECT_ID, and OPENAI_CUSTOM_HEADERS.
// Otherwise they reach every OpenAI-compatible provider, the ChatGPT backend,
// and the gateway alike. openai-go has no opt-out like anthropic-sdk-go's
// WithoutEnvironmentDefaults. kritika always sets the key and base URL. It
// goes before kritika's own headers, which a custom header can name.
func withoutEnvironment() []option.RequestOption {
	opts := []option.RequestOption{option.WithHeaderDel("OpenAI-Organization"), option.WithHeaderDel("OpenAI-Project")}
	for line := range strings.SplitSeq(os.Getenv("OPENAI_CUSTOM_HEADERS"), "\n") {
		if name, _, ok := strings.Cut(line, ":"); ok {
			name = strings.TrimSpace(name)
			if strings.EqualFold(name, "Content-Type") || strings.EqualFold(name, "Accept") {
				// These defaults describe the SDK's JSON requests. Deleting
				// the environment override also deletes the default.
				opts = append(opts, option.WithHeader(name, "application/json"))
			} else {
				opts = append(opts, option.WithHeaderDel(name))
			}
		}
	}
	return opts
}

// NewOpenAI builds the adapter.
func NewOpenAI(cfg OpenAIConfig) (*OpenAI, error) {
	if cfg.APIKey == "" {
		return nil, errors.New("model: openai: an API key is required")
	}
	opts := append(withoutEnvironment(), option.WithAPIKey(cfg.APIKey))
	if cfg.BaseURL != "" {
		if err := checkBaseURL(cfg.BaseURL); err != nil {
			return nil, fmt.Errorf("model: openai: %w", err)
		}
		opts = append(opts, option.WithBaseURL(cfg.BaseURL))
	}
	if cfg.HTTPClient != nil {
		opts = append(opts, option.WithHTTPClient(cfg.HTTPClient))
	}
	opts = append(opts, option.WithHeader("User-Agent", userAgent()), option.WithMaxRetries(cfg.Retries),
		option.WithRequestTimeout(cmp.Or(cfg.RequestTimeout, StepTimeout)))
	for k, v := range attribution {
		opts = append(opts, option.WithHeader(k, v))
	}
	if cfg.Part > 0 {
		opts = append(opts, option.WithHeader(PartHeader, strconv.Itoa(cfg.Part)))
	}
	if cfg.Reviewer != "" {
		opts = append(opts, option.WithHeader(ReviewerHeader, cfg.Reviewer))
	}
	static, session := splitHeaders(cfg.Headers)
	for k, v := range static {
		opts = append(opts, option.WithHeader(k, v))
	}
	return &OpenAI{
		client: openai.NewClient(opts...), openRouter: cfg.OpenRouter, reportsModel: cfg.OpenRouter || cfg.ReportsModel,
		gateway: cfg.Gateway, openCode: cfg.OpenCode, sessionHeaders: session, pricing: cfg.Pricing, now: time.Now,
	}, nil
}

func checkBaseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("base URL: %w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("base URL %q must be absolute", raw)
	}
	return nil
}

// Step implements Stepper.
func (o *OpenAI) Step(ctx context.Context, req StepRequest) (StepResponse, error) {
	if err := checkRequest(req); err != nil {
		return StepResponse{}, err
	}
	params, err := o.params(req)
	if err != nil {
		return StepResponse{}, err
	}
	var opts []option.RequestOption
	if req.Session != "" {
		if o.openCode {
			opts = append(opts, option.WithHeader(OpenCodeSessionHeader, req.Session))
		}
		for k, v := range o.sessionHeaders {
			opts = append(opts, option.WithHeader(k, strings.ReplaceAll(v, SessionPlaceholder, req.Session)))
		}
	}
	if o.openRouter {
		// OpenRouter walks the models list itself, primary first. The
		// top-level cache_control is its automatic prompt caching: a
		// breakpoint on the last cacheable block that moves forward as the
		// conversation grows. Without it, providers whose caching is not
		// automatic (Anthropic, Gemini) cache none of a tool loop's steps.
		opts = append(opts, option.WithJSONSet("usage", map[string]any{"include": true}))
		if !req.Once {
			opts = append(opts, option.WithJSONSet("cache_control", map[string]any{"type": "ephemeral"}))
		}
		if len(req.Fallbacks) > 0 {
			opts = append(opts, option.WithJSONSet("models", append([]string{req.Model}, req.Fallbacks...)))
		}
		if req.Effort != "" {
			// OpenRouter's own reasoning parameter, which it maps to the
			// nearest level each model in the list takes.
			opts = append(opts, option.WithJSONSet("reasoning", map[string]any{"effort": string(req.Effort)}))
		}
		resp, err := o.step(ctx, params, req.Model, "", req.Fallbacks, opts...)
		if err != nil {
			return StepResponse{}, fmt.Errorf("model: %s: %w", req.Model, err)
		}
		return resp, nil
	}
	return eachModel(ctx, req, func(id string) (StepResponse, error) {
		var alias string
		if !o.openCode {
			var err error
			if id, alias, err = resolve(id, func(family string) (string, error) { return o.resolveLatest(ctx, family) }); err != nil {
				return StepResponse{}, err
			}
		}
		return o.step(ctx, params, id, alias, nil, opts...)
	})
}

// step sends modelID, with the fallbacks that OpenRouter can answer with
// instead. If modelID has no price of its own, step prices it under alias,
// the floating alias that selected it.
func (o *OpenAI) step(
	ctx context.Context, params openai.ChatCompletionNewParams, modelID, alias string, fallbacks []string, opts ...option.RequestOption,
) (StepResponse, error) {
	params.Model = modelID
	cc, err := o.client.Chat.Completions.New(ctx, params, opts...)
	if apiErr, ok := errors.AsType[*openai.Error](err); ok && apiErr.StatusCode == http.StatusTooManyRequests && apiErr.Code == BudgetCode {
		return StepResponse{}, fmt.Errorf("%w: %s", ErrBudget, apiErr.Message)
	}
	if err != nil {
		return StepResponse{}, openAIError(err)
	}
	if len(cc.Choices) == 0 {
		return StepResponse{}, errors.New("response has no choices")
	}
	msg := cc.Choices[0].Message
	out := StepResponse{Text: msg.Content, Stop: openAIStop(cc.Choices[0].FinishReason), Model: modelID}
	if o.reportsModel && cc.Model != "" {
		// OpenRouter's server-side fallback can answer with another model in
		// the list, and the gateway answers for a model that the runner names
		// only as "review". Both responses say which model answered. Another
		// provider's model field names a dated snapshot of the one asked for.
		// That is not what an admin configured, so step does not use it.
		out.Model = cc.Model
	}
	for _, tc := range msg.ToolCalls {
		if tc.Type != "function" {
			continue
		}
		args := cmp.Or(tc.Function.Arguments, "{}")
		out.ToolCalls = append(out.ToolCalls, ToolCall{ID: tc.ID, Name: tc.Function.Name, Input: json.RawMessage(args)})
	}

	u := cc.Usage
	cached, written := u.PromptTokensDetails.CachedTokens, u.PromptTokensDetails.CacheWriteTokens
	out.Usage = Usage{Input: max(u.PromptTokens-cached-written, 0), CacheRead: cached, CacheWrite: written, Output: u.CompletionTokens}

	// Cost and the serving provider are OpenRouter's additions to the
	// response, which the SDK's types do not carry. The cost is read raw:
	// it only prices the call, so a shape reportedCost cannot read must not
	// fail the step.
	var extra struct {
		Provider string `json:"provider"`
		Usage    struct {
			Cost json.RawMessage `json:"cost"`
		} `json:"usage"`
		chatGPTCarry
	}
	if raw := cc.RawJSON(); raw != "" {
		if err := json.Unmarshal([]byte(raw), &extra); err != nil {
			return StepResponse{}, fmt.Errorf("decode response: %w", err)
		}
	}
	out.Upstream = extra.Provider
	if o.gateway {
		out.ChatGPTOutput = extra.ChatGPTOutput
	}
	if cost, ok := reportedCost(extra.Usage.Cost); ok {
		out.CostUSD = cost
	} else {
		out.CostUSD, out.Unpriced = o.price(out.Model, modelID, alias, fallbacks, out.Usage)
	}
	return out, nil
}

// reportedCost is the cost a response's usage reports, and whether it
// reports one: a number, as OpenRouter and kritika's gateway send it, or
// the total_cost of an object, as Bifrost passes a provider's cost on. Any
// other shape, an object without a total included, reports none.
func reportedCost(raw json.RawMessage) (float64, bool) {
	var cost *float64
	if json.Unmarshal(raw, &cost) == nil && cost != nil {
		return *cost, true
	}
	var obj struct {
		TotalCost *float64 `json:"total_cost"`
	}
	if json.Unmarshal(raw, &obj) == nil && obj.TotalCost != nil {
		return *obj.TotalCost, true
	}
	return 0, false
}

// price is what a call costs when the provider reported no cost for it. It is
// the price of the model that answered, else that of the model asked for or
// its alias. Those apply only where the model that answered can be no other
// than the one asked for. That model can answer under the id that an alias
// selected, or under another name for it. A request that lists fallbacks
// cannot tell that case apart from one of the fallbacks answering.
func (o *OpenAI) price(served, asked, alias string, fallbacks []string, u Usage) (float64, bool) {
	keys := []string{served}
	if served == asked || len(fallbacks) == 0 {
		keys = append(keys, asked, alias)
	}
	for _, key := range keys {
		if price, ok := o.pricing[key]; ok && key != "" {
			return price.Cost(u), false
		}
	}
	return 0, true
}

// openAIError adds the body that a server refused a request with to the SDK's
// error, whose own text is only the status. The body says why.
func openAIError(err error) error {
	if e, ok := errors.AsType[*openai.Error](err); ok && e.RawJSON() != "" {
		return fmt.Errorf("%w %s", err, e.RawJSON())
	}
	return err
}

func openAIStop(finish string) StopReason {
	switch finish {
	case "stop":
		return StopEndTurn
	case "tool_calls", "function_call":
		return StopToolUse
	case "length":
		return StopMaxTokens
	default:
		return StopOther
	}
}

// params maps everything but the model, which each attempt sets.
func (o *OpenAI) params(req StepRequest) (openai.ChatCompletionNewParams, error) {
	var p openai.ChatCompletionNewParams
	if req.System != "" {
		p.Messages = append(p.Messages, openai.SystemMessage(req.System))
	}
	for _, m := range req.Messages {
		messages := openAIMessages(m)
		if o.gateway && m.Role == RoleAssistant && m.ChatGPTOutput != nil {
			messages[0].OfAssistant.SetExtraFields(map[string]any{chatGPTOutputField: m.ChatGPTOutput})
		}
		p.Messages = append(p.Messages, messages...)
	}
	if req.MaxTokens > 0 {
		// OpenAI's own API deprecated max_tokens. OpenRouter documents only
		// max_tokens.
		if o.openRouter {
			p.MaxTokens = openai.Int(req.MaxTokens)
		} else {
			p.MaxCompletionTokens = openai.Int(req.MaxTokens)
		}
	}
	if req.Effort != "" && !o.openRouter {
		p.ReasoningEffort = shared.ReasoningEffort(req.Effort)
	}
	if len(req.Tools) == 0 {
		return p, nil
	}
	for _, t := range req.Tools {
		fn := shared.FunctionDefinitionParam{Name: t.Name}
		if t.Description != "" {
			fn.Description = openai.String(t.Description)
		}
		if len(t.InputSchema) > 0 {
			if err := json.Unmarshal(t.InputSchema, &fn.Parameters); err != nil {
				return p, fmt.Errorf("model: tool %s: input schema: %w", t.Name, err)
			}
		}
		p.Tools = append(p.Tools, openai.ChatCompletionFunctionTool(fn))
	}
	switch req.ToolChoice.Mode {
	case ToolChoiceRequired:
		p.ToolChoice.OfAuto = openai.String(string(openai.ChatCompletionToolChoiceOptionAutoRequired))
	case ToolChoiceTool:
		p.ToolChoice = openai.ToolChoiceOptionFunctionToolChoice(openai.ChatCompletionNamedToolChoiceFunctionParam{Name: req.ToolChoice.Name})
	default:
		p.ToolChoice.OfAuto = openai.String(string(openai.ChatCompletionToolChoiceOptionAutoAuto))
	}
	return p, nil
}

// openAIMessages maps one message. Tool results become one tool message
// each, ahead of any text, since they must follow the assistant's calls.
func openAIMessages(m Message) []openai.ChatCompletionMessageParamUnion {
	if m.Role == RoleAssistant {
		var a openai.ChatCompletionAssistantMessageParam
		if m.Text != "" {
			a.Content.OfString = openai.String(m.Text)
		}
		for _, c := range m.ToolCalls {
			args := cmp.Or(string(c.Input), "{}")
			a.ToolCalls = append(a.ToolCalls, openai.ChatCompletionMessageToolCallUnionParam{
				OfFunction: &openai.ChatCompletionMessageFunctionToolCallParam{
					ID: c.ID, Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{Name: c.Name, Arguments: args},
				},
			})
		}
		return []openai.ChatCompletionMessageParamUnion{{OfAssistant: &a}}
	}
	out := make([]openai.ChatCompletionMessageParamUnion, 0, len(m.ToolResults)+1)
	for _, r := range m.ToolResults {
		content := r.Content
		if r.IsError {
			// Chat completions has no error flag on a tool message.
			content = toolErrorPrefix + content
		}
		out = append(out, openai.ToolMessage(content, r.CallID))
	}
	if m.Text != "" || len(m.ToolResults) == 0 {
		out = append(out, openai.UserMessage(m.Text))
	}
	return out
}
