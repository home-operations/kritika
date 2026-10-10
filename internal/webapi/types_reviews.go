package webapi

import (
	"encoding/json"
	"time"

	"github.com/home-operations/kritika/internal/review"
	"github.com/home-operations/kritika/internal/store"
)

// The shapes of a review and what it read, ran and made, which
// routes_reviews.go serves.

// ReviewInfo is a review row in full.
type ReviewInfo struct {
	Review
	Pull PullRef `json:"pull"`
	// PullState is open or closed, and PullMerged whether it was merged: a
	// merged or closed pull request takes no more reviews.
	PullState     string  `json:"pullState"`
	PullMerged    bool    `json:"pullMerged"`
	ScopeReason   string  `json:"scopeReason"`
	MergeBaseSHA  string  `json:"mergeBaseSha"`
	PatchID       string  `json:"patchId"`
	PriorReviewID *string `json:"priorReviewId"`
	// NewestReviewID is the pull request's newest review that was not
	// skipped, null when none is newer than this.
	NewestReviewID    *string    `json:"newestReviewId"`
	CancelRequestedAt *time.Time `json:"cancelRequestedAt"`
}

// Summary is a review's overall take and praise. Diagram is the Mermaid
// source of its flow diagram, "" when the review drew none.
type Summary struct {
	Headline string   `json:"headline,omitempty"`
	Take     string   `json:"take"`
	Praise   []string `json:"praise"`
	Diagram  string   `json:"diagram,omitempty"`
}

// Finding is one finding in full.
type Finding struct {
	ID             string          `json:"id"`
	Path           string          `json:"path"`
	Line           int             `json:"line"`
	EndLine        int             `json:"endLine"`
	Severity       review.Severity `json:"severity"`
	Category       review.Category `json:"category"`
	Title          string          `json:"title"`
	Explanation    string          `json:"explanation"`
	SuggestedFix   string          `json:"suggestedFix"`
	Replacement    string          `json:"replacement"`
	AgentPrompt    string          `json:"agentPrompt"`
	Fingerprint    string          `json:"fingerprint"`
	PostedInline   bool            `json:"postedInline"`
	ForgeCommentID *int64          `json:"forgeCommentId"`
	CreatedAt      time.Time       `json:"createdAt"`
	// ReactionsUp and ReactionsDown count the 👍 and 👎 on its inline
	// comment, as the poller last read them.
	ReactionsUp   int `json:"reactionsUp"`
	ReactionsDown int `json:"reactionsDown"`
	// Rules are the ids of the review rules it enforces.
	Rules []string `json:"rules"`
	// Status is what became of it on its pull request: whether a later
	// review no longer reported it, or a maintainer dismissed it, with
	// DismissReason the reason they gave.
	Status        store.FindingStatus `json:"status"`
	DismissReason string              `json:"dismissReason"`
}

// RunnerRun is the Kubernetes Job that prepared a review.
type RunnerRun struct {
	ID                string     `json:"id"`
	Phase             string     `json:"phase"`
	JobName           string     `json:"jobName"`
	PodName           string     `json:"podName"`
	NodeName          string     `json:"nodeName"`
	CreatedAt         time.Time  `json:"createdAt"`
	ScheduledAt       *time.Time `json:"scheduledAt"`
	StartedAt         *time.Time `json:"startedAt"`
	FinishedAt        *time.Time `json:"finishedAt"`
	HeartbeatAt       *time.Time `json:"heartbeatAt"`
	ExitCode          *int       `json:"exitCode"`
	TerminationReason string     `json:"terminationReason"`
	DeadlineExceeded  bool       `json:"deadlineExceeded"`
	Error             string     `json:"error"`
	LogTail           string     `json:"logTail"`
}

// TimelineStep is one agent step. Part is the part of a split review it
// was for, from 1; 0 when the review was not split.
type TimelineStep struct {
	Index        int      `json:"index"`
	Part         int      `json:"part"`
	Tools        []string `json:"tools"`
	DurationMs   int64    `json:"durationMs"`
	OutputBytes  int      `json:"outputBytes"`
	InputTokens  int64    `json:"inputTokens"`
	OutputTokens int64    `json:"outputTokens"`
}

