package tuning

import (
	"fmt"
	"time"
)

// Model holds the tunable values of package model.
type Model struct {
	// AnthropicMaxTokens is the output cap an Anthropic Messages request
	// gets when its step sets none: the API requires one.
	AnthropicMaxTokens int64 `env:"KRITIKA_TUNING_MODEL_ANTHROPIC_MAX_TOKENS" envDefault:"8192" yaml:"anthropicMaxTokens,omitempty"`
	// CatalogTTL is how long a provider's model catalog, which resolves a
	// floating ~<family>-latest alias, is used before it is read again.
	CatalogTTL time.Duration `env:"KRITIKA_TUNING_MODEL_CATALOG_TTL" envDefault:"5m" yaml:"catalogTtl,omitempty"`
	// CatalogTimeout bounds one read of a provider's model catalog.
	CatalogTimeout time.Duration `env:"KRITIKA_TUNING_MODEL_CATALOG_TIMEOUT" envDefault:"30s" yaml:"catalogTimeout,omitempty"`
	// CatalogRetry is how long a catalog that failed to refresh stays in use
	// before the next refresh: shorter than CatalogTTL, so a passing
	// failure heals soon, and long enough not to hammer a provider that is
	// down.
	CatalogRetry time.Duration `env:"KRITIKA_TUNING_MODEL_CATALOG_RETRY" envDefault:"1m" yaml:"catalogRetry,omitempty"`
	// DefaultEmbedMaxBatch is the most texts one embeddings request sends:
	// the default when the configuration file does not set
	// embedding.maxBatch. It and the two below are conservative enough for
	// the common OpenAI-compatible servers, which differ widely in what they
	// accept.
	DefaultEmbedMaxBatch int `env:"KRITIKA_TUNING_MODEL_DEFAULT_EMBED_MAX_BATCH" envDefault:"64" yaml:"defaultEmbedMaxBatch,omitempty"`
	// DefaultEmbedMaxBatchChars is the most characters one embeddings
	// request sends in all: the default when the configuration file does
	// not set embedding.maxBatchChars.
	DefaultEmbedMaxBatchChars int `env:"KRITIKA_TUNING_MODEL_DEFAULT_EMBED_MAX_BATCH_CHARS" envDefault:"200000" yaml:"defaultEmbedMaxBatchChars,omitempty"` //nolint:lll // a struct tag does not wrap
	// DefaultEmbedMaxItemChars is the most characters of one text an
	// embeddings request sends, the rest cut: the default when the
	// configuration file does not set embedding.maxItemChars.
	DefaultEmbedMaxItemChars int `env:"KRITIKA_TUNING_MODEL_DEFAULT_EMBED_MAX_ITEM_CHARS" envDefault:"16000" yaml:"defaultEmbedMaxItemChars,omitempty"` //nolint:lll // a struct tag does not wrap
	// EmbedTimeout bounds one embeddings request, a batch of at most
	// DefaultEmbedMaxBatch (or embedding.maxBatch) short texts.
	EmbedTimeout time.Duration `env:"KRITIKA_TUNING_MODEL_EMBED_TIMEOUT" envDefault:"1m" yaml:"embedTimeout,omitempty"`
	// PlanPause is how long the adapter sends nothing on a ChatGPT plan
	// after the plan refused a step for its usage limit. The refusal says
	// nothing of when the limit resets, and a step sent meanwhile costs the
	// plan nothing but fails.
	PlanPause time.Duration `env:"KRITIKA_TUNING_MODEL_PLAN_PAUSE" envDefault:"15m" yaml:"planPause,omitempty"`
	// PlanErrorBodyBytes is how much of the body of a ChatGPT plan's refusal
	// before the Responses layer, admission or routing, the error quotes.
	PlanErrorBodyBytes ByteSize `env:"KRITIKA_TUNING_MODEL_PLAN_ERROR_BODY_BYTES" envDefault:"4Ki" yaml:"planErrorBodyBytes,omitempty"`
	// DefaultStepTimeout bounds one request to a provider for a step: the
	// default when the configuration file does not set
	// providers.<name>.timeout, and at most configFile.maxProviderTimeout.
	// It is long enough for a step's whole output from a slow model. If a
	// provider takes the request and never answers, the step fails as a
	// timeout, which is Transient, so the gateway's retries and its
	// fallback provider still get their turn.
	DefaultStepTimeout time.Duration `env:"KRITIKA_TUNING_MODEL_DEFAULT_STEP_TIMEOUT" envDefault:"5m" yaml:"defaultStepTimeout,omitempty"`
	// StepBudgetWaits is what a step's budget (model.StepBudget) adds to its
	// provider's and its fallback's request timeouts for the waits between
	// their attempts.
	StepBudgetWaits time.Duration `env:"KRITIKA_TUNING_MODEL_STEP_BUDGET_WAITS" envDefault:"2m" yaml:"stepBudgetWaits,omitempty"`
	// GatewaySettle is what the gateway can take to charge and record an
	// answered step before it writes the answer, which the runner's request
	// to the gateway waits for on top of the step's budget. It covers
	// adapter.chargeTimeout, adapter.recordTimeout and
	// adapter.reservationPad.
	GatewaySettle time.Duration `env:"KRITIKA_TUNING_MODEL_GATEWAY_SETTLE" envDefault:"5m" yaml:"gatewaySettle,omitempty"`
	// RetryWaitCeiling bounds the wait a provider asks for before it becomes
	// a Duration, which a huge number of seconds overflows. Callers wait far
	// less.
	RetryWaitCeiling time.Duration `env:"KRITIKA_TUNING_MODEL_RETRY_WAIT_CEILING" envDefault:"1h" yaml:"retryWaitCeiling,omitempty"`
	// MaxRerankAnswer bounds a rerank answer, which carries a score, and
	// with some APIs a document, for each document sent.
	MaxRerankAnswer ByteSize `env:"KRITIKA_TUNING_MODEL_MAX_RERANK_ANSWER" envDefault:"8Mi" yaml:"maxRerankAnswer,omitempty"`
}

