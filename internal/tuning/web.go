package tuning

import (
	"fmt"
	"time"
)

// WebAPI holds the tunable values of package webapi.
type WebAPI struct {
	// MaxBodyBytes bounds a request body.
	MaxBodyBytes ByteSize `env:"KRITIKA_TUNING_WEB_API_MAX_BODY_BYTES" envDefault:"1Mi" yaml:"maxBodyBytes,omitempty"`
	// DefaultLimit is how many items a list returns without ?limit=, or
	// an MCP tool's list without limit. At most MaxLimit. The MCP tools'
	// descriptions state it.
	DefaultLimit int `env:"KRITIKA_TUNING_WEB_API_DEFAULT_LIMIT" envDefault:"50" yaml:"defaultLimit,omitempty"`
	// MaxLimit is the most items a list returns, whatever ?limit= asks.
	MaxLimit int `env:"KRITIKA_TUNING_WEB_API_MAX_LIMIT" envDefault:"200" yaml:"maxLimit,omitempty"`
	// HeartbeatInterval is how often an idle event stream carries a
	// comment, so that proxies and the browser keep it open.
	HeartbeatInterval time.Duration `env:"KRITIKA_TUNING_WEB_API_HEARTBEAT_INTERVAL" envDefault:"25s" yaml:"heartbeatInterval,omitempty"`
	// ClientBuffer is how many events a slow stream's client can fall
	// behind before it is told to resync instead.
	ClientBuffer int `env:"KRITIKA_TUNING_WEB_API_CLIENT_BUFFER" envDefault:"64" yaml:"clientBuffer,omitempty"`
	// WriteTimeout bounds each write to an event stream, so a client that
	// stops reading is dropped rather than holding its goroutine forever.
	WriteTimeout time.Duration `env:"KRITIKA_TUNING_WEB_API_WRITE_TIMEOUT" envDefault:"30s" yaml:"writeTimeout,omitempty"`
	// PullFollowups bounds the follow-ups a pull request's detail lists.
	PullFollowups int `env:"KRITIKA_TUNING_WEB_API_PULL_FOLLOWUPS" envDefault:"200" yaml:"pullFollowups,omitempty"`
	// RulesRepoPage is how many repositories one read of the rules list
	// takes.
	RulesRepoPage int `env:"KRITIKA_TUNING_WEB_API_RULES_REPO_PAGE" envDefault:"500" yaml:"rulesRepoPage,omitempty"`
	// RecentIndexRuns is how many index runs a repository's detail lists.
	RecentIndexRuns int `env:"KRITIKA_TUNING_WEB_API_RECENT_INDEX_RUNS" envDefault:"20" yaml:"recentIndexRuns,omitempty"`
	// IndexChunkLimit bounds the chunks one file's index listing returns.
	IndexChunkLimit int `env:"KRITIKA_TUNING_WEB_API_INDEX_CHUNK_LIMIT" envDefault:"200" yaml:"indexChunkLimit,omitempty"`
	// AnalyticsRepos is how many repositories the analytics list.
	AnalyticsRepos int `env:"KRITIKA_TUNING_WEB_API_ANALYTICS_REPOS" envDefault:"5" yaml:"analyticsRepos,omitempty"`
	// AnalyticsRefusals is how many refused egress hosts the analytics
	// list, each with a row per repository.
	AnalyticsRefusals int `env:"KRITIKA_TUNING_WEB_API_ANALYTICS_REFUSALS" envDefault:"50" yaml:"analyticsRefusals,omitempty"`
	// AnalyticsDependencies is how many drifting dependencies the analytics
	// list.
	AnalyticsDependencies int `env:"KRITIKA_TUNING_WEB_API_ANALYTICS_DEPENDENCIES" envDefault:"50" yaml:"analyticsDependencies,omitempty"`
	// TopTools is how many of the agents' most run tools the tool usage
	// lists.
	TopTools int `env:"KRITIKA_TUNING_WEB_API_TOP_TOOLS" envDefault:"10" yaml:"topTools,omitempty"`
	// DefaultUsageWindow is the span that a usage or analytics series
	// covers without ?from=. At most MaxUsageWindow.
	DefaultUsageWindow time.Duration `env:"KRITIKA_TUNING_WEB_API_DEFAULT_USAGE_WINDOW" envDefault:"720h" yaml:"defaultUsageWindow,omitempty"`
	// MaxUsageWindow is the most that ?from= and ?to= can span. The series
	// are read from every row in the span, and the analytics page reads
	// the span before it too. So a span of years scans the account's
	// history twice on one request.
	MaxUsageWindow time.Duration `env:"KRITIKA_TUNING_WEB_API_MAX_USAGE_WINDOW" envDefault:"8784h" yaml:"maxUsageWindow,omitempty"`
	// TokenNameMax is the longest name that a personal access token can
	// have, in characters. The dashboard's token form checks the default,
	// 100, on its own.
	TokenNameMax int `env:"KRITIKA_TUNING_WEB_API_TOKEN_NAME_MAX" envDefault:"100" yaml:"tokenNameMax,omitempty"`
	// TokenLifetimes are the lifetimes that a personal access token can be
	// created with, 0 for one that never expires. A request asks for whole
	// days, so Validate refuses a lifetime that is not whole days. An empty
	// list, [] in the file and in the environment alike, turns the creation
	// of tokens off: an empty variable is unset, and so the default. The
	// dashboard's token form offers the default lifetimes on its own.
	TokenLifetimes Durations `env:"KRITIKA_TUNING_WEB_API_TOKEN_LIFETIMES" envDefault:"0s,168h,720h,2160h,8760h" yaml:"tokenLifetimes,omitempty" tuning:"zero"` //nolint:lll // a struct tag does not wrap
	// ExportLimit is how many findings an export holds at most: the
	// newest.
	ExportLimit int `env:"KRITIKA_TUNING_WEB_API_EXPORT_LIMIT" envDefault:"1000" yaml:"exportLimit,omitempty"`
}

