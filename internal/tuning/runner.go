package tuning

import (
	"fmt"
	"time"
)

// RunSpec holds the tunable values of package runspec. A spec travels as a
// key of the run's Secret, which Kubernetes caps at 1 MiB with the
// credentials beside it, so what grows with a pull request is cut before it
// is encoded.
type RunSpec struct {
	// HeartbeatInterval is how often a runner stamps
	// runner_runs.heartbeat_at. The worker counts a run stale after
	// reportRetryWindow + reportTimeout + 2 × heartbeatInterval
	// (Values.HeartbeatStale), so the report settings dominate it at the
	// defaults.
	HeartbeatInterval time.Duration `env:"KRITIKA_TUNING_RUN_SPEC_HEARTBEAT_INTERVAL" envDefault:"15s" yaml:"heartbeatInterval,omitempty"`
	// MaxBytes bounds an encoded spec, and it leaves the Secret room for
	// the git and gateway tokens. It can be lowered and not raised: a runner
	// of an earlier build, which spec version 1 still reaches, reads no
	// larger spec.
	MaxBytes ByteSize `env:"KRITIKA_TUNING_RUN_SPEC_MAX_BYTES" envDefault:"900Ki" yaml:"maxBytes,omitempty"`
	// MaxPromptBytes is what an encoded prompt is trimmed to, at least 32Ki
	// under MaxBytes to leave room for the rest of the spec.
	MaxPromptBytes ByteSize `env:"KRITIKA_TUNING_RUN_SPEC_MAX_PROMPT_BYTES" envDefault:"768Ki" yaml:"maxPromptBytes,omitempty"`
	// MaxPriorFindings is how many of the last review's findings a spec
	// carries, and how many dismissed ones.
	MaxPriorFindings int `env:"KRITIKA_TUNING_RUN_SPEC_MAX_PRIOR_FINDINGS" envDefault:"200" yaml:"maxPriorFindings,omitempty"`
	// MaxBodyBytes is the raw cap on the pull request body a spec carries,
	// before it is encoded. When the encoded prompt is over maxPromptBytes,
	// Prompt.Trim drops prior and dismissed findings and then shortens the
	// issue and pull request bodies until it fits. maxBytes is the separate
	// final limit on the encoded spec.
	MaxBodyBytes ByteSize `env:"KRITIKA_TUNING_RUN_SPEC_MAX_BODY_BYTES" envDefault:"64Ki" yaml:"maxBodyBytes,omitempty"`
	// MaxIssueBytes is the raw cap on each linked issue's body a spec
	// carries: the prompt shows a few thousand characters of one. Prompt.Trim
	// shortens issue bodies, as it does the pull request's, when the encoded
	// prompt is still over maxPromptBytes once the findings are dropped.
	MaxIssueBytes ByteSize `env:"KRITIKA_TUNING_RUN_SPEC_MAX_ISSUE_BYTES" envDefault:"16Ki" yaml:"maxIssueBytes,omitempty"`
	// ReportRetryWindow is how long a runner starts new attempts at a
	// report while the gateway is out of reach, and ReportTimeout how long
	// one of them can then hang, since a replica that dies mid-request
	// answers nothing. A replica restarts under the run in every rollout,
	// and a lost report can be a model's answer already paid for. A
	// heartbeat takes the same route, so the worker's patience with a
	// silent run must outlast both.
	ReportRetryWindow time.Duration `env:"KRITIKA_TUNING_RUN_SPEC_REPORT_RETRY_WINDOW" envDefault:"3m" yaml:"reportRetryWindow,omitempty"`
	ReportTimeout     time.Duration `env:"KRITIKA_TUNING_RUN_SPEC_REPORT_TIMEOUT" envDefault:"2m" yaml:"reportTimeout,omitempty"`
	// IndexUploadTimeout is how long one upload of a run's index can take.
	// The indexer's limits allow hundreds of megabytes, so the gateway's
	// usual bound on reading a request does not hold for it.
	IndexUploadTimeout time.Duration `env:"KRITIKA_TUNING_RUN_SPEC_INDEX_UPLOAD_TIMEOUT" envDefault:"30m" yaml:"indexUploadTimeout,omitempty"`
}

