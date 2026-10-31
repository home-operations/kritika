package runner

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/home-operations/kritika/internal/agent"
	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/review"
)

func TestReadLedger(t *testing.T) {
	carried := &agent.Conversation{Messages: []model.Message{
		{Role: model.RoleUser, Text: "review"},
		{Role: model.RoleAssistant, ToolCalls: []model.ToolCall{
			call("1", "read_file", `{"path":"./util.go"}`), call("2", "read_file", `{"path":"gone.go"}`), call("3", "grep", `{"pattern":"x"}`),
		}},
		{Role: model.RoleUser, ToolResults: []model.ToolResult{{CallID: "1", Content: "1\tpackage main"}, {CallID: "2", IsError: true}, {CallID: "3"}}},
	}}
	l := newReadLedger(carried)
	if !l.read["util.go"] || l.read["gone.go"] || len(l.read) != 1 {
		t.Fatalf("seeded reads = %v, want util.go alone, cleaned", l.read)
	}
	head := tree(t, map[string]string{"main.go": "package main\n", "util.go": "package main\n"})
	tools := l.wrap(offeredTools(reviewSpec(), head, nil, []agent.Tool{newReadDiffTool(agentDiff, 1<<10)}))
	run := func(name, input string) error {
		for _, tool := range tools {
			if tool.Def().Name == name {
				_, err := tool.Run(t.Context(), json.RawMessage(input))
				return err
			}
		}
		t.Fatalf("no tool %s", name)
		return nil
	}
	if err := run("read_file", `{"path":"main.go","start_line":1,"end_line":1}`); err != nil {
		t.Fatal(err)
	}
	if err := run("read_file", `{"path":"nope.go"}`); err == nil {
		t.Fatal("a missing file read")
	}
	if err := run("read_diff", `{"path":" main.go "}`); err != nil {
		t.Fatal(err)
	}
	if err := run("grep", `{"pattern":"package","path_glob":"util.go"}`); err != nil {
		t.Fatal(err)
	}
	if l.read["main.go"] || !l.pending["main.go"] {
		t.Fatalf("reads = %v, pending %v; a read counts once its result has been sent", l.read, l.pending)
	}
	answered := func(err error) model.Stepper {
		return model.StepperFunc(func(context.Context, model.StepRequest) (model.StepResponse, error) { return model.StepResponse{}, err })
	}
	if _, err := l.settling(answered(errors.New("refused"))).Step(t.Context(), model.StepRequest{}); err == nil || l.read["main.go"] {
		t.Fatalf("a request refused settled the reads: %v, %v", err, l.read)
	}
	if _, err := l.settling(answered(nil)).Step(t.Context(), model.StepRequest{}); err != nil || !l.read["main.go"] {
		t.Fatalf("a request answered did not settle the reads: %v, %v", err, l.read)
	}
	if !l.read["main.go"] || l.read["nope.go"] || len(l.read) != 2 || len(l.pending) != 0 {
		t.Fatalf("reads = %v, want main.go and util.go: a failed read and a grep hit are not reads", l.read)
	}
}