// Server holds the tunable values of package server.
type Server struct {
	// StartingRetry is how long a caller of a starting replica is told to
	// wait, and how often its page reloads, in whole seconds: the store's
	// own retry interval, so a browser's refresh lands just after the next
	// attempt.
	StartingRetry time.Duration `env:"KRITIKA_TUNING_SERVER_STARTING_RETRY" envDefault:"5s" yaml:"startingRetry,omitempty"`
	// ShutdownTimeout bounds how long an in-flight request to the
	// management listener can hold up process exit once the root context
	// is canceled.
	ShutdownTimeout time.Duration `env:"KRITIKA_TUNING_SERVER_SHUTDOWN_TIMEOUT" envDefault:"10s" yaml:"shutdownTimeout,omitempty"`
	// ReadHeaderTimeout bounds reading one request's headers.
	ReadHeaderTimeout time.Duration `env:"KRITIKA_TUNING_SERVER_READ_HEADER_TIMEOUT" envDefault:"10s" yaml:"readHeaderTimeout,omitempty"`
	// ReadTimeout bounds reading one request, body included, so a client
	// that trickles a body does not hold its connection open. It does not
	// bound the handler: an event stream or a model step runs on past it.
	ReadTimeout time.Duration `env:"KRITIKA_TUNING_SERVER_READ_TIMEOUT" envDefault:"1m" yaml:"readTimeout,omitempty"`
	// IdleTimeout is how long a keep-alive connection can wait for its
	// next request.
	IdleTimeout time.Duration `env:"KRITIKA_TUNING_SERVER_IDLE_TIMEOUT" envDefault:"2m" yaml:"idleTimeout,omitempty"`
}

