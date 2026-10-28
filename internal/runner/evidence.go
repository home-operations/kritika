package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
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
// is withheld once it has submitted. A read counts once the model has
// seen what it returned: the reads of a step are pending until the next
// request carries their results (see settling), so a submission in the
// same answer as a read is not credited with it, whether the loop asks
// the model again or ends on that answer.
type readLedger struct {
	read, pending map[string]bool
	// exempt says every file counts as read: a review of one step, which
	// reads nothing, is not held to the gate, and its submission is still
	// cleared of any unread the model wrote.
	exempt bool
}

// newReadLedger is a ledger that already holds the files read in c, the
// conversation the run carries on, nil for none: the model keeps what it
// read there.
func newReadLedger(c *agent.Conversation) *readLedger {
	l := &readLedger{read: map[string]bool{}, pending: map[string]bool{}}
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
	l.settle()
	return l
}

// add records the file input, a read tool's, names, pending the end of
// the step.
func (l *readLedger) add(input json.RawMessage) {
	var req struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(input, &req); err == nil && req.Path != "" {
		l.pending[cleanPath(req.Path)] = true
	}
}

// exemptLedger is a ledger that counts every file as read.
func exemptLedger() *readLedger {
	return &readLedger{read: map[string]bool{}, pending: map[string]bool{}, exempt: true}
}

// has reports whether the file p names counts as read.
func (l *readLedger) has(p string) bool { return l.exempt || l.read[cleanPath(p)] }

// settle counts the pending reads: a request carrying their results is
// being sent, so the model sees what they returned.
func (l *readLedger) settle() {
	for p := range l.pending {
		l.read[p] = true
	}
	clear(l.pending)
}

// settling is stepper with l's pending reads settled as each request is
// answered: the results of the last step's reads went to the model with
// it, and a request refused before it reached the model, by a budget or
// an outage, delivered nothing.
func (l *readLedger) settling(stepper model.Stepper) model.Stepper {
	return model.StepperFunc(func(ctx context.Context, req model.StepRequest) (model.StepResponse, error) {
		resp, err := stepper.Step(ctx, req)
		if err == nil {
			l.settle()
		}
		return resp, err
	})
}

// cleanPath is p as the ledger keys it and a finding's path is looked
// up: trimmed and cleaned, as read_diff reads its path and Parse a
// finding's, so "./a.go", " a.go" and "a.go" are one file.
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
		if !l.has(f.Path) {
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

// unreadKey is the finding key the worker reads a withheld finding by,
// which the model is not allowed and the ledger alone sets.
const unreadKey = "unread"

// mark is raw, the submission that ended the run, with each finding on a
// file the agent never read marked as such, for the worker to withhold
// it, and those findings named. Only the findings' unread keys change:
// the rest of the submission, the summary with it, is kept as the model
// wrote it, an empty diagram an incremental review answers with
// included, and any unread the model wrote itself is dropped. A finding
// that is not an object is left as it is, for Parse to drop. A
// submission that needs no change is returned as it is.
func (l *readLedger) mark(raw json.RawMessage) (json.RawMessage, []string, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, nil, fmt.Errorf("runner: evidence: %w", err)
	}
	// The findings are the ones encoding/json reads, whatever case their
	// key is in and however often it repeats; they are written back under
	// the one key, the others dropped.
	var chosen struct {
		Findings json.RawMessage `json:"findings"`
	}
	_ = json.Unmarshal(raw, &chosen)
	var findings []json.RawMessage
	if len(chosen.Findings) == 0 || json.Unmarshal(chosen.Findings, &findings) != nil {
		return raw, nil, nil
	}
	var unread []string
	changed := false
	for i, f := range findings {
		marked, where, err := l.markFinding(f)
		if err != nil {
			return nil, nil, err
		}
		if where != "" {
			unread = append(unread, where)
		}
		changed = changed || marked != nil
		if marked != nil {
			findings[i] = marked
		}
	}
	if !changed {
		return raw, nil, nil
	}
	marked, err := json.Marshal(findings)
	if err != nil {
		return nil, nil, fmt.Errorf("runner: evidence: %w", err)
	}
	maps.DeleteFunc(top, func(k string, _ json.RawMessage) bool { return strings.EqualFold(k, keyFindings) })
	top[keyFindings] = marked
	if marked, err = json.Marshal(top); err != nil {
		return nil, nil, fmt.Errorf("runner: evidence: %w", err)
	}
	return marked, unread, nil
}

// markFinding is f, one finding of a submission, with its unread key set
// when its file was never read and dropped otherwise, nil when f needs
// no change or is not an object; where names an unread finding. A
// finding that is re-encoded is written as review.Finding reads it: the
// keys of an object encode sorted, so one repeated in another case would
// otherwise be read the other way round by the worker, and a key the
// contract does not define is dropped as Parse drops it.
func (l *readLedger) markFinding(f json.RawMessage) (marked json.RawMessage, where string, err error) {
	var keys map[string]json.RawMessage
	if json.Unmarshal(f, &keys) != nil || keys == nil {
		return nil, "", nil
	}
	var finding review.Finding
	if err := json.Unmarshal(f, &finding); err != nil {
		return nil, "", nil
	}
	wrote := slices.ContainsFunc(slices.Collect(maps.Keys(keys)), func(k string) bool { return strings.EqualFold(k, unreadKey) })
	finding.Unread = !l.has(finding.Path)
	if finding.Unread {
		where = fmt.Sprintf("%s:%d", strings.TrimSpace(finding.Path), finding.Line)
	} else if !wrote {
		return nil, "", nil
	}
	if marked, err = json.Marshal(finding); err != nil {
		return nil, "", fmt.Errorf("runner: evidence: %w", err)
	}
	return marked, where, nil
}

// keyFindings is the submission's findings key, as the contract names it.
const keyFindings = "findings"