// Agent holds the tunable values of package agent.
type Agent struct {
	// ContinueTokens is the most that a conversation can hold, in tokens,
	// for a later Run to carry it on. Every step of that Run reads the
	// whole of it again. So past this limit, a fresh Run with a handoff
	// costs less, even with the provider's cache warm.
	ContinueTokens int64 `env:"KRITIKA_TUNING_AGENT_CONTINUE_TOKENS" envDefault:"100000" yaml:"continueTokens,omitempty"`
	// MaxCheckoutBytes bounds a checkout's total size on the runner pod's
	// scratch volume, beside the fetched pack.
	MaxCheckoutBytes ByteSize `env:"KRITIKA_TUNING_AGENT_MAX_CHECKOUT_BYTES" envDefault:"256Mi" yaml:"maxCheckoutBytes,omitempty"`
	// BinarySniffBytes is how many bytes at the start of a file the agent's
	// tools search for a NUL byte to decide that the file is binary.
	BinarySniffBytes ByteSize `env:"KRITIKA_TUNING_AGENT_BINARY_SNIFF_BYTES" envDefault:"8Ki" yaml:"binarySniffBytes,omitempty"`
	// MaxSources is how many URLs one run of the run tool records as the
	// review's sources.
	MaxSources int `env:"KRITIKA_TUNING_AGENT_MAX_SOURCES" envDefault:"50" yaml:"maxSources,omitempty"`
	// MaxSourceBytes is how long a recorded source URL can be.
	MaxSourceBytes ByteSize `env:"KRITIKA_TUNING_AGENT_MAX_SOURCE_BYTES" envDefault:"2Ki" yaml:"maxSourceBytes,omitempty"`
	// SetupErrorBytes is how much of a command's setup failure, which the
	// review summary shows, is kept.
	SetupErrorBytes ByteSize `env:"KRITIKA_TUNING_AGENT_SETUP_ERROR_BYTES" envDefault:"2Ki" yaml:"setupErrorBytes,omitempty"`
	// StderrShare divides a command's output limit for the end of its
	// stderr that a cut output keeps: an error printed after a long output
	// lands there.
	StderrShare int `env:"KRITIKA_TUNING_AGENT_STDERR_SHARE" envDefault:"8" yaml:"stderrShare,omitempty"`
	// StderrAppendShare divides a command's output limit for the most that
	// the kept end of stderr, with its lead, can take when it follows a cut
	// output's notes. Longer, it is left out. At most StderrShare, so the
	// end that StderrShare keeps fits.
	StderrAppendShare int `env:"KRITIKA_TUNING_AGENT_STDERR_APPEND_SHARE" envDefault:"4" yaml:"stderrAppendShare,omitempty"`
	// CommandWaitDelay is how long the run tool waits, once a command is
	// killed, for a child that still holds its output open.
	CommandWaitDelay time.Duration `env:"KRITIKA_TUNING_AGENT_COMMAND_WAIT_DELAY" envDefault:"1s" yaml:"commandWaitDelay,omitempty"`
	// DefaultGrepMaxResults is how many matches grep returns when its
	// max_results input is unset or not positive. At most GrepMaxResultsCap.
	DefaultGrepMaxResults int `env:"KRITIKA_TUNING_AGENT_DEFAULT_GREP_MAX_RESULTS" envDefault:"100" yaml:"defaultGrepMaxResults,omitempty"`
	// GrepMaxResultsCap is the most matches grep returns: a larger
	// max_results is clamped to it. The tool's description states it.
	GrepMaxResultsCap int `env:"KRITIKA_TUNING_AGENT_GREP_MAX_RESULTS_CAP" envDefault:"500" yaml:"grepMaxResultsCap,omitempty"`
	// GrepContextLinesCap is the most lines of context grep returns before
	// and after a match. The tool's description states it.
	GrepContextLinesCap int `env:"KRITIKA_TUNING_AGENT_GREP_CONTEXT_LINES_CAP" envDefault:"10" yaml:"grepContextLinesCap,omitempty"`
	// MaxBlobBytes bounds how large a blob the agent reads: read_file
	// rejects an oversized blob with a tool error, grep skips it and keeps
	// walking, and a checkout leaves it out.
	MaxBlobBytes ByteSize `env:"KRITIKA_TUNING_AGENT_MAX_BLOB_BYTES" envDefault:"1Mi" yaml:"maxBlobBytes,omitempty"`
	// DefaultMaxSteps is how many steps a Run takes: the default when the
	// configuration file does not set agent.steps.
	DefaultMaxSteps int `env:"KRITIKA_TUNING_AGENT_DEFAULT_MAX_STEPS" envDefault:"60" yaml:"defaultMaxSteps,omitempty"`
	// DefaultMaxToolOutputBytes is how much of a tool's output a Run keeps:
	// the default when the configuration file does not set agent.output.
	DefaultMaxToolOutputBytes ByteSize `env:"KRITIKA_TUNING_AGENT_DEFAULT_MAX_TOOL_OUTPUT_BYTES" envDefault:"32Ki" yaml:"defaultMaxToolOutputBytes,omitempty"` //nolint:lll // a struct tag does not wrap
	// DefaultMaxTokens is a Run's prompt and output token budget, which
	// forces an early submission: the default when the configuration file
	// does not set agent.tokens.
	DefaultMaxTokens int64 `env:"KRITIKA_TUNING_AGENT_DEFAULT_MAX_TOKENS" envDefault:"6000000" yaml:"defaultMaxTokens,omitempty"`
	// MaxOutputTokensPerStep is the most output tokens one step may produce,
	// which the gateway also clamps a runner's request to.
	MaxOutputTokensPerStep int64 `env:"KRITIKA_TUNING_AGENT_MAX_OUTPUT_TOKENS_PER_STEP" envDefault:"16384" yaml:"maxOutputTokensPerStep,omitempty"` //nolint:lll // a struct tag does not wrap
	// EmptyRetries is how many steps in a row can end with neither text nor
	// a tool call and each get a nudge. These nudges do not spend the
	// once-per-Run nudge. A reasoning model on the ChatGPT route sometimes
	// ends the step before its submission this way, and the nudge
	// recovered every one. The once-per-Run nudge is kept for a turn that
	// answers in prose. One more such step is taken as a turn with no tool
	// call.
	EmptyRetries int `env:"KRITIKA_TUNING_AGENT_EMPTY_RETRIES" envDefault:"2" yaml:"emptyRetries,omitempty" tuning:"zero"`
	// CutOffRetries is how many steps in a row the output cap can cut off
	// before any tool call. The model is still told to go on after each of
	// them. One more is taken as a turn with no tool call: it is better to
	// ask a model that cannot fit a step into the cap for what it has.
	CutOffRetries int `env:"KRITIKA_TUNING_AGENT_CUT_OFF_RETRIES" envDefault:"2" yaml:"cutOffRetries,omitempty" tuning:"zero"`
	// ForcedRetries is how many more steps a model gets after it was told
	// to submit and did not. Each of these steps tells it again. A rejected
	// submission goes back with its error, prose and other tool calls with
	// a note that only the submission is available. The steps spent
	// exploring are worth more than one slip at the end. The token budget
	// bounds the retries where the step cap does not.
	ForcedRetries int `env:"KRITIKA_TUNING_AGENT_FORCED_RETRIES" envDefault:"2" yaml:"forcedRetries,omitempty" tuning:"zero"`
	// WrapUpFloor is the least time that a Run keeps, before its context's
	// deadline, for the steps that it tells the model to submit on, unless
	// the Run's whole time divided by WrapUpTotalShare is less. A
	// submission writes the whole answer, so it can take longer than any
	// step before it.
	WrapUpFloor time.Duration `env:"KRITIKA_TUNING_AGENT_WRAP_UP_FLOOR" envDefault:"2m" yaml:"wrapUpFloor,omitempty"`
	// WrapUpAnswers is how many of a Run's slowest answers so far it keeps
	// time for before its deadline: one for the submission and one for a
	// retry.
	WrapUpAnswers int `env:"KRITIKA_TUNING_AGENT_WRAP_UP_ANSWERS" envDefault:"2" yaml:"wrapUpAnswers,omitempty"`
	// WrapUpTotalShare divides a Run's whole time for the most that
	// WrapUpFloor keeps of it, so a short Run is not told to submit at
	// once.
	WrapUpTotalShare int `env:"KRITIKA_TUNING_AGENT_WRAP_UP_TOTAL_SHARE" envDefault:"4" yaml:"wrapUpTotalShare,omitempty"`
	// WrapUpTokenShare is the share of a Run's token budget whose spending
	// tells the model to submit with what it has. At 1 or more, only the
	// budget spent whole does.
	WrapUpTokenShare float64 `env:"KRITIKA_TUNING_AGENT_WRAP_UP_TOKEN_SHARE" envDefault:"0.9" yaml:"wrapUpTokenShare,omitempty"`
	// StepRetryWaits are the waits before a step that failed transiently
	// is sent again, one for each retry, while the Run's context lives. The
	// Stepper has already retried on its own schedule, so these outlast a
	// provider outage that schedule did not: the conversation so far is
	// kept rather than the Run ending with its steps spent.
	StepRetryWaits Durations `env:"KRITIKA_TUNING_AGENT_STEP_RETRY_WAITS" envDefault:"30s,1m,2m,4m" yaml:"stepRetryWaits,omitempty"`
}