// Auth holds the tunable values of package auth.
type Auth struct {
	// MaxAPIBody bounds how much of a forge API response a sign-in reads.
	MaxAPIBody ByteSize `env:"KRITIKA_TUNING_AUTH_MAX_API_BODY" envDefault:"1Mi" yaml:"maxApiBody,omitempty"`
	// GitHubMaxPages bounds how many pages of organizations or teams, a
	// hundred each, a GitHub sign-in reads: far beyond any real account. A
	// longer list fails the sign-in rather than being cut: a role mapping
	// must not decide on part of what the person belongs to.
	GitHubMaxPages int `env:"KRITIKA_TUNING_AUTH_GIT_HUB_MAX_PAGES" envDefault:"10" yaml:"gitHubMaxPages,omitempty"`
	// ForgejoPageSize is the page size that a Forgejo sign-in's listing
	// asks for, which is the instance's default cap.
	ForgejoPageSize int `env:"KRITIKA_TUNING_AUTH_FORGEJO_PAGE_SIZE" envDefault:"50" yaml:"forgejoPageSize,omitempty"`
	// ForgejoMaxPages bounds how many pages a Forgejo sign-in reads of one
	// list. A longer list fails the sign-in rather than being cut: a role
	// mapping must not decide on part of what the person belongs to.
	ForgejoMaxPages int `env:"KRITIKA_TUNING_AUTH_FORGEJO_MAX_PAGES" envDefault:"20" yaml:"forgejoMaxPages,omitempty"`
	// FailedBuildTTL is how long a failed build of a sign-in, that is, the
	// discovery of an unreachable OIDC issuer, is remembered before a
	// sign-in retries it. So a dead issuer does not cost a network round
	// trip on every attempt.
	FailedBuildTTL time.Duration `env:"KRITIKA_TUNING_AUTH_FAILED_BUILD_TTL" envDefault:"30s" yaml:"failedBuildTtl,omitempty"`
	// MaxFailedSignIns is how many wrong passwords one client address can
	// give in FailedSignInWindow before its password sign-ins are refused.
	// Behind a proxy every client shares its address, so the limit is then
	// the whole instance's: guessing slows for everyone, which is the side
	// to err on.
	MaxFailedSignIns int `env:"KRITIKA_TUNING_AUTH_MAX_FAILED_SIGN_INS" envDefault:"10" yaml:"maxFailedSignIns,omitempty"`
	// FailedSignInWindow is the window that MaxFailedSignIns counts in,
	// from a client's first wrong password.
	FailedSignInWindow time.Duration `env:"KRITIKA_TUNING_AUTH_FAILED_SIGN_IN_WINDOW" envDefault:"15m" yaml:"failedSignInWindow,omitempty"`
	// MaxTrackedClients bounds the password limiter's memory. At this
	// bound, the limiter drops the expired windows. If that frees no room,
	// it drops the oldest window. So a flood of addresses costs the
	// displaced client its count, not the process its memory.
	MaxTrackedClients int `env:"KRITIKA_TUNING_AUTH_MAX_TRACKED_CLIENTS" envDefault:"10000" yaml:"maxTrackedClients,omitempty"`
	// MaxLocalBody bounds a password sign-in's request body.
	MaxLocalBody ByteSize `env:"KRITIKA_TUNING_AUTH_MAX_LOCAL_BODY" envDefault:"4Ki" yaml:"maxLocalBody,omitempty"`
	// HTTPTimeout bounds each call to a sign-in provider, when the
	// provider's client sets none.
	HTTPTimeout time.Duration `env:"KRITIKA_TUNING_AUTH_HTTP_TIMEOUT" envDefault:"15s" yaml:"httpTimeout,omitempty"`
}

// Linear holds the tunable values of package linear.
type Linear struct {
	// MaxResponseBytes bounds the answer read for one issue. The prompt
	// keeps a few thousand bytes of its description.
	MaxResponseBytes ByteSize `env:"KRITIKA_TUNING_LINEAR_MAX_RESPONSE_BYTES" envDefault:"1Mi" yaml:"maxResponseBytes,omitempty"`
	// HTTPTimeout bounds one request to Linear's API, when the client sets
	// no HTTP client of its own.
	HTTPTimeout time.Duration `env:"KRITIKA_TUNING_LINEAR_HTTP_TIMEOUT" envDefault:"15s" yaml:"httpTimeout,omitempty"`
}

// Metrics holds the tunable values of package metrics.
type Metrics struct {
	// QueueCollectTimeout bounds reading the job queue's statistics for
	// one scrape, so a slow database does not stall the scrape.
	QueueCollectTimeout time.Duration `env:"KRITIKA_TUNING_METRICS_QUEUE_COLLECT_TIMEOUT" envDefault:"2s" yaml:"queueCollectTimeout,omitempty"`
}

// Upstream holds the tunable values of package upstream.
type Upstream struct {
	// NearMax bounds how many of the tree's paths that end in a missing
	// path a fetch names, where a path given by its last segments is.
	NearMax int `env:"KRITIKA_TUNING_UPSTREAM_NEAR_MAX" envDefault:"5" yaml:"nearMax,omitempty"`
	// RenameLimit is the most deleted or added files whose renames a
	// fetch's diff finds by content: go-git scores each deleted file
	// against each added one, reading the added one whole every time. Past
	// it, only exact renames are found.
	RenameLimit int `env:"KRITIKA_TUNING_UPSTREAM_RENAME_LIMIT" envDefault:"50" yaml:"renameLimit,omitempty"`
}

func init() {
	crossField = append(crossField,
		func(v Values) error {
			return notAbove("webApi.defaultLimit", v.WebAPI.DefaultLimit, "webApi.maxLimit", v.WebAPI.MaxLimit)
		},
		func(v Values) error {
			return notAbove("webApi.defaultUsageWindow", v.WebAPI.DefaultUsageWindow, "webApi.maxUsageWindow", v.WebAPI.MaxUsageWindow)
		},
		func(v Values) error {
			for i, d := range v.WebAPI.TokenLifetimes {
				if d%Day != 0 {
					return fmt.Errorf("%s must be whole days, or 0 for never, since a token is asked for in days (entry %d, %s)",
						fieldLabel("webApi.tokenLifetimes"), i+1, d)
				}
			}
			return nil
		},
	)
}

// Day is the unit of a token lifetime that a request asks for.
const Day = 24 * time.Hour
