package webapi

import (
	"encoding/json"
	"time"

	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/transcript"
)

// ToolDef is a tool a model was offered.
type ToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// ToolCall is a tool call a model made.
type ToolCall struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// ToolResult is what a tool call returned.
type ToolResult struct {
	CallID         string `json:"callId"`
	Content        string `json:"content"`
	IsError        bool   `json:"isError"`
	TruncatedBytes int    `json:"truncatedBytes"`
}

// Message is one request message.
type Message struct {
	Role        model.Role   `json:"role"`
	Text        string       `json:"text"`
	ToolCalls   []ToolCall   `json:"toolCalls"`
	ToolResults []ToolResult `json:"toolResults"`
}

// Response is what the model answered.
type Response struct {
	Text      string           `json:"text"`
	ToolCalls []ToolCall       `json:"toolCalls"`
	Stop      model.StopReason `json:"stop"`
}

// Turn is one model call: the messages new in its request from index
// MessagesFrom, and the answer. System and Tools are set when the call
// changed them from its part's call before, or, at a part's first call,
// from the conversation's; Reset says Messages is the whole request.
type Turn struct {
	Index int             `json:"index"`
	ID    string          `json:"id"`
	Kind  transcript.Kind `json:"kind"`
	Step  int             `json:"step"`
	// Part is the part of a split review the call stepped for, from 1; 0
	// when it was not split.
	Part         int       `json:"part"`
	Model        string    `json:"model"`
	Upstream     string    `json:"upstream"`
	System       *string   `json:"system"`
	Tools        []ToolDef `json:"tools"`
	Reset        bool      `json:"reset"`
	MessagesFrom int       `json:"messagesFrom"`
	Messages     []Message `json:"messages"`
	Response     Response  `json:"response"`
	Usage        Usage     `json:"usage"`
	CostUSD      float64   `json:"costUsd"`
	DurationMs   int64     `json:"durationMs"`
	Error        string    `json:"error"`
	Truncated    bool      `json:"truncated"`
	CreatedAt    time.Time `json:"createdAt"`
	RunnerRunID  string    `json:"runnerRunId"`
	// CarriedReviewID is the review whose conversation holds the messages
	// before MessagesFrom, nil when they are earlier turns here.
	CarriedReviewID *string `json:"carriedReviewId"`
}

// Transcript is a set of model calls: the system prompt and tools the
// first carried, then one turn per call.
type Transcript struct {
	System string    `json:"system"`
	Tools  []ToolDef `json:"tools"`
	Turns  []Turn    `json:"turns"`
}