// Executor holds the tunable values of package executor.
type Executor struct {
	// LogTailBytes is how much of a runner's output is kept: enough to
	// diagnose, small enough to store per run.
	LogTailBytes ByteSize `env:"KRITIKA_TUNING_EXECUTOR_LOG_TAIL_BYTES" envDefault:"64Ki" yaml:"logTailBytes,omitempty"`
	// LogTailLines and LogReadBytes bound the read of a runner pod's log
	// for its tail: enough lines to fill logTailBytes many times over, and
	// a byte ceiling well above that.
	LogTailLines int      `env:"KRITIKA_TUNING_EXECUTOR_LOG_TAIL_LINES" envDefault:"5000" yaml:"logTailLines,omitempty"`
	LogReadBytes ByteSize `env:"KRITIKA_TUNING_EXECUTOR_LOG_READ_BYTES" envDefault:"4Mi" yaml:"logReadBytes,omitempty"`
	// Poll is how often a run's wait reads the watch again, for the grace
	// that a stuck pod or a podless Job is given, and a new Job's way to
	// the watch.
	Poll time.Duration `env:"KRITIKA_TUNING_EXECUTOR_POLL" envDefault:"3s" yaml:"poll,omitempty"`
	// MaxMissing is how many polls in a row a Job can be missing from the
	// watch before it is given up on.
	MaxMissing int `env:"KRITIKA_TUNING_EXECUTOR_MAX_MISSING" envDefault:"10" yaml:"maxMissing,omitempty"`
	// StartGrace is how long a runner pod can stay unable to start before
	// the run is given up: uncreated, unscheduled, or with its container
	// refused. The run is given up so that it does not hold its review's
	// slot until the Job's deadline. StartGrace outlasts what clears on
	// its own: a registry's hiccup, a node on its way from an autoscaler.
	StartGrace time.Duration `env:"KRITIKA_TUNING_EXECUTOR_START_GRACE" envDefault:"3m" yaml:"startGrace,omitempty"`
	// JobTTL is how long a finished Job stays for kubectl when the
	// jobTemplate sets no ttlSecondsAfterFinished: a whole number of
	// seconds, at least one. The run row keeps everything the Job knew.
	JobTTL time.Duration `env:"KRITIKA_TUNING_EXECUTOR_JOB_TTL" envDefault:"10m" yaml:"jobTtl,omitempty"`
	// APITimeout bounds one call to the Kubernetes API about a run's Job
	// that must not wait on the run: listing its pods, deleting it, and
	// the dry run of the Job at startup.
	APITimeout time.Duration `env:"KRITIKA_TUNING_EXECUTOR_API_TIMEOUT" envDefault:"20s" yaml:"apiTimeout,omitempty"`
	// FinishTimeout bounds collecting a finished run's pod: its listing
	// and the read of its log's tail.
	FinishTimeout time.Duration `env:"KRITIKA_TUNING_EXECUTOR_FINISH_TIMEOUT" envDefault:"20s" yaml:"finishTimeout,omitempty"`
	// WhyNoPodTimeout bounds reading why the Job controller created no pod
	// for a Job.
	WhyNoPodTimeout time.Duration `env:"KRITIKA_TUNING_EXECUTOR_WHY_NO_POD_TIMEOUT" envDefault:"10s" yaml:"whyNoPodTimeout,omitempty"`
	// WatchSync bounds how long the executor's start waits for its trial
	// watches and its first lists.
	WatchSync time.Duration `env:"KRITIKA_TUNING_EXECUTOR_WATCH_SYNC" envDefault:"1m" yaml:"watchSync,omitempty"`
	// EventInstanceBytes is the longest reporting instance an Event names,
	// past which it names the pod's UID, or the instance cut short when the
	// UID is longer. Kubernetes takes no more than 128 bytes.
	EventInstanceBytes ByteSize `env:"KRITIKA_TUNING_EXECUTOR_EVENT_INSTANCE_BYTES" envDefault:"128" yaml:"eventInstanceBytes,omitempty"`
	// EventNoteBytes is the longest note an Event holds. Kubernetes takes
	// no more than 1Ki.
	EventNoteBytes ByteSize `env:"KRITIKA_TUNING_EXECUTOR_EVENT_NOTE_BYTES" envDefault:"1Ki" yaml:"eventNoteBytes,omitempty"`
	// EventTimeout bounds an Event's write.
	EventTimeout time.Duration `env:"KRITIKA_TUNING_EXECUTOR_EVENT_TIMEOUT" envDefault:"10s" yaml:"eventTimeout,omitempty"`
}

