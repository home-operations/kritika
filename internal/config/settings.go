package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/home-operations/kritika/internal/jobtimeout"
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

	// GatewayCacheDir is where the gateway keeps the downloads of the hosts
	// egress.cache names, to serve each again to every runner that asks for
	// the same URL; empty keeps none. The directory is the cache's alone,
	// an emptyDir in the chart, so it lasts as long as the pod.
	GatewayCacheDir string `env:"KRITIKA_GATEWAY_CACHE_DIR" yaml:"gatewayCacheDir,omitempty"`
	// GatewayCacheMaxSize bounds what filesystem storage keeps, the least recently
	// used downloads going first, and apart from that what its fetches
	// under way may write, so the directory holds at most twice it. 2Gi
	// holds a few dozen release archives.
	GatewayCacheMaxSize ByteSize `env:"KRITIKA_GATEWAY_CACHE_MAX_SIZE" envDefault:"2Gi" yaml:"gatewayCacheMaxSize,omitempty"`
	// GatewayCacheMaxAge is how long a kept download is served before it is
	// fetched again, so an asset replaced upstream is served stale for at
	// most this long; a day, since release assets rarely change.
	GatewayCacheMaxAge time.Duration `env:"KRITIKA_GATEWAY_CACHE_MAX_AGE" envDefault:"24h" yaml:"gatewayCacheMaxAge,omitempty"`
	// GatewayCacheStorage selects filesystem or s3. Filesystem needs no
	// external storage and remains the default.
	GatewayCacheStorage string `env:"KRITIKA_GATEWAY_CACHE_STORAGE" envDefault:"filesystem" yaml:"gatewayCacheStorage,omitempty"`
	// GatewayCacheMaxEntrySize bounds one cached download. 256Mi accommodates
	// typical release archives while bounding each fill's temporary storage.
	GatewayCacheMaxEntrySize ByteSize `env:"KRITIKA_GATEWAY_CACHE_MAX_ENTRY_SIZE" envDefault:"256Mi" yaml:"gatewayCacheMaxEntrySize,omitempty"`
	// GatewayCacheS3Endpoint is the object store's http(s) origin; it has no
	// default because each installation supplies its own storage service.
	GatewayCacheS3Endpoint string `env:"KRITIKA_GATEWAY_CACHE_S3_ENDPOINT" yaml:"gatewayCacheS3Endpoint,omitempty"`
	// GatewayCacheS3Region is the signing region. It is explicit because S3
	// compatible servers can use region names other than AWS's defaults.
	GatewayCacheS3Region string `env:"KRITIKA_GATEWAY_CACHE_S3_REGION" yaml:"gatewayCacheS3Region,omitempty"`
	// GatewayCacheS3Bucket is an existing bucket, never created by serve.
	GatewayCacheS3Bucket string `env:"KRITIKA_GATEWAY_CACHE_S3_BUCKET" yaml:"gatewayCacheS3Bucket,omitempty"`
	// GatewayCacheS3Prefix isolates this instance's entries. The default
	// keeps them under gateway/; the storage format adds its own version.
	GatewayCacheS3Prefix string `env:"KRITIKA_GATEWAY_CACHE_S3_PREFIX" envDefault:"gateway/" yaml:"gatewayCacheS3Prefix,omitempty"`
	// GatewayCacheS3ForcePathStyle avoids requiring wildcard bucket DNS,
	// which is uncommon for storage services inside a cluster.
	GatewayCacheS3ForcePathStyle bool `env:"KRITIKA_GATEWAY_CACHE_S3_FORCE_PATH_STYLE" envDefault:"true" yaml:"gatewayCacheS3ForcePathStyle"`

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

	// ReviewWorkers is the capacity of each of the separate review and
	// follow-up queues on one replica. Each job can hold a runner pod;
	// model concurrency is bounded separately by account limits.
	ReviewWorkers int `env:"KRITIKA_REVIEW_WORKERS" envDefault:"2" yaml:"reviewWorkers,omitempty"`
	// IndexWorkers is how many index jobs one worker replica runs at once;
	// indexing is rate-limited apart from reviews so onboarding a large
	// account cannot starve them. Each holds a runner pod too, so a replica
	// runs at most 2*ReviewWorkers + IndexWorkers concurrent runner pods.
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
	// RunnerServiceAccount is the permissionless service account runner pods
	// run as. RunnerDatabaseSecret and RunnerDatabaseSecretKey locate the
	// runner role's DSN, which the Job injects as KRITIKA_DATABASE_URL.
	RunnerServiceAccount    string `env:"KRITIKA_RUNNER_SERVICE_ACCOUNT" envDefault:"kritika-runner" yaml:"serviceAccount,omitempty"`
	RunnerDatabaseSecret    string `env:"KRITIKA_RUNNER_DATABASE_SECRET" envDefault:"kritika-postgres-runner" yaml:"databaseSecret,omitempty"`
	RunnerDatabaseSecretKey string `env:"KRITIKA_RUNNER_DATABASE_SECRET_KEY" envDefault:"uri" yaml:"databaseSecretKey,omitempty"`

	// RunnerJobTemplate is set on every runner Job as a JobTemplateSpec
	// spells it: its labels and annotations, and its spec's
	// activeDeadlineSeconds and ttlSecondsAfterFinished. RunnerPodTemplate
	// is set on its pod as a PodTemplateSpec spells it: labels,
	// annotations, resources for the whole pod, placement, priority, a
	// runtime class, hostUsers, and the runner container's image and
	// imagePullPolicy. The executor refuses any other field at startup.
	RunnerJobTemplate jsonObject `env:"KRITIKA_RUNNER_JOB_TEMPLATE" yaml:"jobTemplate,omitempty"`
	RunnerPodTemplate jsonObject `env:"KRITIKA_RUNNER_POD_TEMPLATE" yaml:"podTemplate,omitempty"`
}

