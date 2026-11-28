package tuning

import (
	"errors"
	"fmt"
	"math"
	"time"
)

// RoleMap holds the tunable values of package rolemap.
type RoleMap struct {
	// EvalCostLimit bounds one evaluation of a role mapping, in CEL's cost
	// units. The expression is the admin's and its inputs come from the
	// identity provider. So this is a guard against an accident rather than
	// an attack. No real mapping comes near it.
	EvalCostLimit int64 `env:"KRITIKA_TUNING_ROLE_MAP_EVAL_COST_LIMIT" envDefault:"1000000" yaml:"evalCostLimit,omitempty"`
}

// RepoConfig holds the tunable values of package repoconfig. A repository
// configuration is meant to point at a handful of small instruction and
// template files, not embed arbitrary content. These caps bound how much of
// the merge-base tree ends up in a review prompt.
type RepoConfig struct {
	// MaxFileBytes is the largest repository file read for a review: the
	// configuration, a referenced file, a rule file or a skill.
	MaxFileBytes ByteSize `env:"KRITIKA_TUNING_REPO_CONFIG_MAX_FILE_BYTES" envDefault:"256Ki" yaml:"maxFileBytes,omitempty"`
	// MaxTotalBytes bounds the referenced files read for a review in all.
	MaxTotalBytes ByteSize `env:"KRITIKA_TUNING_REPO_CONFIG_MAX_TOTAL_BYTES" envDefault:"1Mi" yaml:"maxTotalBytes,omitempty"`
	// MaxRulesBytes caps the rules a prompt lists by their ids and text.
	MaxRulesBytes ByteSize `env:"KRITIKA_TUNING_REPO_CONFIG_MAX_RULES_BYTES" envDefault:"16Ki" yaml:"maxRulesBytes,omitempty"`
	// MaxRuleFileBytes caps the content of a prompt's file rules, and of
	// an agent's instructions file.
	MaxRuleFileBytes ByteSize `env:"KRITIKA_TUNING_REPO_CONFIG_MAX_RULE_FILE_BYTES" envDefault:"32Ki" yaml:"maxRuleFileBytes,omitempty"`
	// MaxRuleFiles bounds how many rule files are read for one review.
	MaxRuleFiles int `env:"KRITIKA_TUNING_REPO_CONFIG_MAX_RULE_FILES" envDefault:"100" yaml:"maxRuleFiles,omitempty"`
	// MaxSkills bounds how many of a repository's skills are read.
	MaxSkills int `env:"KRITIKA_TUNING_REPO_CONFIG_MAX_SKILLS" envDefault:"50" yaml:"maxSkills,omitempty"`
	// MaxSkillDescription is how long a skill's description can be, in
	// characters.
	MaxSkillDescription int `env:"KRITIKA_TUNING_REPO_CONFIG_MAX_SKILL_DESCRIPTION" envDefault:"1024" yaml:"maxSkillDescription,omitempty"`
	// MaxSkillListingBytes bounds how much of the system prompt the listed
	// skills' names and descriptions can take together.
	MaxSkillListingBytes ByteSize `env:"KRITIKA_TUNING_REPO_CONFIG_MAX_SKILL_LISTING_BYTES" envDefault:"4Ki" yaml:"maxSkillListingBytes,omitempty"` //nolint:lll // a struct tag does not wrap
	// MaxSkillLoadedBytes bounds how much the instructions of the skills
	// loaded whole can take together.
	MaxSkillLoadedBytes ByteSize `env:"KRITIKA_TUNING_REPO_CONFIG_MAX_SKILL_LOADED_BYTES" envDefault:"32Ki" yaml:"maxSkillLoadedBytes,omitempty"` //nolint:lll // a struct tag does not wrap
}