// Runner holds the tunable values of package runner.
type Runner struct {
	// GatewayWait is how long a runner waits for its gateway to answer
	// before it fetches. A runner started as the service comes up can beat
	// the gateway's Service endpoints by the readiness probe's period and a
	// little propagation. Much longer than that is an outage, and the run
	// reports it and does not sit it out.
	GatewayWait time.Duration `env:"KRITIKA_TUNING_RUNNER_GATEWAY_WAIT" envDefault:"90s" yaml:"gatewayWait,omitempty"`
	// GatewayPoll is how often the wait tries again, and how long each try
	// can take.
	GatewayPoll time.Duration `env:"KRITIKA_TUNING_RUNNER_GATEWAY_POLL" envDefault:"2s" yaml:"gatewayPoll,omitempty"`

	// FetchWriteBytes is the most that a run's fetches write of their
	// files, beside its checkout. The run tool's kept outputs share it.
	FetchWriteBytes ByteSize `env:"KRITIKA_TUNING_RUNNER_FETCH_WRITE_BYTES" envDefault:"256Mi" yaml:"fetchWriteBytes,omitempty"`
	// FetchWireBytes is the most that one fetch takes from the server, and
	// FetchTimeout the most wall time.
	FetchWireBytes ByteSize      `env:"KRITIKA_TUNING_RUNNER_FETCH_WIRE_BYTES" envDefault:"128Mi" yaml:"fetchWireBytes,omitempty"`
	FetchTimeout   time.Duration `env:"KRITIKA_TUNING_RUNNER_FETCH_TIMEOUT" envDefault:"2m" yaml:"fetchTimeout,omitempty"`
	// FetchDiffBytes is the most of a diff that a fetch keeps, the same as
	// the run tool keeps of an output.
	FetchDiffBytes ByteSize `env:"KRITIKA_TUNING_RUNNER_FETCH_DIFF_BYTES" envDefault:"4Mi" yaml:"fetchDiffBytes,omitempty"`
	// MaxTagListings is how many times each of fetch_repo and fetch_oci
	// lists tags in a run, each counting its own: the listing that a fetch
	// naming a missing ref makes to name the closest included.
	MaxTagListings int `env:"KRITIKA_TUNING_RUNNER_MAX_TAG_LISTINGS" envDefault:"8" yaml:"maxTagListings,omitempty"`
	// TagsShown is the most tag names a listing's answer shows, and
	// ClosestTagsShown the most that an error for a missing ref does.
	TagsShown        int `env:"KRITIKA_TUNING_RUNNER_TAGS_SHOWN" envDefault:"200" yaml:"tagsShown,omitempty"`
	ClosestTagsShown int `env:"KRITIKA_TUNING_RUNNER_CLOSEST_TAGS_SHOWN" envDefault:"10" yaml:"closestTagsShown,omitempty"`
	// ChangedShown is the most paths that a fetched diff's answer names
	// before the patches.
	ChangedShown int `env:"KRITIKA_TUNING_RUNNER_CHANGED_SHOWN" envDefault:"100" yaml:"changedShown,omitempty"`
	// MaxDirNameBytes bounds the name of a fetch's directory, beside which
	// its diff is kept as the name and .diff: so at most 250, for a file
	// system's 255 bytes. The fetch's number is kept whole however small it
	// is.
	MaxDirNameBytes int `env:"KRITIKA_TUNING_RUNNER_MAX_DIR_NAME_BYTES" envDefault:"100" yaml:"maxDirNameBytes,omitempty"`

	// OCIWireBytes is the most that one fetch_oci call takes from the
	// registry, and OCITimeout the most wall time.
	OCIWireBytes ByteSize      `env:"KRITIKA_TUNING_RUNNER_OCI_WIRE_BYTES" envDefault:"16Mi" yaml:"ociWireBytes,omitempty"`
	OCITimeout   time.Duration `env:"KRITIKA_TUNING_RUNNER_OCI_TIMEOUT" envDefault:"1m" yaml:"ociTimeout,omitempty"`
	// OCIMetadataBytes is the largest manifest or config that fetch_oci
	// reads.
	OCIMetadataBytes ByteSize `env:"KRITIKA_TUNING_RUNNER_OCI_METADATA_BYTES" envDefault:"4Mi" yaml:"ociMetadataBytes,omitempty"`
	// OCIConfigShown is what fetch_oci shows of a config that is no
	// image's.
	OCIConfigShown ByteSize `env:"KRITIKA_TUNING_RUNNER_OCI_CONFIG_SHOWN" envDefault:"8Ki" yaml:"ociConfigShown,omitempty"`
	// OCITagPages is how many pages long a tag listing of fetch_oci is.
	OCITagPages int `env:"KRITIKA_TUNING_RUNNER_OCI_TAG_PAGES" envDefault:"50" yaml:"ociTagPages,omitempty"`

	// SimilarTimeout bounds one similar-code request to the gateway, which
	// can wait for the account's embedding slot.
	SimilarTimeout time.Duration `env:"KRITIKA_TUNING_RUNNER_SIMILAR_TIMEOUT" envDefault:"1m" yaml:"similarTimeout,omitempty"`
	// MaxSimilarAnswerBody bounds the gateway's answer to a similar-code
	// request, as JSON encodes it: contextPack.similarLimit chunks, their
	// text escaped at worst. The gateway leaves its lowest ranked chunks out
	// of an answer that would pass it.
	MaxSimilarAnswerBody ByteSize `env:"KRITIKA_TUNING_RUNNER_MAX_SIMILAR_ANSWER_BODY" envDefault:"1Mi" yaml:"maxSimilarAnswerBody,omitempty"`
	// SimilarReportWindow bounds what a run spends, before it searches, on
	// learning whether its repository has an index and reporting its
	// exclusions. Similar code is best effort, and the review waits for it.
	SimilarReportWindow time.Duration `env:"KRITIKA_TUNING_RUNNER_SIMILAR_REPORT_WINDOW" envDefault:"20s" yaml:"similarReportWindow,omitempty"`
	// MaxSimilarShown is the most similar chunks a prompt shows.
	MaxSimilarShown int `env:"KRITIKA_TUNING_RUNNER_MAX_SIMILAR_SHOWN" envDefault:"10" yaml:"maxSimilarShown,omitempty"`
	// SearchCalls is how many times one run can call search_code.
	SearchCalls int `env:"KRITIKA_TUNING_RUNNER_SEARCH_CALLS" envDefault:"10" yaml:"searchCalls,omitempty"`
	// SearchShown is the most chunks one search_code answer shows. It asks
	// the gateway for twice as many, as it leaves out those that the head
	// no longer has or that the run ignores, and the gateway caps one
	// query's answer at gateway.similarPerQuery unless asked for more.
	SearchShown int `env:"KRITIKA_TUNING_RUNNER_SEARCH_SHOWN" envDefault:"4" yaml:"searchShown,omitempty"`

	// MaxRuleFileDepth bounds how deep below a rule file directory files
	// are looked for, against a tree that nests without end.
	MaxRuleFileDepth int `env:"KRITIKA_TUNING_RUNNER_MAX_RULE_FILE_DEPTH" envDefault:"8" yaml:"maxRuleFileDepth,omitempty"`
	// ConversationTimeout bounds asking the gateway for the conversation
	// to carry on.
	ConversationTimeout time.Duration `env:"KRITIKA_TUNING_RUNNER_CONVERSATION_TIMEOUT" envDefault:"30s" yaml:"conversationTimeout,omitempty"`
	// CarryShare bounds the conversation a run carries on to 1/CarryShare
	// of its token budget. Every step reads the whole of it again. Without
	// this bound, a budget that the monthly cap cut short leaves the agent
	// too few steps to work in.
	CarryShare int `env:"KRITIKA_TUNING_RUNNER_CARRY_SHARE" envDefault:"4" yaml:"carryShare,omitempty"`
	// MaxPackDiffBytes is how much of a diff, and of a delta diff, a
	// context pack keeps: whole files in order, with a file left out if it
	// would pass the limit. A pull request that regenerates a large text
	// file otherwise puts the whole of it in the service's memory. The
	// prompt's own budget is far smaller, so the agent is sent no less for
	// it.
	MaxPackDiffBytes ByteSize `env:"KRITIKA_TUNING_RUNNER_MAX_PACK_DIFF_BYTES" envDefault:"4Mi" yaml:"maxPackDiffBytes,omitempty"`
	// FailureTimeout bounds recording a run's error.
	FailureTimeout time.Duration `env:"KRITIKA_TUNING_RUNNER_FAILURE_TIMEOUT" envDefault:"10s" yaml:"failureTimeout,omitempty"`
	// MaxFailureCause is the most of a run's error that its failure report
	// carries. The run's row keeps less, so a git error of any length
	// still fits the report.
	MaxFailureCause ByteSize `env:"KRITIKA_TUNING_RUNNER_MAX_FAILURE_CAUSE" envDefault:"8Ki" yaml:"maxFailureCause,omitempty"`

	// ReportBackoff is the first wait between attempts at a report, which
	// doubles up to ReportMaxBackoff.
	ReportBackoff    time.Duration `env:"KRITIKA_TUNING_RUNNER_REPORT_BACKOFF" envDefault:"1s" yaml:"reportBackoff,omitempty"`
	ReportMaxBackoff time.Duration `env:"KRITIKA_TUNING_RUNNER_REPORT_MAX_BACKOFF" envDefault:"15s" yaml:"reportMaxBackoff,omitempty"`
	// HeartbeatTimeout bounds one heartbeat, as runSpec.reportTimeout does
	// a report. A heartbeat is tried once: another comes along soon. One
	// that hangs for all of it, a beat after the last that landed, leaves
	// the next a beat and as long again before the worker deems the run
	// dead.
	HeartbeatTimeout time.Duration `env:"KRITIKA_TUNING_RUNNER_HEARTBEAT_TIMEOUT" envDefault:"10s" yaml:"heartbeatTimeout,omitempty"`
	// MaxReportAnswer bounds what the gateway's answer to a report is read
	// to: the part checkpoints of a run, or an error.
	MaxReportAnswer ByteSize `env:"KRITIKA_TUNING_RUNNER_MAX_REPORT_ANSWER" envDefault:"256Mi" yaml:"maxReportAnswer,omitempty"`

	// MinPartTime is the least time a part of a split review starts with,
	// or half its timeout when that is less. Less is not enough for an
	// agent to read its files and answer. Its whole timeout is too much.
	// The run's deadline leaves the last round no more than that, less
	// what the run took to reach it.
	MinPartTime time.Duration `env:"KRITIKA_TUNING_RUNNER_MIN_PART_TIME" envDefault:"2m" yaml:"minPartTime,omitempty"`
	// PartSlotsPoll is how often a split review asks the worker for the
	// slots its parts may run in, and PartSlotsTimeout how long one ask can
	// take.
	PartSlotsPoll    time.Duration `env:"KRITIKA_TUNING_RUNNER_PART_SLOTS_POLL" envDefault:"2s" yaml:"partSlotsPoll,omitempty"`
	PartSlotsTimeout time.Duration `env:"KRITIKA_TUNING_RUNNER_PART_SLOTS_TIMEOUT" envDefault:"5s" yaml:"partSlotsTimeout,omitempty"`
	// InstallTimeout bounds each step of installing a repository's tool:
	// the registry's fetch, a checksum's lookup, and each attempt at the
	// download, as fetchTimeout bounds a fetch.
	InstallTimeout time.Duration `env:"KRITIKA_TUNING_RUNNER_INSTALL_TIMEOUT" envDefault:"2m" yaml:"installTimeout,omitempty"`
	// NoteOmittedPaths is how many omitted paths a review note names.
	NoteOmittedPaths int `env:"KRITIKA_TUNING_RUNNER_NOTE_OMITTED_PATHS" envDefault:"5" yaml:"noteOmittedPaths,omitempty"`
	// GatewayRetries is how many times a step the gateway answered with a
	// 500, its own database not answering, is sent again. The gateway
	// marks every other refusal final, since it already retried the
	// provider.
	GatewayRetries int `env:"KRITIKA_TUNING_RUNNER_GATEWAY_RETRIES" envDefault:"2" yaml:"gatewayRetries,omitempty" tuning:"zero"`
	// CancelWriteTimeout bounds writing a cancelled agent's row, well
	// inside a runner pod's termination grace period.
	CancelWriteTimeout time.Duration `env:"KRITIKA_TUNING_RUNNER_CANCEL_WRITE_TIMEOUT" envDefault:"5s" yaml:"cancelWriteTimeout,omitempty"`
}