// TestReadLedgerCheck: a submission with findings on files never read is
// refused naming them, and what does not decode is left to Validate.
func TestReadLedgerCheck(t *testing.T) {
	l := newReadLedger(nil)
	l.add(json.RawMessage(`{"path":"main.go"}`))
	l.add(json.RawMessage(`{"path":"util.go"}`))
	l.settle()
	sub := func(paths ...string) json.RawMessage {
		findings := make([]string, 0, len(paths))
		for i, p := range paths {
			findings = append(findings, `{"path":"`+p+`","line":`+string(rune('1'+i))+`,"severity":"p2","category":"correctness","title":"t","explanation":"e"}`)
		}
		return json.RawMessage(`{"summary":{"take":"ok","praise":[]},"findings":[` + strings.Join(findings, ",") + `]}`)
	}
	if err := l.check(sub("main.go", "./util.go")); err != nil {
		t.Fatalf("findings on read files refused: %v", err)
	}
	err := l.check(sub("main.go", "other.go", "sub/x.go"))
	if err == nil || !strings.Contains(err.Error(), "findings on files you have not read: other.go:2, sub/x.go:3. Read each file with read_file") {
		t.Fatalf("check = %v, want the unread findings named", err)
	}
	if err := l.check(json.RawMessage(`not json`)); err != nil {
		t.Fatalf("what does not decode is Validate's to refuse, got %v", err)
	}
	// Refused once, a submission is refused again until the model sees
	// the refusal, then only once more was read.
	if err := l.check(sub("main.go", "other.go")); err == nil {
		t.Fatal("not refused again before the refusal was delivered")
	}
	answered := model.StepperFunc(func(context.Context, model.StepRequest) (model.StepResponse, error) { return model.StepResponse{}, nil })
	if _, err := l.settling(answered).Step(t.Context(), model.StepRequest{}); err != nil {
		t.Fatal(err)
	}
	if err := l.check(sub("main.go", "other.go")); err != nil {
		t.Fatal("refused again with nothing more read")
	}
	l.add(json.RawMessage(`{"path":"other.go"}`))
	l.settle()
	if err := l.check(sub("main.go", "other.go", "sub/x.go")); err == nil || !strings.Contains(err.Error(), "sub/x.go:3") {
		t.Fatalf("not refused again after more was read: %v", err)
	}
}

// TestReadLedgerMark: marking sets unread on the findings of files never
// read and drops the model's own. It touches nothing else.
func TestReadLedgerMark(t *testing.T) {
	l := newReadLedger(nil)
	l.add(json.RawMessage(`{"path":"main.go"}`))
	l.add(json.RawMessage(`{"path":"util.go"}`))
	l.settle()
	sub := func(paths ...string) json.RawMessage {
		findings := make([]string, 0, len(paths))
		for i, p := range paths {
			findings = append(findings, `{"path":"`+p+`","line":`+string(rune('1'+i))+`,"severity":"p2","category":"correctness","title":"t","explanation":"e"}`)
		}
		return json.RawMessage(`{"summary":{"take":"ok","praise":[]},"findings":[` + strings.Join(findings, ",") + `]}`)
	}
	raw := sub("main.go", "other.go")
	marked, unread, err := l.mark(raw)
	if err != nil || len(unread) != 1 || unread[0] != "other.go:2" {
		t.Fatalf("mark = %s, %v, %v", marked, unread, err)
	}
	var res review.Result
	if err := json.Unmarshal(marked, &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Findings) != 2 || res.Findings[0].Unread || !res.Findings[1].Unread || res.Summary.Take != "ok" {
		t.Fatalf("marked = %s", marked)
	}
	// The model's own unread, in any case, goes. The summary and the keys
	// the contract does not define stay as they were, an empty diagram
	// that takes the last review's back included.
	raw = json.RawMessage(`{"summary":{"take":"ok","praise":[],"diagram":""},"verdict":"x","findings":[` +
		`{"path":"main.go","line":1,"severity":"p2","category":"correctness","title":"t","explanation":"e","Unread":true,"mood":"ok"},` +
		`null,{"path":"other.go","line":2,"severity":"p2","category":"correctness","title":"t","explanation":"e","unread":false}]}`)
	marked, unread, err = l.mark(raw)
	if err != nil || len(unread) != 1 || unread[0] != "other.go:2" {
		t.Fatalf("mark = %s, %v, %v", marked, unread, err)
	}
	for _, want := range []string{`"diagram":""`, `"verdict":"x"`, `null`, `"unread":true`} {
		if !strings.Contains(string(marked), want) {
			t.Errorf("marked lacks %s:\n%s", want, marked)
		}
	}
	if strings.Contains(string(marked), `"Unread"`) || strings.Count(string(marked), "unread") != 1 || strings.Contains(string(marked), "mood") {
		t.Errorf("the model's own unread, or a key the contract does not define, survived:\n%s", marked)
	}
}