// ChatGPT holds the tunable values of package chatgpt.
type ChatGPT struct {
	// RefreshLead is how long before a ChatGPT plan's access token expires
	// it is renewed. So a step's request never goes out with a token about
	// to lapse under it.
	RefreshLead time.Duration `env:"KRITIKA_TUNING_CHATGPT_REFRESH_LEAD" envDefault:"5m" yaml:"refreshLead,omitempty"`
	// ClientTimeout bounds one request of an HTTP client that the package
	// makes itself, a sign-in's or a refresh's.
	ClientTimeout time.Duration `env:"KRITIKA_TUNING_CHATGPT_CLIENT_TIMEOUT" envDefault:"30s" yaml:"clientTimeout,omitempty"`
	// MaxTokenBody bounds the token endpoint's answer to a refresh.
	MaxTokenBody ByteSize `env:"KRITIKA_TUNING_CHATGPT_MAX_TOKEN_BODY" envDefault:"1Mi" yaml:"maxTokenBody,omitempty"`
	// CallbackReadHeaderTimeout bounds reading a request's headers on the
	// sign-in's local callback server.
	CallbackReadHeaderTimeout time.Duration `env:"KRITIKA_TUNING_CHATGPT_CALLBACK_READ_HEADER_TIMEOUT" envDefault:"10s" yaml:"callbackReadHeaderTimeout,omitempty"` //nolint:lll // a struct tag does not wrap
	// LoginTimeout bounds a kritika chatgpt login, the wait for the
	// person to sign in included.
	LoginTimeout time.Duration `env:"KRITIKA_TUNING_CHATGPT_LOGIN_TIMEOUT" envDefault:"10m" yaml:"loginTimeout,omitempty"`
	// CallbackCloseTimeout bounds the callback server's graceful shutdown
	// once the sign-in is over, after which it is closed.
	CallbackCloseTimeout time.Duration `env:"KRITIKA_TUNING_CHATGPT_CALLBACK_CLOSE_TIMEOUT" envDefault:"1s" yaml:"callbackCloseTimeout,omitempty"` //nolint:lll // a struct tag does not wrap
}