// AgentRun is a review's tool loop. Result is the submitted
// review JSON, null unless the agent submitted. SkillsOffered are the
// repository skills the agent could read, SkillsOpened the ones it did;
// CommandsOffered and CommandsRun the same for the run tool's commands.
type AgentRun struct {
	StopReason      string          `json:"stopReason"`
	Steps           int             `json:"steps"`
	ToolCalls       map[string]int  `json:"toolCalls"`
	Timeline        []TimelineStep  `json:"timeline"`
	Sources         []string        `json:"sources"`
	SkillsOffered   []string        `json:"skillsOffered"`
	SkillsOpened    []string        `json:"skillsOpened"`
	CommandsOffered []string        `json:"commandsOffered"`
	CommandsRun     []string        `json:"commandsRun"`
	Usage           Usage           `json:"usage"`
	CostUSD         float64         `json:"costUsd"`
	UnpricedCalls   int64           `json:"unpricedCalls,omitzero"`
	Model           string          `json:"model"`
	Error           string          `json:"error"`
	CreatedAt       time.Time       `json:"createdAt"`
	Result          json.RawMessage `json:"result"`
	// CarriedReviewID is the review whose conversation the agent carried
	// on, nil when it started afresh.
	CarriedReviewID *string `json:"carriedReviewId"`
	// Parts are how the parts of a split review ended, in part order;
	// empty when it was not split.
	Parts []AgentPart `json:"parts"`
}

// AgentPart is one part of a split review: its files, why its agent
// stopped, with the error it ended on, and its steps.
type AgentPart struct {
	Paths []string `json:"paths"`
	Stop  string   `json:"stop"`
	Error string   `json:"error"`
	Steps int      `json:"steps"`
}

// UsageRow is one usage row charged to a review.
type UsageRow struct {
	RunnerRunID  string    `json:"runnerRunId,omitzero"`
	Role         string    `json:"role"`
	Model        string    `json:"model"`
	Upstream     string    `json:"upstream"`
	InputTokens  int64     `json:"inputTokens"`
	OutputTokens int64     `json:"outputTokens"`
	CostUSD      float64   `json:"costUsd"`
	ChatGPTPlan  bool      `json:"chatgptPlan,omitzero"`
	Unpriced     bool      `json:"unpriced,omitzero"`
	CreatedAt    time.Time `json:"createdAt"`
}

// Stage is one context chunk without its text.
type Stage struct {
	Stage     string `json:"stage"`
	Path      string `json:"path"`
	Language  string `json:"language"`
	Symbol    string `json:"symbol"`
	Kind      string `json:"kind"`
	Scope     string `json:"scope"`
	StartLine int    `json:"startLine"`
	EndLine   int    `json:"endLine"`
	Ref       string `json:"ref"`
	Bytes     int    `json:"bytes"`
}

// RepoFile is one repository file the context pack read, by size.
type RepoFile struct {
	Path string `json:"path"`
	Size int    `json:"size"`
}

// ContextPack is what a review's Job gathered, without diff bodies,
// stage texts or file contents.
type ContextPack struct {
	HeadSHA      string   `json:"headSha"`
	BaseSHA      string   `json:"baseSha"`
	PatchID      string   `json:"patchId"`
	ChangedPaths []string `json:"changedPaths"`
	DeltaPaths   []string `json:"deltaPaths"`
	PriorHeadSHA *string  `json:"priorHeadSha"`
	Stages       []Stage  `json:"stages"`
	RepoNotes    []string `json:"repoNotes"`
	// RuleIDs are the ids of the rules the review was given to check: the
	// ones that apply to the paths it changed and to the pull request.
	RuleIDs   []string   `json:"ruleIds"`
	RepoFiles []RepoFile `json:"repoFiles"`
	CreatedAt time.Time  `json:"createdAt"`
}

// ReviewDetail is everything recorded about one review but its bodies.
type ReviewDetail struct {
	Review      ReviewInfo   `json:"review"`
	Summary     *Summary     `json:"summary"`
	Findings    []Finding    `json:"findings"`
	RunnerRun   *RunnerRun   `json:"runnerRun"`
	AgentRun    *AgentRun    `json:"agentRun"`
	Usage       []UsageRow   `json:"usage"`
	ContextPack *ContextPack `json:"contextPack"`
}

// ReviewDiff is a review's diff and, for an incremental one, the diff
// since the prior review's head. Swept says the retention sweep has
// emptied both.
type ReviewDiff struct {
	Diff      string `json:"diff"`
	DeltaDiff string `json:"deltaDiff"`
	Swept     bool   `json:"swept"`
}

// ContextChunk is one context chunk with its text.
type ContextChunk struct {
	Stage     string `json:"stage"`
	Path      string `json:"path"`
	Language  string `json:"language"`
	Symbol    string `json:"symbol"`
	Kind      string `json:"kind"`
	Scope     string `json:"scope"`
	StartLine int    `json:"startLine"`
	EndLine   int    `json:"endLine"`
	Ref       string `json:"ref"`
	Text      string `json:"text"`
}

// ReviewRaw is the review's inputs and output as stored: the repository
// files and context the runner gathered, the agent's submitted review
// (null for a review that did not submit one), and the runner's log tail.
type ReviewRaw struct {
	RepoFiles map[string]string `json:"repoFiles"`
	Stages    []ContextChunk    `json:"stages"`
	Result    json.RawMessage   `json:"result"`
	LogTail   string            `json:"logTail"`
}