// HeartbeatStale is how old a runner's heartbeat can get before the worker
// treats the run as dead. A heartbeat goes through the gateway as a report
// does, so an outage silences both. It outlasts a report's last attempt,
// RunSpec.ReportRetryWindow and RunSpec.ReportTimeout, and the beats either
// side of it, because ending the run sooner can lose an answer already paid
// for. A pod that dies is seen sooner, by its Job. Only a wedged runner
// waits this long. It saturates rather than overflows.
func (v Values) HeartbeatStale() time.Duration {
	r := v.RunSpec
	report := satAdd(int64(r.ReportRetryWindow), int64(r.ReportTimeout))
	return time.Duration(satAdd(report, satMul(2, int64(r.HeartbeatInterval))))
}

// The fixed limits that the runner's and the executor's values keep within.
const (
	// specV1Bytes is the largest spec that a runner of spec version 1 reads,
	// whatever its build: a runner of a build before runSpec.maxBytes was
	// tunable reads no larger one.
	specV1Bytes = 900 << 10
	// minSpecBytes is the least runSpec.maxBytes: room for a spec's fields
	// that nothing trims, its run, repository, model and limits, beside
	// what is left of its prompt.
	minSpecBytes = 64 << 10
	// specReserveBytes is the least of runSpec.maxBytes that
	// runSpec.maxPromptBytes leaves to the rest of a spec: its run, repository,
	// refs, model and limits, which no trim shrinks.
	specReserveBytes = 32 << 10
	// kubeEventInstanceBytes and kubeEventNoteBytes are the longest
	// reporting instance and note that Kubernetes takes in an Event.
	kubeEventInstanceBytes = 128
	kubeEventNoteBytes     = 1024
	// fileNameBytes is the longest file name of the file systems a runner
	// writes to, and maxDirNameBytes the longest name of a fetch's
	// directory that leaves room for its diff's .diff.
	fileNameBytes   = 255
	maxDirNameBytes = fileNameBytes - len(".diff")
)