// Adapter holds the tunable values of package adapter.
type Adapter struct {
	// ReservationPad is how long past a step's deadline its model
	// invocation's reservation stays held before a sweep may release it,
	// so a step that answered at its deadline is still charged against it.
	// At most model.gatewaySettle.
	ReservationPad time.Duration `env:"KRITIKA_TUNING_ADAPTER_RESERVATION_PAD" envDefault:"2m" yaml:"reservationPad,omitempty"`
	// ChargeTimeout bounds saving an answered step's usage and settling it,
	// on a context of its own, so a step whose caller is gone is still
	// accounted for. With RecordTimeout, at most model.gatewaySettle.
	ChargeTimeout time.Duration `env:"KRITIKA_TUNING_ADAPTER_CHARGE_TIMEOUT" envDefault:"2m" yaml:"chargeTimeout,omitempty"`
	// RecordTimeout bounds recording one model call. Recording is best
	// effort: the transcript view loses a turn rather than a review, or a
	// runner's step, that waits on it.
	RecordTimeout time.Duration `env:"KRITIKA_TUNING_ADAPTER_RECORD_TIMEOUT" envDefault:"2s" yaml:"recordTimeout,omitempty"`
	// LeaseReleaseTimeout bounds letting a fallback's slot go once the step
	// is over, whose context is usually done by then.
	LeaseReleaseTimeout time.Duration `env:"KRITIKA_TUNING_ADAPTER_LEASE_RELEASE_TIMEOUT" envDefault:"10s" yaml:"leaseReleaseTimeout,omitempty"` //nolint:lll // a struct tag does not wrap
	// RetryMin is the wait before a step that failed in a way another
	// attempt can avoid is tried again, doubled each time up to RetryMax.
	RetryMin time.Duration `env:"KRITIKA_TUNING_ADAPTER_RETRY_MIN" envDefault:"1s" yaml:"retryMin,omitempty"`
	// RetryMax is the longest of the doubling waits between a step's
	// attempts. At least RetryMin.
	RetryMax time.Duration `env:"KRITIKA_TUNING_ADAPTER_RETRY_MAX" envDefault:"30s" yaml:"retryMax,omitempty"`
	// RetryAfterMax is the longest Retry-After of a provider's that a step
	// honors: a longer one sleeps the step past its caller's deadline with
	// the remaining retries and the fallback untried.
	RetryAfterMax time.Duration `env:"KRITIKA_TUNING_ADAPTER_RETRY_AFTER_MAX" envDefault:"1m" yaml:"retryAfterMax,omitempty"`
	// PlanTokenTTL bounds how long a replica keeps using a ChatGPT plan's
	// access token that it read without reading it again. So a new sign-in
	// reaches every replica within it.
	PlanTokenTTL time.Duration `env:"KRITIKA_TUNING_ADAPTER_PLAN_TOKEN_TTL" envDefault:"1m" yaml:"planTokenTtl,omitempty"`
}