// ConfigFile holds the tunable values of package configfile: the defaults
// a configuration file's keys fall back to, which a key the file sets
// overrides, and the bounds that the file's values are checked against.
type ConfigFile struct {
	// DefaultConcurrency is how many calls can run at once on each model of
	// a provider of the instance, which every account shares: the default
	// when the configuration file does not set providers.<name>.concurrency.
	DefaultConcurrency int `env:"KRITIKA_TUNING_CONFIG_FILE_DEFAULT_CONCURRENCY" envDefault:"8" yaml:"defaultConcurrency,omitempty"`
	// DefaultAccountConcurrency is how many calls can run at once on each
	// model of an account's own provider: the default when the
	// configuration file does not set that provider's concurrency.
	DefaultAccountConcurrency int `env:"KRITIKA_TUNING_CONFIG_FILE_DEFAULT_ACCOUNT_CONCURRENCY" envDefault:"2" yaml:"defaultAccountConcurrency,omitempty"` //nolint:lll // a struct tag does not wrap
	// MaxProviderRetries bounds a provider's retries: with backoff, five
	// more attempts span about a minute, which a runner's deadline
	// absorbs.
	MaxProviderRetries int `env:"KRITIKA_TUNING_CONFIG_FILE_MAX_PROVIDER_RETRIES" envDefault:"5" yaml:"maxProviderRetries,omitempty"`
	// MaxProviderTimeout bounds a provider's timeout. A step's budget is
	// two of them, which the runner's request to the gateway and a run's
	// deadline must outlast. At least model.defaultStepTimeout.
	MaxProviderTimeout time.Duration `env:"KRITIKA_TUNING_CONFIG_FILE_MAX_PROVIDER_TIMEOUT" envDefault:"30m" yaml:"maxProviderTimeout,omitempty"` //nolint:lll // a struct tag does not wrap
	// MaxPushSettle is the most that a push's review can wait
	// (trigger.settle): a longer wait is a manual review in all but name.
	MaxPushSettle time.Duration `env:"KRITIKA_TUNING_CONFIG_FILE_MAX_PUSH_SETTLE" envDefault:"1h" yaml:"maxPushSettle,omitempty"`
	// DefaultAgentTimeout bounds a review's agent: the default when the
	// configuration file does not set agent.timeout. At most the agent
	// timeout that jobTimeout leaves, and whole seconds, as a run's spec
	// carries it.
	DefaultAgentTimeout time.Duration `env:"KRITIKA_TUNING_CONFIG_FILE_DEFAULT_AGENT_TIMEOUT" envDefault:"20m" yaml:"defaultAgentTimeout,omitempty"` //nolint:lll // a struct tag does not wrap
	// DefaultParts is the most parts a review is split into: the default
	// when the configuration file does not set agent.parts. A 512 KiB diff
	// at review.partBytes a part.
	DefaultParts int `env:"KRITIKA_TUNING_CONFIG_FILE_DEFAULT_PARTS" envDefault:"8" yaml:"defaultParts,omitempty"`
	// DefaultFetches is the most repositories fetch_repo fetches for a
	// review: the default when the configuration file does not set
	// agent.fetches. The upstream of a dependency bump takes one, a review
	// of several a few more.
	DefaultFetches int `env:"KRITIKA_TUNING_CONFIG_FILE_DEFAULT_FETCHES" envDefault:"8" yaml:"defaultFetches,omitempty"`
	// DefaultCommandTimeout bounds one command of the run tool: the default
	// when the configuration file does not set agent.commandTimeout. Whole
	// seconds, as a run's spec carries it.
	DefaultCommandTimeout time.Duration `env:"KRITIKA_TUNING_CONFIG_FILE_DEFAULT_COMMAND_TIMEOUT" envDefault:"30s" yaml:"defaultCommandTimeout,omitempty"` //nolint:lll // a struct tag does not wrap
	// MinPromptTokens is the least agent.prompt takes: the system prompt,
	// with its rules and skills, and the pull request's description are
	// paid for out of the same budget. Below it, little of the diff will
	// fit. At most review.defaultBudgetTokens.
	MinPromptTokens int `env:"KRITIKA_TUNING_CONFIG_FILE_MIN_PROMPT_TOKENS" envDefault:"8000" yaml:"minPromptTokens,omitempty"`
	// DefaultMaxDeltaFiles is the most files an incremental review takes:
	// the default when a repository sets no review.incremental.
	DefaultMaxDeltaFiles int `env:"KRITIKA_TUNING_CONFIG_FILE_DEFAULT_MAX_DELTA_FILES" envDefault:"25" yaml:"defaultMaxDeltaFiles,omitempty"`
	// DefaultSimilarFloor is the similar-code floor: the default when the
	// configuration file does not set embedding.similarFloor. A cosine
	// similarity, from 0 to 1, as that key is.
	DefaultSimilarFloor float64 `env:"KRITIKA_TUNING_CONFIG_FILE_DEFAULT_SIMILAR_FLOOR" envDefault:"0.5" yaml:"defaultSimilarFloor,omitempty" tuning:"zero"` //nolint:lll // a struct tag does not wrap
	// DefaultSimilarMargin is the similar-code margin: the default when the
	// configuration file does not set embedding.similarMargin. Wide enough
	// to keep the matches close to a query's best and drop only those well
	// below it. From 0 to 1, as that key is.
	DefaultSimilarMargin float64 `env:"KRITIKA_TUNING_CONFIG_FILE_DEFAULT_SIMILAR_MARGIN" envDefault:"0.1" yaml:"defaultSimilarMargin,omitempty" tuning:"zero"` //nolint:lll // a struct tag does not wrap
	// DefaultRerankCandidates is how many of each query's candidates are
	// sent to be reranked: the default when the configuration file does not
	// set embedding.rerank.candidates, but for the tei API. At most
	// MaxRerankCandidates.
	DefaultRerankCandidates int `env:"KRITIKA_TUNING_CONFIG_FILE_DEFAULT_RERANK_CANDIDATES" envDefault:"50" yaml:"defaultRerankCandidates,omitempty"` //nolint:lll // a struct tag does not wrap
	// MaxRerankCandidates bounds embedding.rerank.candidates.
	MaxRerankCandidates int `env:"KRITIKA_TUNING_CONFIG_FILE_MAX_RERANK_CANDIDATES" envDefault:"100" yaml:"maxRerankCandidates,omitempty"`
	// DefaultTEIRerankCandidates is the candidates sent to a Text
	// Embeddings Inference reranker when the configuration file does not
	// set embedding.rerank.candidates: the most that its
	// --max-client-batch-size takes by default, which refuses a request of
	// more with a 422. At most MaxRerankCandidates.
	DefaultTEIRerankCandidates int `env:"KRITIKA_TUNING_CONFIG_FILE_DEFAULT_TEI_RERANK_CANDIDATES" envDefault:"32" yaml:"defaultTeiRerankCandidates,omitempty"` //nolint:lll // a struct tag does not wrap
	// DefaultRerankTimeout bounds the reranking of one similar-code
	// request: the default when the configuration file does not set
	// embedding.rerank.timeout. It starts before the wait for a slot, so it
	// covers that wait and the reranking of each of the request's queries
	// in turn. A run waits a minute for its answer, of which the timeout
	// leaves at least half to the embedding, its slot and the searches. At
	// most MaxRerankTimeout.
	DefaultRerankTimeout time.Duration `env:"KRITIKA_TUNING_CONFIG_FILE_DEFAULT_RERANK_TIMEOUT" envDefault:"15s" yaml:"defaultRerankTimeout,omitempty"` //nolint:lll // a struct tag does not wrap
	// MaxRerankTimeout bounds embedding.rerank.timeout.
	MaxRerankTimeout time.Duration `env:"KRITIKA_TUNING_CONFIG_FILE_MAX_RERANK_TIMEOUT" envDefault:"30s" yaml:"maxRerankTimeout,omitempty"`
	// DefaultSessionTTL is how long a dashboard session lasts: the default
	// when the configuration file does not set auth.sessionTTL. Within
	// MinSessionTTL and MaxSessionTTL.
	DefaultSessionTTL time.Duration `env:"KRITIKA_TUNING_CONFIG_FILE_DEFAULT_SESSION_TTL" envDefault:"12h" yaml:"defaultSessionTtl,omitempty"`
	// MinSessionTTL is the shortest auth.sessionTTL the file can set.
	MinSessionTTL time.Duration `env:"KRITIKA_TUNING_CONFIG_FILE_MIN_SESSION_TTL" envDefault:"5m" yaml:"minSessionTtl,omitempty"`
	// MaxSessionTTL is the longest auth.sessionTTL the file can set.
	MaxSessionTTL time.Duration `env:"KRITIKA_TUNING_CONFIG_FILE_MAX_SESSION_TTL" envDefault:"720h" yaml:"maxSessionTtl,omitempty"`
	// MaxReviewerChars bounds an agent's instructions written as text, in
	// characters. The public schema, docs/public/kritika.schema.json,
	// states the default.
	MaxReviewerChars int `env:"KRITIKA_TUNING_CONFIG_FILE_MAX_REVIEWER_CHARS" envDefault:"8000" yaml:"maxReviewerChars,omitempty"`
	// MaxReviewers bounds how many agents a configuration, or a
	// repository's file, writes: each runs an agent of its own over the
	// change, or picks the one the default pass runs. It also sizes the
	// largest review's job timeout. The public schema states the default.
	MaxReviewers int `env:"KRITIKA_TUNING_CONFIG_FILE_MAX_REVIEWERS" envDefault:"8" yaml:"maxReviewers,omitempty"`
	// MaxRuleChars bounds one rule's text, in characters. The public
	// schema states the default.
	MaxRuleChars int `env:"KRITIKA_TUNING_CONFIG_FILE_MAX_RULE_CHARS" envDefault:"2000" yaml:"maxRuleChars,omitempty"`
}

