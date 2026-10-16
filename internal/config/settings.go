package config

import (
	"encoding/json"
	"time"

	"go.yaml.in/yaml/v4"
)

// Settings holds non-secret application settings from the configuration file.
// Operational settings can also be supplied through environment variables.
type Settings struct {
	// Addr is the listen address of serve's one public listener: the
	// webhooks, /hooks/{connection}, and the dashboard. Port 8080 matches
	// the container image's EXPOSE and the other services in the fleet.
	Addr string `env:"KRITIKA_ADDR" envDefault:":8080" yaml:"addr,omitempty"`

	// MetricsAddr is the listen address for /healthz, /readyz and /metrics.
	// Kept on a separate port from the public listener, as konflate does, so
	// the management endpoints are never reachable through the ingress that
	// fronts it.
	MetricsAddr string `env:"KRITIKA_METRICS_ADDR" envDefault:":8081" yaml:"metricsAddr,omitempty"`

	// GatewayAddr is the listen address of the gateway serve runs: the
	// forward proxy runner pods reach the outside through, and the model
	// and similar-code endpoints a runner calls with its run token. Its own
	// port, so the runner network policy can name it without opening the
	// public or management listeners.
	GatewayAddr string `env:"KRITIKA_GATEWAY_ADDR" envDefault:":8082" yaml:"gatewayAddr,omitempty"`

	// GatewayURL is the gateway's in-cluster address, http://host:port:
	// runner Jobs are handed it as HTTPS_PROXY and HTTP_PROXY, with their
	// run token as its password, and a review's job document names it as
	// its model endpoint. serve needs it, since every review reaches its
	// model through it.
	GatewayURL string `env:"KRITIKA_GATEWAY_URL" yaml:"gatewayUrl,omitempty"`

	// GatewayTokenTTL is how long a run token outlives its runner Job's
	// deadline before it expires on its own, in case the worker that
	// minted it dies before revoking it.
	GatewayTokenTTL time.Duration `env:"KRITIKA_GATEWAY_TOKEN_TTL" envDefault:"1h" yaml:"gatewayTokenTtl,omitempty"`

	// WebURL is the dashboard's externally reachable origin: an absolute
	// http(s) URL with a host and no query or fragment. It is how the
	// dashboard builds absolute links (OIDC redirect URIs, session cookie
	// scope) back to itself, and how a review's summary comment links its
	// re-run badge to the pull request's page, so it must match how the
	// ingress/HTTPRoute actually exposes it. A trailing slash is trimmed. Required to serve.
	// Parsed once into an unexported *url.URL, read back with
	// [Config.WebURLParsed].
	WebURL string `env:"KRITIKA_WEB_URL" yaml:"webUrl,omitempty"`

	// DatabaseAppRole and DatabaseRunnerRole are the Postgres role names the
	// owner grants privileges to during migrations. They default to what
	// deploy/dev and the chart's CloudNativePG example declare.
	DatabaseAppRole    string `env:"KRITIKA_DATABASE_APP_ROLE" envDefault:"kritika_app" yaml:"databaseAppRole,omitempty"`
	DatabaseRunnerRole string `env:"KRITIKA_DATABASE_RUNNER_ROLE" envDefault:"kritika_runner" yaml:"databaseRunnerRole,omitempty"`

	// LeaderRetryInterval is how often a leader-eligible replica retries the
	// leader lock and how often the holder verifies it still has it.
	LeaderRetryInterval time.Duration `env:"KRITIKA_LEADER_RETRY_INTERVAL" envDefault:"15s" yaml:"leaderRetryInterval,omitempty"`

	// ReviewWorkers is how many review jobs one worker replica runs at once.
	// Each one holds a runner pod open for the length of a fetch and diff,
	// so this bounds pods per replica, not model calls.
	ReviewWorkers int `env:"KRITIKA_REVIEW_WORKERS" envDefault:"2" yaml:"reviewWorkers,omitempty"`
	// IndexWorkers is how many index jobs one worker replica runs at once;
	// indexing is rate-limited apart from reviews so onboarding a large
	// account cannot starve them. Each holds a runner pod too, so a replica
	// runs at most ReviewWorkers + IndexWorkers runner pods.
	IndexWorkers int `env:"KRITIKA_INDEX_WORKERS" envDefault:"1" yaml:"indexWorkers,omitempty"`

	// ThreadWorkers reserves capacity for finding resolution/restoration updates.
	// One per replica keeps these short forge/database jobs independent of model
	// follow-ups without adding runner pods.
	ThreadWorkers int `env:"KRITIKA_THREAD_WORKERS" envDefault:"1" yaml:"threadWorkers,omitempty"`

	// Executor selects how runners run; see the Executor type.
	Executor Executor `env:"KRITIKA_EXECUTOR" envDefault:"kubernetes" yaml:"executor,omitempty"`

	// LogLevel is the minimum slog level emitted: debug, info, warn or error.
	LogLevel string `env:"KRITIKA_LOG_LEVEL" envDefault:"info" yaml:"logLevel,omitempty"`

	// LogFormat selects the slog handler: "json" (the default, for containers)
	// or "text" for local runs.
	LogFormat string `env:"KRITIKA_LOG_FORMAT" envDefault:"json" yaml:"logFormat,omitempty"`

	// PollInterval is how often the leader lists each app's open pull
	// requests, its backstop for missed webhooks; 0s turns polling off.
	PollInterval time.Duration `env:"KRITIKA_POLL_INTERVAL" envDefault:"10m" yaml:"pollInterval,omitempty"`
	// PollLookback bounds how far back a first or long-idle poll looks, so a
	// long outage does not list every open pull request's history at once.
	PollLookback time.Duration `env:"KRITIKA_POLL_LOOKBACK" envDefault:"24h" yaml:"pollLookback,omitempty"`
	// OnboardWindow is how many onboarding index jobs the leader keeps
	// queued or running at once; accounts take turns, and the repositories
	// whose pull requests moved last go first.
	OnboardWindow int `env:"KRITIKA_ONBOARD_WINDOW" envDefault:"4" yaml:"onboardWindow,omitempty"`
	// IndexGrace is how long the index of a repository that stopped running
	// is kept, so turning it back on within the window reuses the index.
	IndexGrace time.Duration `env:"KRITIKA_INDEX_GRACE" envDefault:"720h" yaml:"indexGrace,omitempty"`
	// TranscriptRetention is how long a review's transcript is
	// kept; at least a day, since members read it after the review.
	TranscriptRetention time.Duration `env:"KRITIKA_TRANSCRIPT_RETENTION" envDefault:"720h" yaml:"transcriptRetention,omitempty"`
	// DiffRetention is how long a review keeps the diff it was made from,
	// the context it read and the repository files it named; at least a
	// day, since members read them after the review. The review and its
	// findings stay.
	DiffRetention time.Duration `env:"KRITIKA_DIFF_RETENTION" envDefault:"720h" yaml:"diffRetention,omitempty"`

	// Runner groups the runtime settings used to create runner Jobs.
	Runner `yaml:"runner,omitempty"`
	// fileEnv records file-supplied settings by env name for Env's source reporting.
	fileEnv map[string]bool
}

