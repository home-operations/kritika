package runspec

import (
	"encoding/json"
	"time"

	"github.com/home-operations/kritika/internal/model"
)

// What a run reports to the gateway, under these paths, with Secrets.ReportToken
// as its bearer token. They are the run's only way to write what it found. A
// run holds no database credential, and the gateway writes for it, to its own
// run alone.
const (
	// ReportPrefix is what every path below starts with.
	ReportPrefix = "/v1/run/"
	// ReportAuthorizePath answers GET once the token authorizes a run.
	ReportAuthorizePath = ReportPrefix + "authorize"
	// ReportHeartbeatPath takes an empty POST.
	ReportHeartbeatPath = ReportPrefix + "heartbeat"
	// ReportPhasePath takes a POST of a PhaseReport.
	ReportPhasePath = ReportPrefix + "phase"
	// ReportFailurePath takes a POST of a FailureReport.
	ReportFailurePath = ReportPrefix + "failure"
	// ReportContextPath takes a POST of a ContextReport.
	ReportContextPath = ReportPrefix + "context"
	// ReportAgentPath takes a POST of an AgentReport.
	ReportAgentPath = ReportPrefix + "agent"
	// ReportIndexPath takes a POST of newline-delimited JSON: an IndexReport,
	// then each of its IndexChunks.
	ReportIndexPath = ReportPrefix + "index"
	// ReportCheckpointsPath answers GET with the PartCheckpoints of the input
	// that the ReportCheckpointInput query parameter names, and takes a POST
	// of a CheckpointReport.
	ReportCheckpointsPath = ReportPrefix + "part-checkpoints"
	// ReportPartsPath takes a POST of a PartsReport and answers with a
	// PartSlots.
	ReportPartsPath = ReportPrefix + "parts-remaining"
)

// ReportCheckpointInput names a part input hash on ReportCheckpointsPath.
const ReportCheckpointInput = "input"

// IndexUploadTimeout is how long one upload of a run's index can take. The
// indexer's limits allow hundreds of megabytes, so the gateway's usual bound
// on reading a request does not hold for it.
const IndexUploadTimeout = 30 * time.Minute

// Index modes, as index_runs and index_packs spell them.
const (
	IndexModeFull        = "full"
	IndexModeIncremental = "incremental"
)

// PhaseReport moves the run to Phase.
type PhaseReport struct {
	Phase string `json:"phase"`
}

// FailureReport moves the run to the failed phase. Cause is already masked.
type FailureReport struct {
	Cause string `json:"cause"`
}

// ContextReport commits a review's context and its next phase together.
type ContextReport struct {
	Pack  ContextPack `json:"pack"`
	Phase string      `json:"phase"`
}

// ContextPack is a runner's review decisions and encoded context.
type ContextPack struct {
	Head         string          `json:"head"`
	Base         string          `json:"base"`
	PatchID      string          `json:"patchId"`
	Diff         string          `json:"diff"`
	ChangedPaths []string        `json:"changedPaths"`
	Stages       json.RawMessage `json:"stages"`
	Files        json.RawMessage `json:"files"`
	Notes        []string        `json:"notes"`
	PriorHead    *string         `json:"priorHead"`
	DeltaDiff    string          `json:"deltaDiff"`
	DeltaPaths   []string        `json:"deltaPaths"`
	Scope        string          `json:"scope"`
	ScopeReason  string          `json:"scopeReason"`
	SkipReason   string          `json:"skipReason"`
	SkipDetail   string          `json:"skipDetail"`
	RuleIDs      []string        `json:"ruleIds"`
}

// AgentReport commits an agent's result, its retained conversation and the
// run's next phase together.
type AgentReport struct {
	Record AgentRecord `json:"record"`
	Phase  string      `json:"phase"`
}

