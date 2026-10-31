//go:build bench

package bench

import (
	"time"

	"github.com/home-operations/kritika/internal/review"
)

// caseResult is one case under one mode.
type caseResult struct {
	Steps     int              `json:"steps"`
	ToolCalls map[string]int   `json:"tool_calls,omitempty"`
	ID        string           `json:"id"`
	Mode      string           `json:"mode"`
	Findings  []review.Finding `json:"findings"`
	Dropped   int              `json:"dropped"`
	MustTotal int              `json:"must_total"`
	MustHit   int              `json:"must_hit"`
	NiceTotal int              `json:"nice_total"`
	NiceHit   int              `json:"nice_hit"`
	Matched   int              `json:"matched_findings"`
	// Judged are the pairs that the judge was asked about: an expected
	// range that no finding landed on, against each finding in its file.
	// JudgeHit is how many expected ranges a judge's match alone hit.
	Judged      []verdict      `json:"judged,omitempty"`
	JudgeHit    int            `json:"judge_hit"`
	JudgeCost   float64        `json:"judge_cost_usd"`
	Context     map[string]int `json:"context_chunks"`
	PromptChars int            `json:"prompt_chars"`
	Input       int64          `json:"input_tokens"`
	Cached      int64          `json:"cached_tokens"`
	Output      int64          `json:"output_tokens"`
	CostUSD     float64        `json:"cost_usd"`
	Latency     time.Duration  `json:"latency_ns"`
	Error       string         `json:"error,omitempty"`
}

// Report is what a run writes, and what the floors command reads back.
type Report struct {
	Options benchOptions `json:"options"`
	Model   string       `json:"model"`
	// Judge is the judge that scored the run, as judge.Name says, or "" for
	// lines alone. The floors are pinned to it.
	Judge string    `json:"judge,omitempty"`
	When  time.Time `json:"when"`
	Dry   bool      `json:"dry"`
	// Scripted says the model was the corpus's own script, so the run
	// measured the wiring and nothing of a model.
	Scripted bool                  `json:"scripted,omitempty"`
	Cases    int                   `json:"cases"`
	Modes    map[string]modeTotals `json:"modes"`
	Results  []caseResult          `json:"results"`
}

// report is Report as the bench's own code names it.
type report = Report

type modeTotals struct {
	Cases      int     `json:"cases"`
	Errors     int     `json:"errors"`
	MustRecall float64 `json:"must_recall"`
	NiceRecall float64 `json:"nice_recall"`
	Findings   int     `json:"findings"`
	Matched    int     `json:"matched_findings"`
	MatchRate  float64 `json:"match_rate"`
	// MeanFindings and MeanToolCalls are per case that ran, the collapse
	// floors' measures: a review that reads nothing or reports nothing
	// has broken, whatever its recall by chance.
	MeanFindings  float64 `json:"mean_findings"`
	MeanToolCalls float64 `json:"mean_tool_calls"`
	JudgeHit      int     `json:"judge_hit"`
	CostUSD       float64 `json:"cost_usd"`
	JudgeCost     float64 `json:"judge_cost_usd"`
	MeanLatency   float64 `json:"mean_latency_s"`
	InputTokens   int64   `json:"input_tokens"`
	OutputTokens  int64   `json:"output_tokens"`
}