// TestReadLedgerMarkCanonical: a finding re-encoded is written as the
// worker reads it, and an exempt ledger clears the model's own unread.
func TestReadLedgerMarkCanonical(t *testing.T) {
	l := newReadLedger(nil)
	l.add(json.RawMessage(`{"path":"main.go"}`))
	l.settle()
	var res review.Result
	// The findings key and a repeated path are read as encoding/json
	// reads them, and written back under one key each.
	raw := json.RawMessage(`{"summary":{"take":"ok","praise":[]},"Findings":[` +
		`{"path":"other.go","Path":"main.go","line":1,"Line":2,"severity":"p2","category":"correctness","title":"t","explanation":"e","unread":true}]}`)
	marked, unread, err := l.mark(raw)
	if err != nil || len(unread) != 0 {
		t.Fatalf("mark = %s, %v, %v", marked, unread, err)
	}
	if err := json.Unmarshal(marked, &res); err != nil || len(res.Findings) != 1 || res.Findings[0].Path != "main.go" ||
		res.Findings[0].Line != 2 || res.Findings[0].Unread || strings.Contains(string(marked), `"Findings"`) ||
		strings.Contains(string(marked), `"Path"`) {
		t.Fatalf("marked = %s, %v; want main.go:2 under findings alone", marked, err)
	}
	// A finding re-encoded is written as the worker reads it: a key
	// repeated in another case resolves as encoding/json resolves it, for
	// every field, and one the contract does not define goes.
	raw = json.RawMessage(`{"summary":{"take":"ok","praise":[]},"findings":[` +
		`{"path":"main.go","line":1,"severity":"p0","Severity":"p2","category":"correctness","title":"t","explanation":"e","mood":"ok","unread":true}]}`)
	if marked, unread, err = l.mark(raw); err != nil || len(unread) != 0 {
		t.Fatalf("mark = %s, %v, %v", marked, unread, err)
	}
	if err := json.Unmarshal(marked, &res); err != nil || len(res.Findings) != 1 || res.Findings[0].Severity != review.SeverityP2 ||
		strings.Contains(string(marked), `"Severity"`) || strings.Contains(string(marked), "mood") {
		t.Fatalf("marked = %s, %v; want p2 under severity alone, without mood", marked, err)
	}
	exempt := exemptLedger()
	never := json.RawMessage(`{"summary":{"take":"ok","praise":[]},"findings":[{"path":"never.go","line":1,"severity":"p2",` +
		`"category":"correctness","title":"t","explanation":"e"}]}`)
	if marked, unread, err := exempt.mark(never); err != nil || unread != nil || string(marked) != string(never) {
		t.Fatalf("an exempt ledger marks nothing: %s, %v, %v", marked, unread, err)
	}
	if marked, unread, err := exempt.mark(raw); err != nil || unread != nil || strings.Contains(string(marked), "unread") {
		t.Fatalf("an exempt ledger still clears the model's own unread: %s, %v, %v", marked, unread, err)
	}
	read := `{"summary":{"take":"ok","praise":[]},"findings":[{"path":"./main.go","line":1,"severity":"p2","category":"correctness",` +
		`"title":"t","explanation":"e"}]}`
	for _, raw := range []string{`{"summary":{"take":"ok","praise":[]}}`, read} {
		if marked, unread, err := l.mark(json.RawMessage(raw)); err != nil || unread != nil || string(marked) != raw {
			t.Fatalf("a submission that needs no change is returned as it is: %s, %v, %v", marked, unread, err)
		}
	}
}

// sameFinding reports whether raw is the one finding of TestReviewAgentEvidence
// with its summary, marked unread or not as unread says.
func sameFinding(t *testing.T, raw json.RawMessage, unread bool) bool {
	t.Helper()
	var res review.Result
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatal(err)
	}
	return res.Summary.Take == "ok" && len(res.Findings) == 1 && res.Findings[0].Path == "main.go" && res.Findings[0].Line == 3 &&
		res.Findings[0].Title == "t" && res.Findings[0].Unread == unread
}

