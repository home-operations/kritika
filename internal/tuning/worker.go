package tuning

import (
	"fmt"
	"time"
)

// Worker holds the tunable values of package worker.
type Worker struct {
	// MinAgentTokens is the least monthly headroom a review starts with:
	// below it the agent cannot read the diff before running out, so the
	// review is capped instead.
	MinAgentTokens int64 `env:"KRITIKA_TUNING_WORKER_MIN_AGENT_TOKENS" envDefault:"50000" yaml:"minAgentTokens,omitempty"`
	// DiffBytesPerLine estimates a unified diff's size from the forge's
	// counts: a changed line with its share of the context around it.
	DiffBytesPerLine int `env:"KRITIKA_TUNING_WORKER_DIFF_BYTES_PER_LINE" envDefault:"64" yaml:"diffBytesPerLine,omitempty"`
	// DiffBytesPerFile is the estimate's share of a file's header.
	DiffBytesPerFile int `env:"KRITIKA_TUNING_WORKER_DIFF_BYTES_PER_FILE" envDefault:"256" yaml:"diffBytesPerFile,omitempty"`
	// AgentRowWait is how long a failed run's agent row is waited for: a
	// runner pod's termination grace period.
	AgentRowWait time.Duration `env:"KRITIKA_TUNING_WORKER_AGENT_ROW_WAIT" envDefault:"30s" yaml:"agentRowWait,omitempty"`
	// AgentRowPoll is how often the wait for a failed run's agent row looks
	// again.
	AgentRowPoll time.Duration `env:"KRITIKA_TUNING_WORKER_AGENT_ROW_POLL" envDefault:"1s" yaml:"agentRowPoll,omitempty"`
	// AgentRowReadSlack is what the read of a run's agent row gets on top
	// of AgentRowWait, for the reads around the wait.
	AgentRowReadSlack time.Duration `env:"KRITIKA_TUNING_WORKER_AGENT_ROW_READ_SLACK" envDefault:"10s" yaml:"agentRowReadSlack,omitempty"`
	// ModelStepsReadTimeout bounds the read of whether a failed run began
	// any model step, which runs on past the job's cancellation.
	ModelStepsReadTimeout time.Duration `env:"KRITIKA_TUNING_WORKER_MODEL_STEPS_READ_TIMEOUT" envDefault:"10s" yaml:"modelStepsReadTimeout,omitempty"` //nolint:lll // a struct tag does not wrap
	// MaxAnswers is how many of the latest replies in a finding's thread a
	// re-review is shown.
	MaxAnswers int `env:"KRITIKA_TUNING_WORKER_MAX_ANSWERS" envDefault:"4" yaml:"maxAnswers,omitempty"`
	// AnswerBytes is how much of each of those replies it is shown.
	AnswerBytes int `env:"KRITIKA_TUNING_WORKER_ANSWER_BYTES" envDefault:"600" yaml:"answerBytes,omitempty"`
	// ConcessionReasonBytes is how much of an answer, or of kritika's own
	// reply, the reason of a dismissal that kritika concedes quotes. The
	// store cuts the whole reason again, to Store.DismissalReasonChars.
	ConcessionReasonBytes int `env:"KRITIKA_TUNING_WORKER_CONCESSION_REASON_BYTES" envDefault:"200" yaml:"concessionReasonBytes,omitempty"`
	// OwnPathsNamed is how many of the changed files that configure kritika
	// the reason it does not approve a change names before it counts the
	// rest.
	OwnPathsNamed int `env:"KRITIKA_TUNING_WORKER_OWN_PATHS_NAMED" envDefault:"3" yaml:"ownPathsNamed,omitempty"`
	// ReleaseTimeout bounds the lease release after the job's context is
	// gone.
	ReleaseTimeout time.Duration `env:"KRITIKA_TUNING_WORKER_RELEASE_TIMEOUT" envDefault:"10s" yaml:"releaseTimeout,omitempty"`
	// DetachTimeout bounds work that runs on past the job's context once
	// the model answers: publishing, and ending a review whose job ended.
	// It must still finish then, but a hung forge or database call must not
	// hold the job forever.
	DetachTimeout time.Duration `env:"KRITIKA_TUNING_WORKER_DETACH_TIMEOUT" envDefault:"2m" yaml:"detachTimeout,omitempty"`
	// ChatGPTRefreshInterval is how often the leader looks for ChatGPT
	// sessions whose tokens are due for renewal.
	ChatGPTRefreshInterval time.Duration `env:"KRITIKA_TUNING_WORKER_CHAT_GPT_REFRESH_INTERVAL" envDefault:"1m" yaml:"chatGptRefreshInterval,omitempty"` //nolint:lll // a struct tag does not wrap
	// ChatGPTRefreshTimeout bounds one session's refresh and the write that
	// saves it, which run on past the leader's cancellation: once OpenAI
	// answers, the refresh token it rotated is spent, and only the saved
	// replacement can renew the session again.
	ChatGPTRefreshTimeout time.Duration `env:"KRITIKA_TUNING_WORKER_CHAT_GPT_REFRESH_TIMEOUT" envDefault:"1m" yaml:"chatGptRefreshTimeout,omitempty"` //nolint:lll // a struct tag does not wrap
	// ChatGPTSaveAttempts is how many times a refreshed token set is
	// written before it is given up.
	ChatGPTSaveAttempts int `env:"KRITIKA_TUNING_WORKER_CHAT_GPT_SAVE_ATTEMPTS" envDefault:"4" yaml:"chatGptSaveAttempts,omitempty"`
	// ChatGPTSaveBackoff is the pause after the first failed write of a
	// refreshed token set, and what each later pause adds to it.
	ChatGPTSaveBackoff time.Duration `env:"KRITIKA_TUNING_WORKER_CHAT_GPT_SAVE_BACKOFF" envDefault:"1s" yaml:"chatGptSaveBackoff,omitempty"`
	// ChatGPTHTTPTimeout bounds one request to OpenAI's token endpoint.
	ChatGPTHTTPTimeout time.Duration `env:"KRITIKA_TUNING_WORKER_CHAT_GPT_HTTP_TIMEOUT" envDefault:"30s" yaml:"chatGptHttpTimeout,omitempty"`
	// AwaitInterval is how long the job of an answered review request
	// sleeps between looks at whether the review ended.
	AwaitInterval time.Duration `env:"KRITIKA_TUNING_WORKER_AWAIT_INTERVAL" envDefault:"15s" yaml:"awaitInterval,omitempty"`
	// ConfidenceTimeout bounds scoring a review, the wait for a slot on the
	// confidence model included. With the write-back's own bound it stays
	// inside JobTimeout.PublishHeadroom, which Validate holds it to.
	ConfidenceTimeout time.Duration `env:"KRITIKA_TUNING_WORKER_CONFIDENCE_TIMEOUT" envDefault:"2m" yaml:"confidenceTimeout,omitempty"`
	// ConfidenceMaxOutputTokens bounds the scorer's answer: a score and a
	// sentence or two, with room for a reasoning model's thinking.
	ConfidenceMaxOutputTokens int64 `env:"KRITIKA_TUNING_WORKER_CONFIDENCE_MAX_OUTPUT_TOKENS" envDefault:"4096" yaml:"confidenceMaxOutputTokens,omitempty"` //nolint:lll // a struct tag does not wrap
	// ConfidenceBudgetShare is how many times the agent's prompt budget the
	// scorer's prompt can take, up to ConfidenceBudgetCap tokens and never
	// less than the agent's. The scorer makes one call with no tool outputs
	// to leave room for, and the diff is what its risk rating rests on.
	ConfidenceBudgetShare int `env:"KRITIKA_TUNING_WORKER_CONFIDENCE_BUDGET_SHARE" envDefault:"2" yaml:"confidenceBudgetShare,omitempty"`
	// ConfidenceBudgetCap bounds the scorer's prompt budget, so that an
	// agent budget set for a large context does not double past what the
	// scorer's model holds.
	ConfidenceBudgetCap int `env:"KRITIKA_TUNING_WORKER_CONFIDENCE_BUDGET_CAP" envDefault:"96000" yaml:"confidenceBudgetCap,omitempty"`
	// ContinueWindow is how recently a run must have kept a conversation for
	// a review to carry it on. OpenAI keeps a prompt's prefix cached for
	// about 30 minutes after its last use; the window leaves a runner the
	// minutes it takes from the decision to its first step.
	ContinueWindow time.Duration `env:"KRITIKA_TUNING_WORKER_CONTINUE_WINDOW" envDefault:"25m" yaml:"continueWindow,omitempty"`
	// AnthropicContinueWindow is ContinueWindow for an Anthropic model,
	// whose prompt cache lasts about 5 minutes.
	AnthropicContinueWindow time.Duration `env:"KRITIKA_TUNING_WORKER_ANTHROPIC_CONTINUE_WINDOW" envDefault:"4m" yaml:"anthropicContinueWindow,omitempty"` //nolint:lll // a struct tag does not wrap
	// ThreadMessages is how many of a thread's messages a follow-up's
	// prompt keeps.
	ThreadMessages int `env:"KRITIKA_TUNING_WORKER_THREAD_MESSAGES" envDefault:"20" yaml:"threadMessages,omitempty"`
	// RepliesPerThread is how many replies the bot makes, in one of its
	// finding threads, to replies that did not address it. Past it, it
	// leaves the thread to a mention.
	RepliesPerThread int `env:"KRITIKA_TUNING_WORKER_REPLIES_PER_THREAD" envDefault:"3" yaml:"repliesPerThread,omitempty"`
	// ForgeBuildTimeout bounds a forge client's build, which runs on no
	// caller's context.
	ForgeBuildTimeout time.Duration `env:"KRITIKA_TUNING_WORKER_FORGE_BUILD_TIMEOUT" envDefault:"1m" yaml:"forgeBuildTimeout,omitempty"`
	// EmbedBatch is how many staged chunks are embedded and inserted at
	// once.
	EmbedBatch int `env:"KRITIKA_TUNING_WORKER_EMBED_BATCH" envDefault:"128" yaml:"embedBatch,omitempty"`
	// AppliedBuildWait is how long an index job waits on a pod whose
	// embedder or index format the leader did not apply, before it is tried
	// again.
	AppliedBuildWait time.Duration `env:"KRITIKA_TUNING_WORKER_APPLIED_BUILD_WAIT" envDefault:"1m" yaml:"appliedBuildWait,omitempty"`
	// BuildWaitWarn is the wait for a pod on the applied build from which an
	// index job logs each wait as a warning. A rollout's waits end within
	// minutes; a leader that never applies the pods' build keeps every index
	// job waiting until a restart.
	BuildWaitWarn int `env:"KRITIKA_TUNING_WORKER_BUILD_WAIT_WARN" envDefault:"10" yaml:"buildWaitWarn,omitempty"`
	// TipCheckWait is how long an index job waits to ask the forge for the
	// branch tip again after the forge did not give it, unless the forge
	// names a later time.
	TipCheckWait time.Duration `env:"KRITIKA_TUNING_WORKER_TIP_CHECK_WAIT" envDefault:"1m" yaml:"tipCheckWait,omitempty"`
	// ForgeWaitMin is an index job's first wait for a forge that failed
	// transiently before its run starts. Each wait doubles it, up to
	// ForgeWaitMax, ForgeWaits times: about an hour in all at the defaults.
	// River's backoff would spend the job's few attempts within seconds and
	// drop the push that queued it; a snooze spends none.
	ForgeWaitMin time.Duration `env:"KRITIKA_TUNING_WORKER_FORGE_WAIT_MIN" envDefault:"30s" yaml:"forgeWaitMin,omitempty"`
	// ForgeWaitMax caps one of those waits.
	ForgeWaitMax time.Duration `env:"KRITIKA_TUNING_WORKER_FORGE_WAIT_MAX" envDefault:"15m" yaml:"forgeWaitMax,omitempty"`
	// ForgeWaits is how many of those waits a job has before the failure
	// spends an attempt.
	ForgeWaits int `env:"KRITIKA_TUNING_WORKER_FORGE_WAITS" envDefault:"8" yaml:"forgeWaits,omitempty"`
	// MergeTimeout bounds writing a split review's summary, inside the time
	// publishing reads the agent's run in: Validate holds it within
	// JobTimeout.PublishHeadroom.
	MergeTimeout time.Duration `env:"KRITIKA_TUNING_WORKER_MERGE_TIMEOUT" envDefault:"100s" yaml:"mergeTimeout,omitempty"`
	// MergeMaxOutputTokens bounds the merge call's answer: a summary, with
	// room for the review model's reasoning at its effort.
	MergeMaxOutputTokens int64 `env:"KRITIKA_TUNING_WORKER_MERGE_MAX_OUTPUT_TOKENS" envDefault:"16384" yaml:"mergeMaxOutputTokens,omitempty"`
	// OnboardInterval is how often the onboarding feeder tops its window up.
	OnboardInterval time.Duration `env:"KRITIKA_TUNING_WORKER_ONBOARD_INTERVAL" envDefault:"30s" yaml:"onboardInterval,omitempty"`
	// PartSlotInterval is how often a split review looks for model slots
	// for the parts still waiting for one.
	PartSlotInterval time.Duration `env:"KRITIKA_TUNING_WORKER_PART_SLOT_INTERVAL" envDefault:"2s" yaml:"partSlotInterval,omitempty"`
	// MaxNamedFiles is how many files a split review's note names before it
	// counts the rest.
	MaxNamedFiles int `env:"KRITIKA_TUNING_WORKER_MAX_NAMED_FILES" envDefault:"5" yaml:"maxNamedFiles,omitempty"`
	// RescueInterval is how often the leader looks for jobs and runs that a
	// dead replica left.
	RescueInterval time.Duration `env:"KRITIKA_TUNING_WORKER_RESCUE_INTERVAL" envDefault:"30s" yaml:"rescueInterval,omitempty"`
	// RescueBatch is how many jobs and runs one rescue pass takes on.
	RescueBatch int `env:"KRITIKA_TUNING_WORKER_RESCUE_BATCH" envDefault:"100" yaml:"rescueBatch,omitempty"`
	// RetentionSweepInterval is how often the leader deletes what is past
	// its retention window: model-call transcripts, egress refusals,
	// settled model invocations, review diffs, agent conversations, stopped
	// repositories' indexes, finished runs' similar-code exclusions and
	// dashboard sessions.
	RetentionSweepInterval time.Duration `env:"KRITIKA_TUNING_WORKER_RETENTION_SWEEP_INTERVAL" envDefault:"1h" yaml:"retentionSweepInterval,omitempty"` //nolint:lll // a struct tag does not wrap
	// ConversationRetention is how long an agent's conversation is kept for
	// a re-review to carry on: well past any provider's cache window, after
	// which carrying it on costs more than starting afresh.
	ConversationRetention time.Duration `env:"KRITIKA_TUNING_WORKER_CONVERSATION_RETENTION" envDefault:"2h" yaml:"conversationRetention,omitempty"` //nolint:lll // a struct tag does not wrap
	// LockRetry is how long a write under a pull request's publication lock
	// waits before it tries the held lock again.
	LockRetry time.Duration `env:"KRITIKA_TUNING_WORKER_LOCK_RETRY" envDefault:"500ms" yaml:"lockRetry,omitempty"`
	// ShutdownSaveGrace is how long a completed runner's answer is still
	// saved for once the replica shuts down.
	ShutdownSaveGrace time.Duration `env:"KRITIKA_TUNING_WORKER_SHUTDOWN_SAVE_GRACE" envDefault:"10s" yaml:"shutdownSaveGrace,omitempty"`
	// SnoozeMin is the first snooze of a review that found every model slot
	// held. Each further snooze doubles it, up to SnoozeMax. A slot let go
	// sooner wakes it; the snooze bounds the wait when none is, as when a
	// holder died and its lease expired.
	SnoozeMin time.Duration `env:"KRITIKA_TUNING_WORKER_SNOOZE_MIN" envDefault:"5s" yaml:"snoozeMin,omitempty"`
	// SnoozeMax caps that snooze.
	SnoozeMax time.Duration `env:"KRITIKA_TUNING_WORKER_SNOOZE_MAX" envDefault:"1m" yaml:"snoozeMax,omitempty"`
	// PublishWait is how long a review or thread change whose start found
	// the pull request's publication lock held waits before it tries again:
	// the older review is posting, which is brief.
	PublishWait time.Duration `env:"KRITIKA_TUNING_WORKER_PUBLISH_WAIT" envDefault:"5s" yaml:"publishWait,omitempty"`
	// LabelsMovedSnooze is how long a review skipped by its filter while the
	// pull request's labels moved waits to look again, without spending an
	// attempt.
	LabelsMovedSnooze time.Duration `env:"KRITIKA_TUNING_WORKER_LABELS_MOVED_SNOOZE" envDefault:"1s" yaml:"labelsMovedSnooze,omitempty"`
	// StackTimeout bounds the read of a pull request's stack. The prompt
	// does without it, and the score's read spends the time the confidence
	// model has to answer in.
	StackTimeout time.Duration `env:"KRITIKA_TUNING_WORKER_STACK_TIMEOUT" envDefault:"10s" yaml:"stackTimeout,omitempty"`
	// SuperviseInterval is how often supervision looks at a running run.
	SuperviseInterval time.Duration `env:"KRITIKA_TUNING_WORKER_SUPERVISE_INTERVAL" envDefault:"10s" yaml:"superviseInterval,omitempty"`
	// ThreadTimeout bounds a thread job: a few forge requests, each of which
	// can wait out a rate limit for up to River's default job timeout.
	ThreadTimeout time.Duration `env:"KRITIKA_TUNING_WORKER_THREAD_TIMEOUT" envDefault:"5m" yaml:"threadTimeout,omitempty"`
	// MinRetention is the shortest transcriptRetention and diffRetention
	// that the configuration takes, so that a review's transcript and diff
	// stay for at least a day to look into.
	MinRetention time.Duration `env:"KRITIKA_TUNING_WORKER_MIN_RETENTION" envDefault:"24h" yaml:"minRetention,omitempty"`
}

