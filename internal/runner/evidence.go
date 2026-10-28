package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/home-operations/kritika/internal/agent"
	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/review"
)

// readTools are the tools that read a file of the repository whole or
// in part, by name: read_file, and read_diff over the pull request's
// diff. A grep hit is not a read: it shows the lines that match, not what
// runs around them.
var readTools = []string{"read_file", "read_diff"}

// readLedger records the repository files the agent has read, which a
// finding must be on: a finding on a file the agent never read was made
// from the diff alone, or from nothing, and the review is told to read
// the file or leave the finding out while it still can, and the finding
// is withheld once it has submitted.
type readLedger struct {
	read map[string]bool
}

// newReadLedger is a ledger that already holds the files read in c, the
// conversation the run carries on, nil for none: the model keeps what it
// read there.
func newReadLedger(c *agent.Conversation) *readLedger {
	l := &readLedger{read: map[string]bool{}}
	if c == nil {
		return l
	}
	calls := map[string]model.ToolCall{}
	for _, m := range c.Messages {
		for _, call := range m.ToolCalls {
			if slices.Contains(readTools, call.Name) {
				calls[call.ID] = call
			}
		}
		for _, r := range m.ToolResults {
			if call, ok := calls[r.CallID]; ok && !r.IsError {
				l.add(call.Input)
			}
		}
	}
	return l
}

// add records the file input, a read tool's, names.
func (l *readLedger) add(input json.RawMessage) {
	var req struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(input, &req); err == nil && req.Path != "" {
		l.read[cleanPath(req.Path)] = true
	}
}

// cleanPath is p as the ledger keys it and a finding's path is looked
// up: trimmed and cleaned, so "./a.go" and "a.go" are one file.
func cleanPath(p string) string { return path.Clean(strings.TrimSpace(p)) }

// wrap returns tools with each read tool recording on l the files it
// reads.
func (l *readLedger) wrap(tools []agent.Tool) []agent.Tool {
	out := make([]agent.Tool, len(tools))
	for i, t := range tools {
		if slices.Contains(readTools, t.Def().Name) {
			t = readTool{Tool: t, ledger: l}
		}
		out[i] = t
	}
	return out
}

// readTool is a read tool that records the files it reads.
type readTool struct {
	agent.Tool
	ledger *readLedger
}

func (t readTool) Run(ctx context.Context, input json.RawMessage) (string, error) {
	out, err := t.Tool.Run(ctx, input)
	if err == nil {
		t.ledger.add(input)
	}
	return out, err
}

// unread is the findings of raw, a submission in the contract's shape, on
// files l has no read of, each named "path:line".
func (l *readLedger) unread(raw json.RawMessage) ([]string, error) {
	var res review.Result
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, err
	}
	var out []string
	for _, f := range res.Findings {
		if !l.read[cleanPath(f.Path)] {
			out = append(out, fmt.Sprintf("%s:%d", strings.TrimSpace(f.Path), f.Line))
		}
	}
	return out, nil
}

// check refuses a submission with findings on files the agent has not
// read, naming them, as agent.Run.Evidence: the model reads each file
// around the finding's lines and confirms it, or leaves the finding out.
func (l *readLedger) check(raw json.RawMessage) error {
	unread, err := l.unread(raw)
	if err != nil || len(unread) == 0 {
		// Validate has refused what does not decode.
		return nil
	}
	return fmt.Errorf("review: findings on files you have not read: %s. Read each file with read_file around the finding's "+
		"lines and confirm the finding from what the code does, or leave it out; a finding on a file you never read is withheld",
		strings.Join(unread, ", "))
}

// mark is raw, the submission that ended the run, with each finding on
// a file the agent never read marked as such, for the worker to withhold
// it, and those findings named.
func (l *readLedger) mark(raw json.RawMessage) (json.RawMessage, []string, error) {
	var res review.Result
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, nil, fmt.Errorf("runner: evidence: %w", err)
	}
	var unread []string
	for i := range res.Findings {
		f := &res.Findings[i]
		f.Unread = !l.read[cleanPath(f.Path)]
		if f.Unread {
			unread = append(unread, fmt.Sprintf("%s:%d", strings.TrimSpace(f.Path), f.Line))
		}
	}
	if len(unread) == 0 {
		return raw, nil, nil
	}
	marked, err := json.Marshal(res)
	if err != nil {
		return nil, nil, fmt.Errorf("runner: evidence: %w", err)
	}
	return marked, unread, nil
}