// TestReviewAgentEvidence: a finding on a file that the agent never read is
// refused while the agent can still read. It is taken on a step that the
// agent must submit on, and then marked unread. A review of one step is not
// held to it.
func TestReviewAgentEvidence(t *testing.T) {
	head := tree(t, map[string]string{"main.go": "package main\n\nfunc b() {}\n"})
	finding := `{"summary":{"take":"ok","praise":[]},"findings":[{"path":"main.go","line":3,"severity":"p2","category":"correctness","title":"t","explanation":"e"}]}`
	logger := slog.New(slog.DiscardHandler)
	t.Run("refused, read, submitted", func(t *testing.T) {
		st := &scriptedStepper{steps: []model.StepResponse{
			{ToolCalls: []model.ToolCall{call("1", "submit_review", finding)}},
			{ToolCalls: []model.ToolCall{call("2", "read_file", `{"path":"main.go"}`)}},
			{ToolCalls: []model.ToolCall{call("3", "submit_review", finding)}},
		}}
		s := agentPromptSpec()
		res, _ := agentLoop(t.Context(), st, s, offeredTools(s, head, nil, nil), loopPrompt(s, false), time.Minute, logger)
		if res.Stop != agent.StopSubmitted || res.Steps != 3 || !sameFinding(t, res.Submitted, false) {
			t.Fatalf("result = %+v", res)
		}
		refused := st.reqs[1].Messages[len(st.reqs[1].Messages)-1].ToolResults
		if len(refused) != 1 || !refused[0].IsError || !strings.Contains(refused[0].Content, "files you have not read: main.go:3") {
			t.Fatalf("the first submit's result = %+v, want the unread finding refused", refused)
		}
	})
	t.Run("refused once, then taken and marked when nothing more is read", func(t *testing.T) {
		st := &scriptedStepper{steps: []model.StepResponse{
			{ToolCalls: []model.ToolCall{call("1", "submit_review", finding)}},
			{ToolCalls: []model.ToolCall{call("2", "submit_review", finding)}},
		}}
		s := agentPromptSpec()
		res, _ := agentLoop(t.Context(), st, s, offeredTools(s, head, nil, nil), loopPrompt(s, false), time.Minute, logger)
		if res.Stop != agent.StopSubmitted || res.Steps != 2 || !sameFinding(t, res.Submitted, true) {
			t.Fatalf("result = %+v; want the second submission taken with its finding marked unread", res)
		}
	})
	t.Run("two submissions in one answer are both refused", func(t *testing.T) {
		st := &scriptedStepper{steps: []model.StepResponse{
			{ToolCalls: []model.ToolCall{call("1", "submit_review", finding), call("2", "submit_review", finding)}},
			{ToolCalls: []model.ToolCall{call("3", "read_file", `{"path":"main.go"}`)}},
			{ToolCalls: []model.ToolCall{call("4", "submit_review", finding)}},
		}}
		s := agentPromptSpec()
		res, _ := agentLoop(t.Context(), st, s, offeredTools(s, head, nil, nil), loopPrompt(s, false), time.Minute, logger)
		if res.Stop != agent.StopSubmitted || res.Steps != 3 || !sameFinding(t, res.Submitted, false) {
			t.Fatalf("result = %+v; want the read then the third submission taken", res)
		}
		results := st.reqs[1].Messages[len(st.reqs[1].Messages)-1].ToolResults
		if len(results) != 2 || !results[0].IsError || !results[1].IsError {
			t.Fatalf("the first step's results = %+v, want both submissions refused", results)
		}
	})
	t.Run("a read in the submission's own step does not count", func(t *testing.T) {
		st := &scriptedStepper{steps: []model.StepResponse{
			{ToolCalls: []model.ToolCall{call("1", "read_file", `{"path":"main.go"}`), call("2", "submit_review", finding)}},
			{ToolCalls: []model.ToolCall{call("3", "submit_review", finding)}},
		}}
		s := agentPromptSpec()
		res, _ := agentLoop(t.Context(), st, s, offeredTools(s, head, nil, nil), loopPrompt(s, false), time.Minute, logger)
		if res.Stop != agent.StopSubmitted || res.Steps != 2 || !sameFinding(t, res.Submitted, false) {
			t.Fatalf("result = %+v", res)
		}
		results := st.reqs[1].Messages[len(st.reqs[1].Messages)-1].ToolResults
		if len(results) != 2 || results[0].IsError || !results[1].IsError || !strings.Contains(results[1].Content, "main.go:3") {
			t.Fatalf("the first step's results = %+v, want the read answered and the submission refused", results)
		}
	})
	t.Run("a read in the step a fallback was held on does not count", func(t *testing.T) {
		held := strings.Replace(finding, `"findings"`, `"verdict":"x","findings"`, 1)
		st := &scriptedStepper{steps: []model.StepResponse{
			{ToolCalls: []model.ToolCall{call("1", "read_file", `{"path":"main.go"}`), call("2", "submit_review", held)},
				Usage: model.Usage{Input: 1000}},
		}}
		s := agentPromptSpec()
		s.Agent.MaxTokens = 500
		prompt := loopPrompt(s, false)
		prompt.fallback = review.Lenient(prompt.validate)
		res, _ := agentLoop(t.Context(), st, s, offeredTools(s, head, nil, nil), prompt, time.Minute, logger)
		if res.Stop != agent.StopSubmitted || res.Refused == "" || !sameFinding(t, res.Submitted, true) {
			t.Fatalf("result = %+v; want the held input taken with its finding marked unread", res)
		}
	})
}