// Jobs holds the tunable values of package jobs.
type Jobs struct {
	// OnboardRetryAfter is how long a repository whose onboarding job ended
	// without building an index waits to be offered again: the feeder's
	// pace, and the dashboard's, which shows where a repository waits for
	// it. The poller waits as long to look again at the default branch of a
	// repository whose index job ended without moving its index. River
	// keeps finished jobs longer than that.
	OnboardRetryAfter time.Duration `env:"KRITIKA_TUNING_JOBS_ONBOARD_RETRY_AFTER" envDefault:"1h" yaml:"onboardRetryAfter,omitempty"`
	// IndexAttempts bounds an index job's tries: transient failures (a
	// fetch timeout, a runner killed at its deadline) get River's backoff,
	// and an onboarding that fails every try is offered again later.
	IndexAttempts int `env:"KRITIKA_TUNING_JOBS_INDEX_ATTEMPTS" envDefault:"3" yaml:"indexAttempts,omitempty"`
	// ReviewAttempts bounds a review, follow-up or thread job's tries. With
	// River's backoff, the last of the default tries comes over an hour
	// after the first, which outlasts a restart or a database failover. A
	// job that fails every try fails on its input.
	ReviewAttempts int `env:"KRITIKA_TUNING_JOBS_REVIEW_ATTEMPTS" envDefault:"8" yaml:"reviewAttempts,omitempty"`
}

