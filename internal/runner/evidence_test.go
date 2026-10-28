package runner

import (
	"encoding/json"
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
	if err := run("read_diff", `{"path":"main.go"}`); err != nil {
		t.Fatal(err)
	}
	if err := run("grep", `{"pattern":"package","path_glob":"util.go"}`); err != nil {
		t.Fatal(err)
	}
	if !l.read["main.go"] || l.read["nope.go"] || len(l.read) != 2 {
		t.Fatalf("reads = %v, want main.go and util.go: a failed read and a grep hit are not reads", l.read)
	}
	sub := func(paths ...string) json.RawMessage {
		findings := make([]string, 0, len(paths))
		for i, p := range paths {
			findings = append(findings, `{"path":"`+p+`","line":`+string(rune('1'+i))+`,"severity":"p2","category":"correctness","title":"t","explanation":"e"}`)
		}
		return json.RawMessage(`{"summary":{"take":"ok","praise":[]},"findings":[` + strings.Join(findings, ",") + `]}`)
	}
	if err := l.check(sub("main.go", " util.go")); err != nil {
		t.Fatalf("findings on read files refused: %v", err)
	}
	err := l.check(sub("main.go", "other.go", "sub/x.go"))
	if err == nil || !strings.Contains(err.Error(), "findings on files you have not read: other.go:2, sub/x.go:3. Read each file with read_file") {
		t.Fatalf("check = %v, want the unread findings named", err)
	}
	if err := l.check(json.RawMessage(`not json`)); err != nil {
		t.Fatalf("what does not decode is Validate's to refuse, got %v", err)
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
	if same, unread, err := l.mark(sub("main.go")); err != nil || len(unread) != 0 || string(same) != string(sub("main.go")) {
		t.Fatalf("a submission with every file read is returned as it is: %s, %v, %v", same, unread, err)
	}
}

// TestReviewAgentEvidence: a finding on a file the agent never read is
// refused while the agent can still read, taken on a step it must submit
// on, and then marked unread; a review of one step is not held to it.
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
		if res.Stop != agent.StopSubmitted || res.Steps != 3 || string(res.Submitted) != finding {
			t.Fatalf("result = %+v", res)
		}
		refused := st.reqs[1].Messages[len(st.reqs[1].Messages)-1].ToolResults
		if len(refused) != 1 || !refused[0].IsError || !strings.Contains(refused[0].Content, "files you have not read: main.go:3") {
			t.Fatalf("the first submit's result = %+v, want the unread finding refused", refused)
		}
	})
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
		var marked review.Result
		if err := json.Unmarshal(res.Submitted, &marked); err != nil || len(marked.Findings) != 1 || !marked.Findings[0].Unread {
			t.Fatalf("submitted = %s, %v; want the finding marked unread", res.Submitted, err)
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
	})
}