// DefaultRunnerDeadline bounds a runner Job whose jobTemplate sets no
// activeDeadlineSeconds.
const DefaultRunnerDeadline = 15 * time.Minute

// RunnerDeadline bounds a runner Job, its jobTemplate's
// activeDeadlineSeconds, and so the work around it: the job that runs it
// and the run's gateway token. Validate refuses one that is not a positive
// whole number of seconds within jobtimeout.MaxRunnerDeadline.
func (r Runner) RunnerDeadline() time.Duration {
	d, _ := r.deadline()
	return d
}

// deadlineField names the deadline's setting in a refusal.
const deadlineField = "runner.jobTemplate.spec.activeDeadlineSeconds: KRITIKA_RUNNER_JOB_TEMPLATE"

// deadline is RunnerDeadline, and why the jobTemplate's is refused.
func (r Runner) deadline() (time.Duration, error) {
	spec, _ := r.RunnerJobTemplate["spec"].(map[string]any)
	v, set := spec["activeDeadlineSeconds"]
	if !set || v == nil {
		return DefaultRunnerDeadline, nil
	}
	var seconds float64
	switch n := v.(type) {
	case int:
		seconds = float64(n)
	case int64:
		seconds = float64(n)
	case uint64:
		seconds = float64(n)
	case float64:
		seconds = n
	default:
		return DefaultRunnerDeadline, errors.New(deadlineField + " must be a number of seconds")
	}
	if seconds != math.Trunc(seconds) || seconds < 1 || seconds > jobtimeout.MaxRunnerDeadline.Seconds() {
		return DefaultRunnerDeadline, fmt.Errorf("%s must be a whole number of seconds from 1 to %d, "+
			"or River's %s job timeout cap would cut the runner off early",
			deadlineField, int64(jobtimeout.MaxRunnerDeadline/time.Second), jobtimeout.MaxJobTimeout)
	}
	return time.Duration(seconds) * time.Second, nil
}

// jsonObject is a JSON object from the environment, kept as loose maps
// because the Kubernetes types are not a dependency of this package.
type jsonObject map[string]any

// UnmarshalText decodes the variable's JSON.
func (o *jsonObject) UnmarshalText(b []byte) error { return json.Unmarshal(b, (*map[string]any)(o)) }

// ByteSize is a size in bytes, a whole number of them or one with a
// binary suffix as Kubernetes writes a quantity: Ki, Mi, Gi or Ti.
type ByteSize int64

// UnmarshalText parses the variable's value.
func (b *ByteSize) UnmarshalText(text []byte) error {
	s := string(text)
	num, unit := s, int64(1)
	for i, suffix := range []string{"Ki", "Mi", "Gi", "Ti"} {
		if n, ok := strings.CutSuffix(s, suffix); ok {
			num, unit = n, 1<<(10*(i+1))
			break
		}
	}
	n, err := strconv.ParseInt(num, 10, 64)
	if err != nil || n < 0 || n > math.MaxInt64/unit {
		return fmt.Errorf("config: %q is not a size: a whole number of bytes, or one followed by Ki, Mi, Gi or Ti", s)
	}
	*b = ByteSize(n * unit)
	return nil
}