// JobTimeout holds the tunable values of package jobtimeout: the bounds of
// a River job's timeout. River cancels a job's context once its timeout
// passes, and supervision deletes the runner Job when that happens, so a
// job's timeout must cover everything that the worker does around the
// runner as well as the runner.
type JobTimeout struct {
	// HeartbeatInterval is the cadence of a replica's liveness beats on the
	// River jobs it works.
	HeartbeatInterval time.Duration `env:"KRITIKA_TUNING_JOB_TIMEOUT_HEARTBEAT_INTERVAL" envDefault:"30s" yaml:"heartbeatInterval,omitempty"`
	// AbandonedBeats is how many beats a job can miss before it counts as
	// abandoned by a dead replica (HeartbeatAbandonedAfter), enough to allow
	// for a slow database. A replica fences its job one beat before that.
	AbandonedBeats int `env:"KRITIKA_TUNING_JOB_TIMEOUT_ABANDONED_BEATS" envDefault:"6" yaml:"abandonedBeats,omitempty"`
	// LeaseWaitHeadroom is the time a review or a follow-up can spend
	// taking its model lease before its runner starts.
	LeaseWaitHeadroom time.Duration `env:"KRITIKA_TUNING_JOB_TIMEOUT_LEASE_WAIT_HEADROOM" envDefault:"15m" yaml:"leaseWaitHeadroom,omitempty"`
	// PublishHeadroom covers the worker's side of a review after the
	// runner: reading the agent's run and the pack, scoring, merging, and
	// the forge write-back.
	PublishHeadroom time.Duration `env:"KRITIKA_TUNING_JOB_TIMEOUT_PUBLISH_HEADROOM" envDefault:"5m" yaml:"publishHeadroom,omitempty"`
	// IndexWriteHeadroom covers embedding a repository's staged chunks and
	// swapping the generation after the index runner ends. The embedding
	// pass of a large repository is many model calls.
	IndexWriteHeadroom time.Duration `env:"KRITIKA_TUNING_JOB_TIMEOUT_INDEX_WRITE_HEADROOM" envDefault:"60m" yaml:"indexWriteHeadroom,omitempty"` //nolint:lll // a struct tag does not wrap
	// AgentFetchHeadroom is the job time a review run keeps for fetching
	// and the context pack on top of the agent's own timeout.
	AgentFetchHeadroom time.Duration `env:"KRITIKA_TUNING_JOB_TIMEOUT_AGENT_FETCH_HEADROOM" envDefault:"5m" yaml:"agentFetchHeadroom,omitempty"` //nolint:lll // a struct tag does not wrap
	// MaxJobTimeout bounds any review or index job, so that a runner
	// deadline set absurdly high cannot hold a worker slot for longer than
	// the stuck-job rescuer waits.
	MaxJobTimeout time.Duration `env:"KRITIKA_TUNING_JOB_TIMEOUT_MAX_JOB_TIMEOUT" envDefault:"3h" yaml:"maxJobTimeout,omitempty"`
	// RescueGrace is how much longer than MaxJobTimeout a running job is
	// left alone before River treats it as abandoned by a dead worker
	// (RescueStuckJobsAfter), so that a job still working never runs a
	// second time.
	RescueGrace time.Duration `env:"KRITIKA_TUNING_JOB_TIMEOUT_RESCUE_GRACE" envDefault:"1h" yaml:"rescueGrace,omitempty"`
	// RunnerDeadline bounds a runner Job whose jobTemplate sets no
	// activeDeadlineSeconds. Validate holds it within MaxRunnerDeadline.
	RunnerDeadline time.Duration `env:"KRITIKA_TUNING_JOB_TIMEOUT_RUNNER_DEADLINE" envDefault:"15m" yaml:"runnerDeadline,omitempty"`
}

