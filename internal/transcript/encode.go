package transcript

import (
	"encoding/json"
	"strings"

	"github.com/home-operations/kritika/internal/textcut"
	"github.com/home-operations/kritika/internal/tuning"
)

// Placeholder stands in for content a row over its size cap dropped.
const Placeholder = "[omitted: the row exceeded its size cap]"

// SystemTruncated ends a system prompt cut to its size cap.
const SystemTruncated = "\n[truncated: the system prompt exceeded its size cap]"

// Encoded is a row as the model_calls columns take it. System and Tools
// are nil when unchanged. State is the run's state after this row.
type Encoded struct {
	MessagesFrom int
	Messages     json.RawMessage
	Response     json.RawMessage
	System       *string
	Tools        json.RawMessage
	Truncated    bool
	State        State
}

func maskMessage(m Message, fn func(string) string) Message {
	out := Message{Role: m.Role, Text: fn(m.Text), ToolCalls: maskCalls(m.ToolCalls, fn)}
	for _, res := range m.ToolResults {
		res.Content = fn(res.Content)
		out.ToolResults = append(out.ToolResults, res)
	}
	return out
}

func maskTools(tools []Tool, fn func(string) string) []Tool {
	out := make([]Tool, len(tools))
	for i, t := range tools {
		out[i] = Tool{Name: t.Name, Description: fn(t.Description), InputSchema: maskRaw(t.InputSchema, fn)}
	}
	return out
}

func maskCalls(calls []ToolCall, fn func(string) string) []ToolCall {
	out := make([]ToolCall, 0, len(calls))
	for _, c := range calls {
		out = append(out, ToolCall{ID: c.ID, Name: c.Name, Input: maskRaw(c.Input, fn)})
	}
	return out
}

func maskRaw(raw json.RawMessage, fn func(string) string) json.RawMessage {
	if raw == nil {
		return nil
	}
	return validJSON([]byte(fn(string(raw))))
}

// Encode applies the caps and encodes r. It replaces NUL with the Unicode
// replacement character because PostgreSQL text and jsonb cannot store it.
func (r Row) Encode() Encoded {
	t := tuning.Get().Transcript
	e := Encoded{MessagesFrom: r.MessagesFrom, System: r.System, State: r.next}
	msgs := r.Messages
	if r.prevBytes > int64(t.RunCap) {
		msgs, e.System, e.Truncated = nil, nil, true
	} else if r.Tools != nil {
		e.Tools, _ = json.Marshal(*r.Tools) // cannot fail: every RawMessage is valid JSON
	}
	if e.System != nil && strings.ContainsRune(*e.System, 0) {
		system := strings.ReplaceAll(*e.System, "\x00", "\ufffd")
		e.System = &system
	}
	if e.System != nil && len(*e.System) > t.SystemCap() {
		system := textcut.Prefix(*e.System, t.SystemCap()) + SystemTruncated
		e.System, e.Truncated = &system, true
	}
	msgs, cut := cutResults(msgs, int(t.ToolResultCap))
	e.Truncated = e.Truncated || cut
	e.Messages = marshalMessages(msgs)
	if len(e.Messages) > int(t.RowCap) {
		e.Messages, e.Truncated = marshalMessages(placeholders(msgs)), true
		if len(e.Messages) > int(t.RowCap) {
			// As a row past the run's cap does, it keeps no messages.
			e.Messages = marshalMessages(nil)
		}
	}
	e.Response, _ = json.Marshal(r.Response) // cannot fail: every RawMessage is valid JSON
	replaceJSONNUL(e.Messages)
	replaceJSONNUL(e.Response)
	replaceJSONNUL(e.Tools)
	size := len(e.Messages) + len(e.Response) + len(e.Tools)
	if e.System != nil {
		size += len(*e.System)
	}
	e.State.Bytes = r.prevBytes + int64(size)
	return e
}

func marshalMessages(msgs []Message) json.RawMessage {
	if len(msgs) == 0 {
		return json.RawMessage("[]")
	}
	b, _ := json.Marshal(msgs) // cannot fail: every RawMessage is valid JSON
	return b
}

// cutResults cuts every tool result over limit bytes, on a rune boundary,
// and reports whether it cut any.
func cutResults(msgs []Message, limit int) ([]Message, bool) {
	cut := false
	out := make([]Message, len(msgs))
	for i, m := range msgs {
		out[i] = m
		if len(m.ToolResults) == 0 {
			continue
		}
		out[i].ToolResults = make([]ToolResult, len(m.ToolResults))
		for j, res := range m.ToolResults {
			if len(res.Content) > limit {
				kept := textcut.Prefix(res.Content, limit)
				res.TruncatedBytes += len(res.Content) - len(kept)
				res.Content, cut = kept, true
			}
			out[i].ToolResults[j] = res
		}
	}
	return out, cut
}

// placeholders keeps msgs' roles, tool call ids and names, and replaces
// every text, input and content with Placeholder.
func placeholders(msgs []Message) []Message {
	input, _ := json.Marshal(Placeholder) // cannot fail for a string
	out := make([]Message, len(msgs))
	for i, m := range msgs {
		out[i] = Message{Role: m.Role}
		if m.Text != "" {
			out[i].Text = Placeholder
		}
		for _, c := range m.ToolCalls {
			out[i].ToolCalls = append(out[i].ToolCalls, ToolCall{ID: c.ID, Name: c.Name, Input: input})
		}
		for _, res := range m.ToolResults {
			out[i].ToolResults = append(out[i].ToolResults, ToolResult{CallID: res.CallID, Content: Placeholder, IsError: res.IsError,
				TruncatedBytes: res.TruncatedBytes})
		}
	}
	return out
}

// replaceJSONNUL changes only actual JSON NUL escapes. Skipping each escape
// pair preserves literal backslashes, including text spelling \u0000.
func replaceJSONNUL(raw []byte) {
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\\' {
			continue
		}
		if len(raw)-i >= 6 && string(raw[i:i+6]) == `\u0000` {
			copy(raw[i:i+6], `\ufffd`)
		}
		i++
	}
}