// Runner describes the Jobs created for reviews and indexing.
type Runner struct {
	// RunnerImage is the image runner Jobs use, normally the release's
	// -tools image. Required to serve with the kubernetes executor.
	RunnerImage string `env:"KRITIKA_RUNNER_IMAGE" yaml:"image,omitempty"`

	// RunnerImagePullPolicy is the runner container's imagePullPolicy:
	// Always, IfNotPresent or Never. Empty leaves Kubernetes' default for
	// the image reference.
	RunnerImagePullPolicy string `env:"KRITIKA_RUNNER_IMAGE_PULL_POLICY" yaml:"imagePullPolicy,omitempty"`

	// RunnerServiceAccount is the permissionless service account runner pods
	// run as. RunnerDatabaseSecret and RunnerDatabaseSecretKey locate the
	// runner role's DSN, which the Job injects as KRITIKA_DATABASE_URL.
	RunnerServiceAccount    string `env:"KRITIKA_RUNNER_SERVICE_ACCOUNT" envDefault:"kritika-runner" yaml:"serviceAccount,omitempty"`
	RunnerDatabaseSecret    string `env:"KRITIKA_RUNNER_DATABASE_SECRET" envDefault:"kritika-postgres-runner" yaml:"databaseSecret,omitempty"`
	RunnerDatabaseSecretKey string `env:"KRITIKA_RUNNER_DATABASE_SECRET_KEY" envDefault:"uri" yaml:"databaseSecretKey,omitempty"`

	// RunnerTTL is how long a finished Job stays for kubectl before
	// Kubernetes removes it. The run row keeps everything the Job knew.
	RunnerTTL time.Duration `env:"KRITIKA_RUNNER_TTL" envDefault:"10m" yaml:"ttl,omitempty"`

	// RunnerRuntimeClass is the RuntimeClass runner pods run under, such as
	// a gVisor or Kata class, so a pod that parses untrusted repository
	// content is kept from the node's kernel. Empty uses the cluster's
	// default runtime. Advised, not required.
	RunnerRuntimeClass string `env:"KRITIKA_RUNNER_RUNTIME_CLASS" yaml:"runtimeClass,omitempty"`

	// RunnerDeadline bounds a runner Job, and RunnerResources are copied
	// verbatim into its pod spec, a JSON object of requests and limits.
	RunnerDeadline  time.Duration `env:"KRITIKA_RUNNER_DEADLINE" envDefault:"15m" yaml:"deadline,omitempty"`
	RunnerResources jsonObject    `env:"KRITIKA_RUNNER_RESOURCES" yaml:"resources,omitempty"`
	// RunnerMiseURLReplacements rewrite the URLs runner tools download
	// from, such as to a caching mirror, as mise's url_replacements, the
	// one mise setting an admin sets: mise ignores the [settings] of the
	// image's system config, and a repository's are dropped. mise checks a
	// locked download against the repository's lock, so a mirror cannot
	// change what a tool is. Empty leaves every URL as it is.
	RunnerMiseURLReplacements MiseURLReplacements `env:"KRITIKA_RUNNER_MISE_URL_REPLACEMENTS" yaml:"miseUrlReplacements,omitempty"`
}