// Process holds the tunable values of the two mains, serve's and the
// runner's. On stopping, serve runs three paths side by side: queueDrain,
// queueStopHeadroom and eventsDrain one after another; linger, then
// gateway.drain; and linger, then publicDrain. The longest of them, with
// room for the process to exit, must fit within the chart's
// terminationGracePeriodSeconds, 200s by default: raising one means
// raising that too.
type Process struct {
	// MemLimitRefresh is how often GOMEMLIMIT follows the cgroup memory
	// limit, for a pod resized in place.
	MemLimitRefresh time.Duration `env:"KRITIKA_TUNING_PROCESS_MEM_LIMIT_REFRESH" envDefault:"1m" yaml:"memLimitRefresh,omitempty"`
	// QueueDrain is how long a stopping serve lets running jobs finish. A
	// review still running then is cut and retried.
	QueueDrain time.Duration `env:"KRITIKA_TUNING_PROCESS_QUEUE_DRAIN" envDefault:"100s" yaml:"queueDrain,omitempty"`
	// QueueStopHeadroom covers what a cut review does before it hands its
	// job back, each step at its own bound: deleting the runner Job and
	// reading its pod (20s each), revoking its token (10s) and waiting for
	// its agent row (40s). It is also River's stuck-job threshold for a job
	// that its timeout cut. A job still at it when the process is killed
	// is not recorded by River and waits on the rescuer instead. Completed
	// runner answers are checkpointed before merge and confidence calls,
	// so rescue can resume those answers.
	QueueStopHeadroom time.Duration `env:"KRITIKA_TUNING_PROCESS_QUEUE_STOP_HEADROOM" envDefault:"90s" yaml:"queueStopHeadroom,omitempty"`
	// EventsDrain then lets the Kubernetes Events of the runs the stop cut
	// be written.
	EventsDrain time.Duration `env:"KRITIKA_TUNING_PROCESS_EVENTS_DRAIN" envDefault:"5s" yaml:"eventsDrain,omitempty"`
	// Linger is how long the public listener and the gateway keep
	// accepting connections once serve is told to stop. Kubernetes and the
	// proxies in front of it take a moment to stop routing to a
	// terminating pod. A connection refused meanwhile is a webhook lost,
	// because GitHub does not redeliver on its own, or a runner's model
	// step retried.
	Linger time.Duration `env:"KRITIKA_TUNING_PROCESS_LINGER" envDefault:"5s" yaml:"linger,omitempty"`
	// PublicDrain is how long a stopping serve lets webhook and dashboard
	// requests finish. Event streams end at once, when the API's Run
	// returns.
	PublicDrain time.Duration `env:"KRITIKA_TUNING_PROCESS_PUBLIC_DRAIN" envDefault:"10s" yaml:"publicDrain,omitempty"`
	// SchemaPoll is how often serve looks for the database schema, which
	// the leader migrates, before it works the queues.
	SchemaPoll time.Duration `env:"KRITIKA_TUNING_PROCESS_SCHEMA_POLL" envDefault:"2s" yaml:"schemaPoll,omitempty"`
	// QueueStartRetry is the wait between attempts to start the job queue.
	QueueStartRetry time.Duration `env:"KRITIKA_TUNING_PROCESS_QUEUE_START_RETRY" envDefault:"5s" yaml:"queueStartRetry,omitempty"`
	// QueueStartAttempts is how many times serve tries to start the job
	// queue before it fails: at the defaults, with QueueStartRetry, two
	// minutes.
	QueueStartAttempts int `env:"KRITIKA_TUNING_PROCESS_QUEUE_START_ATTEMPTS" envDefault:"24" yaml:"queueStartAttempts,omitempty"`
	// DriftInterval is how often a replica compares its configuration with
	// the one the leader applied, which differ while a rollout is part
	// done.
	DriftInterval time.Duration `env:"KRITIKA_TUNING_PROCESS_DRIFT_INTERVAL" envDefault:"10s" yaml:"driftInterval,omitempty"`
}