// HeartbeatAbandonedAfter is the age of a job's last beat past which it
// belongs to a replica that died.
func (j JobTimeout) HeartbeatAbandonedAfter() time.Duration {
	return time.Duration(j.AbandonedBeats) * j.HeartbeatInterval
}

// JobFenceAfter is how long a replica that cannot write a job's beat goes
// on before it ends the job: a beat before the leader can hand the job to
// another replica, so that a replica cut off from the database, its runner
// still working, never has its job worked twice.
func (j JobTimeout) JobFenceAfter() time.Duration {
	return j.HeartbeatAbandonedAfter() - j.HeartbeatInterval
}

// RescueStuckJobsAfter is how long a running job is left alone before
// River treats it as abandoned by a dead worker.
func (j JobTimeout) RescueStuckJobsAfter() time.Duration { return j.MaxJobTimeout + j.RescueGrace }

// MaxRunnerDeadline is the largest runner.activeDeadlineSeconds that an
// account can set. Past it, one of two timeouts exceeds MaxJobTimeout: a
// review's or a follow-up's (the deadline plus LeaseWaitHeadroom and
// PublishHeadroom), or an index's (the deadline plus IndexWriteHeadroom).
// River would then cut the runner off before it finishes, rather than the
// deadline doing so.
func (j JobTimeout) MaxRunnerDeadline() time.Duration {
	return min(j.MaxJobTimeout-j.LeaseWaitHeadroom-j.PublishHeadroom, j.MaxJobTimeout-j.IndexWriteHeadroom)
}