func init() {
	crossField = append(crossField,
		func(v Values) error {
			chunk := satMul(jsonGrowth, satAdd(int64(v.Indexer.MaxChunkBytes), indexChunkFields))
			if need := satAdd(satMul(int64(v.ContextPack.SimilarLimit), chunk), similarEnvelope); int64(v.Runner.MaxSimilarAnswerBody) < need {
				return fmt.Errorf("%s (%s) must hold %s chunks of %s, escaped, %d bytes",
					fieldLabel("runner.maxSimilarAnswerBody"), v.Runner.MaxSimilarAnswerBody,
					fieldLabel("contextPack.similarLimit"),
					fieldLabel("indexer.maxChunkBytes"), need)
			}
			return nil
		},
		func(v Values) error {
			if v.RunSpec.MaxBytes > specV1Bytes {
				return fmt.Errorf("%s (%s) %w %s, what a runner of spec version 1 reads, "+
					"inside Kubernetes' %s Secret limit, which the run's credentials share",
					fieldLabel("runSpec.maxBytes"), v.RunSpec.MaxBytes, errMustNotExceed, ByteSize(specV1Bytes), ByteSize(SecretBytes))
			}
			return nil
		},
		func(v Values) error {
			if v.RunSpec.MaxBytes < minSpecBytes {
				return fmt.Errorf("%s (%s) must be at least %s, room for the fields of a spec that are not trimmed",
					fieldLabel("runSpec.maxBytes"), v.RunSpec.MaxBytes, ByteSize(minSpecBytes))
			}
			return nil
		},
		func(v Values) error {
			if v.RunSpec.MaxPromptBytes > v.RunSpec.MaxBytes-specReserveBytes {
				return fmt.Errorf("%s (%s) must leave %s of %s (%s) to the rest of the spec",
					fieldLabel("runSpec.maxPromptBytes"), v.RunSpec.MaxPromptBytes, ByteSize(specReserveBytes),
					fieldLabel("runSpec.maxBytes"), v.RunSpec.MaxBytes)
			}
			return nil
		},
		func(v Values) error {
			return notAbove("runner.reportBackoff", v.Runner.ReportBackoff, "runner.reportMaxBackoff", v.Runner.ReportMaxBackoff)
		},
		func(v Values) error {
			return notAbove("executor.logTailBytes", v.Executor.LogTailBytes, "executor.logReadBytes", v.Executor.LogReadBytes)
		},
		func(v Values) error {
			if v.Executor.EventInstanceBytes > kubeEventInstanceBytes {
				return fmt.Errorf("%s (%s) %w Kubernetes' limit on an Event's reporting instance, %d bytes",
					fieldLabel("executor.eventInstanceBytes"), v.Executor.EventInstanceBytes, errMustNotExceed, kubeEventInstanceBytes)
			}
			return nil
		},
		func(v Values) error {
			if v.Executor.EventNoteBytes > kubeEventNoteBytes {
				return fmt.Errorf("%s (%s) %w Kubernetes' limit on an Event's note, %d bytes",
					fieldLabel("executor.eventNoteBytes"), v.Executor.EventNoteBytes, errMustNotExceed, kubeEventNoteBytes)
			}
			return nil
		},
		func(v Values) error {
			if ttl := v.Executor.JobTTL; ttl < time.Second || ttl%time.Second != 0 {
				return fmt.Errorf("%s (%s) must be a whole number of seconds, at least 1s, as a Job's ttlSecondsAfterFinished is, "+
					"so that a finished Job stays for its log to be read",
					fieldLabel("executor.jobTtl"), v.Executor.JobTTL)
			}
			return nil
		},
		func(v Values) error {
			if v.Runner.MaxDirNameBytes > maxDirNameBytes {
				return fmt.Errorf("%s (%d) %w %d bytes, so that the fetch's diff, its name and .diff, fits a file system's %d",
					fieldLabel("runner.maxDirNameBytes"), v.Runner.MaxDirNameBytes, errMustNotExceed, maxDirNameBytes, fileNameBytes)
			}
			return nil
		},
		func(v Values) error {
			// A beat that hangs from one beat after the last that landed, and
			// the next, a beat later and as slow, must land within HeartbeatStale.
			beats := satMul(2, int64(v.RunSpec.HeartbeatInterval))
			if satAdd(satMul(2, int64(v.Runner.HeartbeatTimeout)), beats) > int64(v.HeartbeatStale()) {
				return fmt.Errorf("%s (%s) %w half of %s and %s together, %s, so that a heartbeat that hangs leaves "+
					"another time to land before the worker deems the run dead",
					fieldLabel("runner.heartbeatTimeout"), v.Runner.HeartbeatTimeout, errMustNotExceed,
					fieldLabel("runSpec.reportRetryWindow"), fieldLabel("runSpec.reportTimeout"),
					time.Duration(satAdd(int64(v.RunSpec.ReportRetryWindow), int64(v.RunSpec.ReportTimeout))))
			}
			return nil
		},
	)
}