// AgentRecord is a redacted agent result and an optional retained
// conversation. JSON fields carry the runner's encoded payloads. An absent
// Result is SQL NULL, so these stay out of the encoding when empty rather
// than arrive as a JSON null.
type AgentRecord struct {
	Stop            string          `json:"stop"`
	ForcedBy        string          `json:"forcedBy"`
	Result          json.RawMessage `json:"result,omitempty"`
	Steps           int             `json:"steps"`
	ToolCalls       json.RawMessage `json:"toolCalls,omitempty"`
	Timeline        json.RawMessage `json:"timeline,omitempty"`
	Sources         json.RawMessage `json:"sources,omitempty"`
	Usage           model.Usage     `json:"usage"`
	CostUSD         float64         `json:"costUsd"`
	UnpricedSteps   int             `json:"unpricedSteps"`
	SkillsOffered   []string        `json:"skillsOffered"`
	SkillsOpened    []string        `json:"skillsOpened"`
	CommandsOffered []string        `json:"commandsOffered"`
	CommandsRun     []string        `json:"commandsRun"`
	CommandErrors   []string        `json:"commandErrors"`
	CommandCounts   map[string]int  `json:"commandCounts"`
	Model           string          `json:"model"`
	Error           string          `json:"error"`
	ContinuedFrom   string          `json:"continuedFrom"`
	Parts           json.RawMessage `json:"parts,omitempty"`
	// Conversation is bytes, not raw JSON: a provider's cached prefix must
	// match byte for byte, and encoding raw JSON rewrites it.
	Conversation []byte `json:"conversation"`
	Tokens       int64  `json:"tokens"`
	Session      string `json:"session"`
	GrantedModel string `json:"grantedModel"`
}

// IndexReport opens an index upload: its pack, and how many chunks follow.
type IndexReport struct {
	Pack   IndexPack `json:"pack"`
	Chunks int       `json:"chunks"`
}

// IndexPack is what an index runner wrote: the mode it built in, which
// can be full where an incremental step was asked for, and the paths that
// changed since the base.
type IndexPack struct {
	Mode         string   `json:"mode"`
	Base         string   `json:"base"`
	ChangedPaths []string `json:"changedPaths"`
}

// IndexChunk is one chunk that an index runner staged, before embedding.
type IndexChunk struct {
	Path      string `json:"path"`
	Language  string `json:"language"`
	Symbol    string `json:"symbol"`
	Kind      string `json:"kind"`
	Scope     string `json:"scope"`
	Text      string `json:"text"`
	StartLine int    `json:"startLine"`
	EndLine   int    `json:"endLine"`
}

// CheckpointReport saves a completed part of a split review.
type CheckpointReport struct {
	Input   string          `json:"input"`
	Part    int             `json:"part"`
	Payload json.RawMessage `json:"payload"`
}

// PartCheckpoint is a completed part and the run that paid for it.
type PartCheckpoint struct {
	Part      int             `json:"part"`
	SourceRun string          `json:"sourceRun"`
	Payload   json.RawMessage `json:"payload"`
}

// PartsReport says how many parts of a split review are unfinished.
type PartsReport struct {
	Remaining int `json:"remaining"`
}

// PartSlots is how many parts the run can hold at once: capacity that only
// the worker grants.
type PartSlots struct {
	Slots int `json:"slots"`
}

// TimelineStep is one agent step as agent_runs.timeline records it.
type TimelineStep struct {
	Index int `json:"index"`
	// Part is the part of a split review that the step was for, from 1. It is
	// 0 when the review was not split.
	Part         int      `json:"part,omitempty"`
	Tools        []string `json:"tools"`
	DurationMS   int64    `json:"duration_ms"`
	OutputBytes  int      `json:"output_bytes"`
	InputTokens  int64    `json:"input_tokens"`
	OutputTokens int64    `json:"output_tokens"`
}

// AgentPart is how the agent of one part of a split review ended: the
// files it reviewed, why it stopped and, once it submitted, the summary it
// wrote.
type AgentPart struct {
	ReusedFrom string `json:"reused_from,omitempty"`
	// Reviewer is the reviewer that the part was for in a review with several,
	// "" in a review with the default reviewer alone. Full says that it
	// reviewed the whole change where the review built on the last one.
	Reviewer string          `json:"agent,omitempty"`
	Full     bool            `json:"full,omitempty"`
	Paths    []string        `json:"paths"`
	Stop     string          `json:"stop"`
	Error    string          `json:"error,omitempty"`
	Steps    int             `json:"steps"`
	Summary  json.RawMessage `json:"summary,omitempty"`
	// ForcedBy is the limit that made the loop tell the part's agent to
	// submit, "" when it never did.
	ForcedBy string `json:"forced_by,omitempty"`
}