// MaxAgentTimeout is the largest agent.timeout that a repository can set.
// Past it, the agent's own contribution to a job's timeout (agent.timeout
// plus AgentFetchHeadroom, LeaseWaitHeadroom and PublishHeadroom) alone
// exceeds MaxJobTimeout, whatever the account's runner deadline.
func (j JobTimeout) MaxAgentTimeout() time.Duration {
	return j.MaxJobTimeout - j.AgentFetchHeadroom - j.LeaseWaitHeadroom - j.PublishHeadroom
}

// Poller holds the tunable values of package poller.
type Poller struct {
	// TipCheckInterval is how often the poller looks at the default branches
	// of an account whose connection's webhooks arrive. A look costs one
	// request per indexed repository: at every poll it would double what
	// polling costs a GitHub App installation, and a missed push webhook is
	// rare while the rest arrive.
	TipCheckInterval time.Duration `env:"KRITIKA_TUNING_POLLER_TIP_CHECK_INTERVAL" envDefault:"1h" yaml:"tipCheckInterval,omitempty"`
	// PollOffRecheck is how often the poller looks again at a configuration
	// that turns polling off, so that turning it back on takes effect
	// without a restart.
	PollOffRecheck time.Duration `env:"KRITIKA_TUNING_POLLER_POLL_OFF_RECHECK" envDefault:"1m" yaml:"pollOffRecheck,omitempty"`
	// ReachTimeout bounds one connection's repository listing: the leader
	// lists right after applying the configuration and applies no change
	// until it is done, so a hung forge must not hold that up.
	ReachTimeout time.Duration `env:"KRITIKA_TUNING_POLLER_REACH_TIMEOUT" envDefault:"1m" yaml:"reachTimeout,omitempty"`
	// MaxCloseChecks bounds how many pull requests one poll of a repository
	// asks the forge about one by one. The rest wait for the next poll.
	MaxCloseChecks int `env:"KRITIKA_TUNING_POLLER_MAX_CLOSE_CHECKS" envDefault:"50" yaml:"maxCloseChecks,omitempty"`
	// ReactionWindow is how long after its latest review a pull request's
	// reactions are read again: a finding is reacted to soon after it is
	// posted, if at all.
	ReactionWindow time.Duration `env:"KRITIKA_TUNING_POLLER_REACTION_WINDOW" envDefault:"168h" yaml:"reactionWindow,omitempty"`
	// ReactionPulls bounds the pull requests that one poll reads the
	// reactions of, per account, the most recently reviewed first, so that
	// an account with many open pull requests does not spend its API quota
	// on them every poll.
	ReactionPulls int `env:"KRITIKA_TUNING_POLLER_REACTION_PULLS" envDefault:"30" yaml:"reactionPulls,omitempty"`
	// ForgeRateLimitWait is how long the looks at an account's default
	// branches pause for a forge's 429 that names no wait.
	ForgeRateLimitWait time.Duration `env:"KRITIKA_TUNING_POLLER_FORGE_RATE_LIMIT_WAIT" envDefault:"1m" yaml:"forgeRateLimitWait,omitempty"`
}