func init() {
	crossField = append(crossField,
		func(v Values) error {
			// A batch of one item is bound by the batch cap too.
			return notAbove("model.defaultEmbedMaxItemChars", v.Model.DefaultEmbedMaxItemChars,
				"model.defaultEmbedMaxBatchChars", v.Model.DefaultEmbedMaxBatchChars)
		},
		func(v Values) error {
			return notAbove("model.catalogRetry", v.Model.CatalogRetry, "model.catalogTtl", v.Model.CatalogTTL)
		},
		func(v Values) error {
			return notAbove("adapter.reservationPad", v.Adapter.ReservationPad, "model.gatewaySettle", v.Model.GatewaySettle)
		},
		func(v Values) error {
			if a := v.Adapter; a.ChargeTimeout+a.RecordTimeout > v.Model.GatewaySettle {
				return fmt.Errorf("%s and %s together %w %s", fieldLabel("adapter.chargeTimeout"), fieldLabel("adapter.recordTimeout"),
					errMustNotExceed, fieldLabel("model.gatewaySettle"))
			}
			return nil
		},
		func(v Values) error {
			return notAbove("adapter.retryMin", v.Adapter.RetryMin, "adapter.retryMax", v.Adapter.RetryMax)
		},
		func(v Values) error {
			return notAbove("agent.defaultGrepMaxResults", v.Agent.DefaultGrepMaxResults, "agent.grepMaxResultsCap", v.Agent.GrepMaxResultsCap)
		},
		func(v Values) error {
			return notAbove("agent.stderrAppendShare", v.Agent.StderrAppendShare, "agent.stderrShare", v.Agent.StderrShare)
		},
		func(v Values) error {
			return notAbove("model.defaultStepTimeout", v.Model.DefaultStepTimeout, "configFile.maxProviderTimeout", v.ConfigFile.MaxProviderTimeout)
		},
	)
}