func init() {
	crossField = append(crossField,
		func(v Values) error {
			return notAbove("configFile.defaultRerankCandidates", v.ConfigFile.DefaultRerankCandidates,
				"configFile.maxRerankCandidates", v.ConfigFile.MaxRerankCandidates)
		},
		func(v Values) error {
			return notAbove("configFile.defaultTeiRerankCandidates", v.ConfigFile.DefaultTEIRerankCandidates,
				"configFile.maxRerankCandidates", v.ConfigFile.MaxRerankCandidates)
		},
		func(v Values) error {
			return notAbove("configFile.defaultRerankTimeout", v.ConfigFile.DefaultRerankTimeout,
				"configFile.maxRerankTimeout", v.ConfigFile.MaxRerankTimeout)
		},
		func(v Values) error {
			return notAbove("configFile.minSessionTtl", v.ConfigFile.MinSessionTTL, "configFile.defaultSessionTtl", v.ConfigFile.DefaultSessionTTL)
		},
		func(v Values) error {
			return notAbove("configFile.defaultSessionTtl", v.ConfigFile.DefaultSessionTTL, "configFile.maxSessionTtl", v.ConfigFile.MaxSessionTTL)
		},
		func(v Values) error {
			return notAbove("configFile.minPromptTokens", v.ConfigFile.MinPromptTokens, "review.defaultBudgetTokens", v.Review.DefaultBudgetTokens)
		},
		func(v Values) error {
			// A jobTimeout that leaves no agent timeout has a rule of its own.
			if most := v.JobTimeout.MaxAgentTimeout(); most > 0 && v.ConfigFile.DefaultAgentTimeout > most {
				return fmt.Errorf("%s %w the agent timeout that jobTimeout leaves, %s",
					fieldLabel("configFile.defaultAgentTimeout"), errMustNotExceed, most)
			}
			return nil
		},
		func(v Values) error {
			// A run's spec carries both in whole seconds.
			return errors.Join(
				wholeSeconds("configFile.defaultAgentTimeout", v.ConfigFile.DefaultAgentTimeout),
				wholeSeconds("configFile.defaultCommandTimeout", v.ConfigFile.DefaultCommandTimeout),
			)
		},
		func(v Values) error {
			return similarity(v.ConfigFile.DefaultSimilarFloor, "configFile.defaultSimilarFloor")
		},
		func(v Values) error {
			return similarity(v.ConfigFile.DefaultSimilarMargin, "configFile.defaultSimilarMargin")
		},
		func(v Values) error {
			// serve waits out their sum for the queues to stop.
			if p := v.Process; p.QueueDrain > 0 && p.QueueStopHeadroom > time.Duration(math.MaxInt64)-p.QueueDrain {
				return fmt.Errorf("%s and %s together must not exceed %s", fieldLabel("process.queueDrain"),
					fieldLabel("process.queueStopHeadroom"), time.Duration(math.MaxInt64))
			}
			return nil
		},
	)
}

// similarity is the error of a field, a cosine similarity or a difference
// of them, outside 0 to 1, or nil. NaN is refused too.
func similarity(x float64, path string) error {
	if !(x >= 0 && x <= 1) {
		return fmt.Errorf("%s must be from 0 to 1, as a cosine similarity is", fieldLabel(path))
	}
	return nil
}
