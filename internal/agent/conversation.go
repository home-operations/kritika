package agent

import (
	"encoding/json"
	"fmt"

	"github.com/home-operations/kritika/internal/model"
)

// ContinueTokens is the most a conversation may hold, in tokens, for a
// later Run to carry it on. Every step of that Run reads the whole of it
// again, so past this a fresh Run with a handoff costs less, even with
// the provider's cache warm.
const ContinueTokens = 100_000

// Conversation is a submitted Run's exchange with its model, kept so that
// a later Run can carry it on while the provider still caches it: the
// system prompt and tools it offered, every message as its last step sent
// them, and the model's answer to that step last. A cached prefix must
// match byte for byte, so nothing in it is masked or cut.
type Conversation struct {
	System   string
	Tools    []model.ToolDef
	Messages []model.Message
	// Tokens is its size as the provider counted the last step: its prompt
	// and its answer.
	Tokens int64
}

// conversationJSON is a Conversation as stored. A tool call's input is the
// model's text as it wrote it, which need not be valid JSON.
type conversationJSON struct {
	System   string        `json:"system"`
	Tools    []toolJSON    `json:"tools"`
	Messages []messageJSON `json:"messages"`
	Tokens   int64         `json:"tokens"`
}

type toolJSON struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
}

type messageJSON struct {
	Role          model.Role        `json:"role"`
	Text          string            `json:"text,omitempty"`
	ToolCalls     []callJSON        `json:"toolCalls,omitempty"`
	ToolResults   []resultJSON      `json:"toolResults,omitempty"`
	ChatGPTOutput []json.RawMessage `json:"chatgptOutput,omitempty"`
}

type callJSON struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Input string `json:"input"`
}

type resultJSON struct {
	CallID  string `json:"callId"`
	Content string `json:"content"`
	IsError bool   `json:"isError,omitempty"`
}

// MarshalJSON implements json.Marshaler.
func (c Conversation) MarshalJSON() ([]byte, error) {
	out := conversationJSON{System: c.System, Tools: make([]toolJSON, len(c.Tools)), Messages: make([]messageJSON, len(c.Messages)),
		Tokens: c.Tokens}
	for i, t := range c.Tools {
		out.Tools[i] = toolJSON{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema}
	}
	for i, m := range c.Messages {
		msg := messageJSON{Role: m.Role, Text: m.Text, ChatGPTOutput: m.ChatGPTOutput}
		for _, call := range m.ToolCalls {
			msg.ToolCalls = append(msg.ToolCalls, callJSON{ID: call.ID, Name: call.Name, Input: string(call.Input)})
		}
		for _, r := range m.ToolResults {
			msg.ToolResults = append(msg.ToolResults, resultJSON{CallID: r.CallID, Content: r.Content, IsError: r.IsError})
		}
		out.Messages[i] = msg
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("agent: encode conversation: %w", err)
	}
	return b, nil
}

// UnmarshalJSON implements json.Unmarshaler.
func (c *Conversation) UnmarshalJSON(b []byte) error {
	var in conversationJSON
	if err := json.Unmarshal(b, &in); err != nil {
		return fmt.Errorf("agent: decode conversation: %w", err)
	}
	out := Conversation{System: in.System, Tools: make([]model.ToolDef, len(in.Tools)), Messages: make([]model.Message, len(in.Messages)),
		Tokens: in.Tokens}
	for i, t := range in.Tools {
		out.Tools[i] = model.ToolDef{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema}
	}
	for i, m := range in.Messages {
		msg := model.Message{Role: m.Role, Text: m.Text, ChatGPTOutput: m.ChatGPTOutput}
		for _, call := range m.ToolCalls {
			msg.ToolCalls = append(msg.ToolCalls, model.ToolCall{ID: call.ID, Name: call.Name, Input: json.RawMessage(call.Input)})
		}
		for _, r := range m.ToolResults {
			msg.ToolResults = append(msg.ToolResults, model.ToolResult{CallID: r.CallID, Content: r.Content, IsError: r.IsError})
		}
		out.Messages[i] = msg
	}
	*c = out
	return nil
}