// TestReviewAgentEvidenceTaken: a submission that the agent can no longer be
// refused on, forced or held as a fallback, is taken and marked. A review
// of one step is not held to the gate.
func TestReviewAgentEvidenceTaken(t *testing.T) {
	head := tree(t, map[string]string{"main.go": "package main\n\nfunc b() {}\n"})
	finding := `{"summary":{"take":"ok","praise":[]},"findings":[{"path":"main.go","line":3,"severity":"p2","category":"correctness","title":"t","explanation":"e"}]}`
	logger := slog.New(slog.DiscardHandler)
	t.Run("forced, taken and marked", func(t *testing.T) {
		st := &scriptedStepper{steps: []model.StepResponse{
			{ToolCalls: []model.ToolCall{call("1", "submit_review", finding)}},
			{ToolCalls: []model.ToolCall{call("2", "submit_review", finding)}},
		}}
		s := agentPromptSpec()
		s.Agent.MaxSteps = 2
		res, _ := agentLoop(t.Context(), st, s, offeredTools(s, head, nil, nil), loopPrompt(s, false), time.Minute, logger)
		if res.Stop != agent.StopSubmitted || res.Steps != 2 {
			t.Fatalf("result = %+v", res)
		}
		if !sameFinding(t, res.Submitted, true) {
			t.Fatalf("submitted = %s; want the finding marked unread", res.Submitted)
		}
	})
	t.Run("one step reads nothing", func(t *testing.T) {
		st := &scriptedStepper{steps: []model.StepResponse{{ToolCalls: []model.ToolCall{call("1", "submit_review", finding)}}}}
		s := agentPromptSpec()
		s.Agent.MaxSteps = 1
		res, _ := agentLoop(t.Context(), st, s, offeredTools(s, head, nil, nil), loopPrompt(s, false), time.Minute, logger)
		if res.Stop != agent.StopSubmitted || res.Steps != 1 || string(res.Submitted) != finding {
			t.Fatalf("result = %+v", res)
		}
		// A held fallback of one step is cleared of the model's own unread.
		held := strings.Replace(finding, `"explanation":"e"`, `"explanation":"e","unread":true,"verdict":"x"`, 1)
		st = &scriptedStepper{steps: []model.StepResponse{
			{ToolCalls: []model.ToolCall{call("1", "submit_review", held)}},
			{ToolCalls: []model.ToolCall{call("2", "submit_review", held)}},
			{ToolCalls: []model.ToolCall{call("3", "submit_review", held)}},
		}}
		prompt := loopPrompt(s, false)
		prompt.fallback = review.Lenient(prompt.validate)
		res, _ = agentLoop(t.Context(), st, s, offeredTools(s, head, nil, nil), prompt, time.Minute, logger)
		if res.Stop != agent.StopSubmitted || res.Refused == "" || !sameFinding(t, res.Submitted, false) {
			t.Fatalf("result = %+v; want the held input taken with the model's unread cleared", res)
		}
	})
}