// jsonObject is a JSON object from the environment, kept as loose maps
// because the Kubernetes types are not a dependency of this package.
type jsonObject map[string]any

// UnmarshalText decodes the variable's JSON.
func (o *jsonObject) UnmarshalText(b []byte) error { return json.Unmarshal(b, (*map[string]any)(o)) }

// UnmarshalYAML accepts native resource maps alongside the environment's JSON.
func (o *jsonObject) UnmarshalYAML(n *yaml.Node) error { return n.Decode((*map[string]any)(o)) }

// MiseURLReplacement is one of mise's url_replacements rules: a download
// URL From matches, as a prefix or, after "regex:", a pattern, becomes To.
type MiseURLReplacement struct {
	From string `json:"from" yaml:"from"`
	To   string `json:"to" yaml:"to"`
}

// MiseURLReplacements are rules in the order mise tries them, the first
// that matches a URL rewriting it, so they are a list rather than a map:
// a JSON array in the environment, a YAML sequence in the file.
type MiseURLReplacements []MiseURLReplacement

// UnmarshalText decodes the variable's JSON.
func (r *MiseURLReplacements) UnmarshalText(b []byte) error {
	return json.Unmarshal(b, (*[]MiseURLReplacement)(r))
}

// UnmarshalYAML accepts a native sequence alongside the environment's JSON.
func (r *MiseURLReplacements) UnmarshalYAML(n *yaml.Node) error {
	return n.Decode((*[]MiseURLReplacement)(r))
}