// Ingest holds the tunable values of package ingest.
type Ingest struct {
	// DispatchTimeout bounds one webhook dispatch, which does not end with
	// its request.
	DispatchTimeout time.Duration `env:"KRITIKA_TUNING_INGEST_DISPATCH_TIMEOUT" envDefault:"30s" yaml:"dispatchTimeout,omitempty"`
}

func init() {
	crossField = append(crossField,
		func(v Values) error {
			j := v.JobTimeout
			if j.MaxRunnerDeadline() <= 0 {
				return fmt.Errorf("%s %w: it must exceed %s and %s together, and %s", fieldLabel("jobTimeout.maxJobTimeout"), errNoDeadline,
					fieldLabel("jobTimeout.leaseWaitHeadroom"), fieldLabel("jobTimeout.publishHeadroom"), fieldLabel("jobTimeout.indexWriteHeadroom"))
			}
			return nil
		},
		func(v Values) error {
			if v.JobTimeout.MaxAgentTimeout() <= 0 {
				return fmt.Errorf("%s leaves no agent timeout: it must exceed %s, %s and %s together", fieldLabel("jobTimeout.maxJobTimeout"),
					fieldLabel("jobTimeout.agentFetchHeadroom"), fieldLabel("jobTimeout.leaseWaitHeadroom"), fieldLabel("jobTimeout.publishHeadroom"))
			}
			return nil
		},
		func(v Values) error {
			j := v.JobTimeout
			if j.MaxRunnerDeadline() > 0 && j.RunnerDeadline > j.MaxRunnerDeadline() {
				return fmt.Errorf("%s %w %s, less %s and %s, or less %s: %s", fieldLabel("jobTimeout.runnerDeadline"), errMustNotExceed,
					fieldLabel("jobTimeout.maxJobTimeout"), fieldLabel("jobTimeout.leaseWaitHeadroom"), fieldLabel("jobTimeout.publishHeadroom"),
					fieldLabel("jobTimeout.indexWriteHeadroom"), j.MaxRunnerDeadline())
			}
			return nil
		},
		func(v Values) error {
			if v.JobTimeout.AbandonedBeats < 2 {
				return fmt.Errorf("%s must be at least 2, so that a job's replica can fence it a beat of %s before it counts as abandoned",
					fieldLabel("jobTimeout.abandonedBeats"), fieldLabel("jobTimeout.heartbeatInterval"))
			}
			return nil
		},
		func(v Values) error {
			return notAbove("worker.confidenceTimeout", v.Worker.ConfidenceTimeout, "jobTimeout.publishHeadroom", v.JobTimeout.PublishHeadroom)
		},
		func(v Values) error {
			return notAbove("worker.mergeTimeout", v.Worker.MergeTimeout, "jobTimeout.publishHeadroom", v.JobTimeout.PublishHeadroom)
		},
		func(v Values) error {
			return notAbove("worker.snoozeMin", v.Worker.SnoozeMin, "worker.snoozeMax", v.Worker.SnoozeMax)
		},
		func(v Values) error {
			return notAbove("worker.forgeWaitMin", v.Worker.ForgeWaitMin, "worker.forgeWaitMax", v.Worker.ForgeWaitMax)
		},
	)
}
