package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/home-operations/kritika/internal/agent"
	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/contextpack"
	"github.com/home-operations/kritika/internal/gitfetch"
	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/repoconfig"
	"github.com/home-operations/kritika/internal/review"
)

// sizedDiff is a unified diff adding a file of each path, in the order
// given, its section about n bytes long.
func sizedDiff(files ...any) string {
	var b strings.Builder
	for i := 0; i < len(files); i += 2 {
		p, n := files[i].(string), files[i+1].(int)
		fmt.Fprintf(&b, "diff --git a/%s b/%s\nnew file mode 100644\n--- /dev/null\n+++ b/%s\n@@ -0,0 +1,%d @@\n", p, p, p, n/40)
		for range n / 40 {
			b.WriteString("+" + strings.Repeat("x", 38) + "\n")
		}
	}
	return b.String()
}

func TestSplitReview(t *testing.T) {
	s := reviewSpec()
	s.Agent.Parts = 4
	res := &gitfetch.Result{Diff: sizedDiff("a/x.go", 50<<10, "b/y.go", 50<<10), DeltaDiff: sizedDiff("a/x.go", 1<<10)}
	if got := splitReview(s, res, review.ScopeFull); len(got) != 2 {
		t.Fatalf("a full review of 100 KiB = %q, want two parts", got)
	}
	if got := splitReview(s, res, review.ScopeIncremental); got != nil {
		t.Fatalf("an incremental review of a small delta = %q, want it whole", got)
	}
	s.Agent.Parts = 0
	if got := splitReview(s, res, review.ScopeFull); got != nil {
		t.Fatalf("a run its grant sized for one part = %q, want it whole", got)
	}
}

func TestPartSpec(t *testing.T) {
	s := reviewSpec()
	s.Prompt.Prior = []review.Finding{{Path: "a/x.go", Title: "mine"}, {Path: "b/y.go", Title: "theirs"}}
	s.Prompt.Dismissed = []review.DismissedFinding{{Path: "b/y.go"}, {Path: "a/x.go"}}
	s.Prompt.PriorDiagram = "flowchart LR\n  a --> b"
	got := partSpec(s, []string{"a/x.go"})
	if len(got.Prompt.Prior) != 1 || got.Prompt.Prior[0].Title != "mine" || len(got.Prompt.Dismissed) != 1 ||
		got.Prompt.Dismissed[0].Path != "a/x.go" || got.Prompt.PriorDiagram != "" {
		t.Fatalf("part spec prompt = %+v", got.Prompt)
	}
	if len(s.Prompt.Prior) != 2 || len(s.Prompt.Dismissed) != 2 || s.Prompt.PriorDiagram == "" {
		t.Fatalf("partSpec changed the review's own spec: %+v", s.Prompt)
	}
}

// submission is a review a part submits: one finding on path.
func submission(path, take string) json.RawMessage {
	return json.RawMessage(`{"summary":{"headline":"H","take":"` + take + `","praise":["p"]},"findings":[{"path":"` + path +
		`","line":1,"severity":"p2","category":"correctness","title":"t","explanation":"e"}]}`)
}

func TestMergeParts(t *testing.T) {
	parts := []reviewPart{{paths: []string{"a/x.go"}}, {paths: []string{"b/y.go"}}, {paths: []string{"c/z.go"}}}
	results := []agent.Result{
		{Stop: agent.StopSubmitted, Submitted: submission("a/x.go", "One."), Steps: 3, Usage: model.Usage{Input: 10, Output: 1},
			CostUSD: 0.1, Model: "m1", ToolCalls: map[string]int{"grep": 1}},
		{Stop: agent.StopNoSubmit, Err: "no valid submit_review", Steps: 2, Usage: model.Usage{Input: 5}, Model: "m2"},
		{Stop: agent.StopSubmitted, Submitted: submission("c/z.go", "Three."), Steps: 1, Model: "m3", ToolCalls: map[string]int{"grep": 2}},
	}
	merged, records, err := mergeParts(parts, results, Secrets{})
	if err != nil {
		t.Fatal(err)
	}
	if merged.Stop != agent.StopSubmitted || merged.Steps != 6 || merged.Usage.Input != 15 || merged.ToolCalls["grep"] != 3 ||
		merged.Model != "m3" || !strings.Contains(merged.Err, "part 2 of 3: no valid submit_review") {
		t.Fatalf("merged = %+v", merged)
	}
	var res review.Result
	if err := json.Unmarshal(merged.Submitted, &res); err != nil || len(res.Findings) != 2 || res.Summary.Take != "One.\n\nThree." {
		t.Fatalf("merged review = %+v, %v", res, err)
	}
	if len(records) != 3 || records[1].Stop != string(agent.StopNoSubmit) || records[1].Summary != nil ||
		!strings.Contains(string(records[0].Summary), `"take":"One."`) || !slices.Equal(records[2].Paths, []string{"c/z.go"}) {
		t.Fatalf("records = %+v", records)
	}
	failed := []agent.Result{{Stop: agent.StopNoSubmit}, {Stop: agent.StopCanceled}, {Stop: agent.StopCanceled}}
	if merged, _, err := mergeParts(parts, failed, Secrets{}); err != nil || merged.Stop != agent.StopNoSubmit || merged.Submitted != nil {
		t.Fatalf("no part submitted = %+v, %v; want the first part's stop", merged, err)
	}
}

// TestPartPrompts: each part is shown its own files' diff, told which part
// it is, given the similar code of its own hunks and offered search_code,
// given the rules of its own paths, and refused a finding on the other
// part's file; the first part checks the earlier finding on a file no part
// reviews. A split with no rules reports an empty list, not a nil one,
// which the pack's rule_ids column would refuse.
func TestPartPrompts(t *testing.T) {
	asked := 0
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked++
		_, _ = w.Write([]byte(`{"indexed":true,"chunks":[{"stage":"similar","path":"lib/z.go","start_line":1,"end_line":1,"text":"func z() {}"}]}`))
	}))
	t.Cleanup(gw.Close)
	s := reviewSpec()
	s.Model.GatewayURL, s.PriorHead = gw.URL, shaB
	s.Prompt.Rules = []configfile.Rule{{ID: "a-rule", Rule: "Mind a.", Paths: []string{"a/**"}}, {ID: "b-rule", Rule: "Mind b.", Paths: []string{"b/**"}}}
	head := tree(t, map[string]string{"a/x.go": "package a\n", "b/y.go": "package b\n", "lib/z.go": "package lib\n"})
	diff := sizedDiff("a/x.go", 50<<10, "b/y.go", 50<<10)
	res := &gitfetch.Result{Diff: diff, Changed: []string{"a/x.go", "b/y.go"}}
	split := [][]string{{"a/x.go"}, {"b/y.go"}}
	var tools agentTools
	parts, chunks, rules, err := partPrompts(t.Context(), s, Secrets{GatewayToken: "t"}, head, head, res, repoconfig.Files{}, nil, split,
		review.ScopeFull, &tools, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 2 || asked != 2 || tools.search == nil || len(chunks) != 2 || !slices.Equal(rules, []string{"a-rule", "b-rule"}) {
		t.Fatalf("%d parts, %d similar requests, search %v, %d chunks, rules %q; want each part's own", len(parts), asked,
			tools.search != nil, len(chunks), rules)
	}
	const earlier = "main.go:1 [p2] earlier finding"
	if !strings.Contains(parts[0].prompt.user, earlier) || strings.Contains(parts[1].prompt.user, earlier) ||
		!strings.Contains(parts[0].prompt.system, "a-rule") || strings.Contains(parts[0].prompt.system, "b-rule") {
		t.Fatalf("part 1's prompt:\n%s\n%s", parts[0].prompt.system, parts[0].prompt.user)
	}
	for i, part := range parts {
		mine, theirs := split[i][0], split[1-i][0]
		if !strings.Contains(part.prompt.user, fmt.Sprintf("this is part %d", i+1)) || !strings.Contains(part.prompt.user, "diff --git a/"+mine) ||
			strings.Contains(part.prompt.user, "diff --git a/"+theirs) || !strings.Contains(part.prompt.system, "search_code") {
			t.Fatalf("part %d's prompt:\n%s", i+1, part.prompt.user)
		}
		if err := part.prompt.validate(submission(theirs, "t")); err == nil {
			t.Fatalf("part %d took a finding on %s", i+1, theirs)
		}
		if err := part.prompt.validate(submission(mine, "t")); err != nil {
			t.Fatalf("part %d refused a finding on its own %s: %v", i+1, mine, err)
		}
	}
	s.Prompt.Rules = nil
	if _, _, rules, err = partPrompts(t.Context(), s, Secrets{GatewayToken: "t"}, head, head, res, repoconfig.Files{}, nil, split,
		review.ScopeFull, &tools, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatal(err)
	}
	if rules == nil || len(rules) != 0 {
		t.Fatalf("rules of a split with none = %#v, want an empty list", rules)
	}
}

// TestPartPromptsContext: the context a split review returns for its pack
// is what its parts' prompts hold, not the similar code that a part's
// budget leaves out, which the summary's note counts instead.
func TestPartPromptsContext(t *testing.T) {
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req contextpack.SimilarRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		text := "func z() {}"
		if len(req.Queries) > 0 && strings.HasPrefix(req.Queries[0], "b/y.go") {
			text = strings.Repeat("x", 60_000)
		}
		_ = json.NewEncoder(w).Encode(contextpack.SimilarResponse{Indexed: true, Chunks: []contextpack.Chunk{
			{Stage: contextpack.StageSimilar, Path: "lib/z.go", StartLine: 1, EndLine: 1, Text: text},
		}})
	}))
	t.Cleanup(gw.Close)
	s := reviewSpec()
	s.Model.GatewayURL, s.Agent.Parallel = gw.URL, 2
	head := tree(t, map[string]string{"a/x.go": "package a\n", "b/y.go": "package b\n", "lib/z.go": "package lib\n"})
	res := &gitfetch.Result{Diff: sizedDiff("a/x.go", 50<<10, "b/y.go", 50<<10), Changed: []string{"a/x.go", "b/y.go"}}
	var tools agentTools
	parts, chunks, _, err := partPrompts(t.Context(), s, Secrets{GatewayToken: "t"}, head, head, res, repoconfig.Files{}, nil,
		[][]string{{"a/x.go"}, {"b/y.go"}}, review.ScopeFull, &tools, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 1 || chunks[0].Text != "func z() {}" || !slices.Equal(chunks, parts[0].prompt.context) || len(parts[1].prompt.context) != 0 {
		t.Fatalf("context = %d chunk(s), part 1's %d, part 2's %d; want part 1's similar code alone", len(chunks),
			len(parts[0].prompt.context), len(parts[1].prompt.context))
	}
	if strings.Contains(parts[1].prompt.user, "### similar: lib/z.go") {
		t.Fatalf("part 2's prompt holds the similar code its budget left out:\n%.2000s", parts[1].prompt.user)
	}
	if notes := splitNotes(parts); !slices.Contains(notes, "1 context chunk(s) left out of the prompt to fit its budget") {
		t.Fatalf("notes = %q", notes)
	}
}

// TestRunPartLoops: the parts run as many at once as Parallel allows, each
// under its own part on the gateway and reading the head through the tools
// they share, and the timeline lists their steps in part order whichever
// part ends first.
func TestRunPartLoops(t *testing.T) {
	answer := func(name, input string) string {
		return `{"id":"x","object":"chat.completion","created":1,"model":"acme/large","choices":[{"index":0,"message":` +
			`{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"` + name +
			`","arguments":` + strconv.Quote(input) + `}}]},"finish_reason":"tool_calls"}],` +
			`"usage":{"prompt_tokens":120,"completion_tokens":30,"total_tokens":150}}`
	}
	for _, tt := range []struct {
		name            string
		parts, parallel int
	}{
		{name: "one after another", parts: 2, parallel: 1},
		{name: "one round", parts: 2, parallel: 2},
		{name: "two at once", parts: 3, parallel: 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var mu sync.Mutex
			var started, running, most int
			asked := map[string]int{}
			together := make(chan struct{})
			gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				part := r.Header.Get(model.PartHeader)
				mu.Lock()
				asked[part]++
				step := asked[part]
				if step == 1 {
					if started++; started == tt.parallel {
						close(together)
					}
				}
				hold := step == 1 && started <= tt.parallel
				running++
				most = max(most, running)
				mu.Unlock()
				if hold {
					// The first parts to start answer once all of them have
					// asked, which only parts running at once can.
					select {
					case <-together:
					case <-time.After(5 * time.Second):
					}
				}
				out := answer("read_file", `{"path":"a.go"}`)
				if step > 1 {
					if !strings.Contains(string(body), "package a") {
						t.Errorf("part %s was not shown a.go", part)
					}
					if part == "1" {
						time.Sleep(50 * time.Millisecond)
					}
					out = answer(submitReview, `{"summary":{"take":"t","praise":[]},"findings":[]}`)
				}
				mu.Lock()
				running--
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(out))
			}))
			t.Cleanup(gw.Close)
			s := reviewSpec()
			s.Model.GatewayURL, s.Agent.Parallel, s.Agent.TimeoutSeconds = gw.URL, tt.parallel, 60
			head := tree(t, map[string]string{"a.go": "package a\n"})
			parts := make([]reviewPart, tt.parts)
			for i := range parts {
				parts[i] = reviewPart{paths: []string{fmt.Sprintf("p%d.go", i+1)}, prompt: loopPrompt(s, false)}
			}
			results, timeline, err := runPartLoops(t.Context(), s, Secrets{GatewayToken: "t"}, head, nil, agentTools{}, newPartLocks(), parts,
				slog.New(slog.DiscardHandler), nil, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			defer mu.Unlock()
			if most != tt.parallel {
				t.Fatalf("%d parts ran at once, want %d", most, tt.parallel)
			}
			for i, res := range results {
				if res.Stop != agent.StopSubmitted {
					t.Fatalf("part %d stopped %s: %s", i+1, res.Stop, res.Err)
				}
			}
			if len(timeline) != 2*tt.parts {
				t.Fatalf("timeline = %+v, want two steps a part", timeline)
			}
			for k, step := range timeline {
				if step.Part != k/2+1 || step.Index != k {
					t.Fatalf("timeline = %+v, want each part's steps in part order", timeline)
				}
			}
		})
	}
}

// countedTool counts its runs, under name.
type countedTool struct {
	name string
	runs *int
}

func (t countedTool) Def() model.ToolDef { return model.ToolDef{Name: t.name} }

func (t countedTool) Run(context.Context, json.RawMessage) (string, error) {
	*t.runs++
	return "ok", nil
}

// TestLockedTool: a tool waits for the lock the parts share, gives up when
// its part ends first, and runs nothing once it has.
func TestLockedTool(t *testing.T) {
	var runs int
	locks := newPartLocks()
	tool := lockTools([]agent.Tool{countedTool{name: "counted", runs: &runs}}, locks)[0]
	if tool.Def().Name != "counted" {
		t.Fatalf("Def = %+v, want the tool's own", tool.Def())
	}
	locks.head <- struct{}{}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if _, err := tool.Run(ctx, nil); !errors.Is(err, context.DeadlineExceeded) || runs != 0 {
		t.Fatalf("waiting past its part's end: err = %v, %d runs", err, runs)
	}
	<-locks.head
	if out, err := tool.Run(t.Context(), nil); err != nil || out != "ok" || runs != 1 || len(locks.head) != 0 {
		t.Fatalf("Run = %q, %v, %d runs, lock held %d", out, err, runs, len(locks.head))
	}
}

// TestLockToolsByResource: each tool waits for the lock of what it touches
// and no other, so a part's command holds up another part's read of the
// head no more; read_file over fetched files takes head or writes by the
// path, and read_diff waits for nothing.
func TestLockToolsByResource(t *testing.T) {
	locks := newPartLocks()
	held := map[string]chan struct{}{"head": locks.head, "writes": locks.writes, "search": locks.search, "oci": locks.oci}
	trueBin, err := exec.LookPath("true")
	if err != nil {
		t.Skip(err)
	}
	fetched := t.TempDir()
	if err := os.MkdirAll(filepath.Join(fetched, "1-x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fetched, "1-x", "README.md"), []byte("# x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	headTree := agent.NewTree(tree(t, map[string]string{"main.go": "package main\n"}), nil)
	run := agent.NewRunTool(agent.RunConfig{Dir: t.TempDir(), Commands: map[string]string{"rg": trueBin}, Timeout: 5 * time.Second,
		MaxOutputBytes: 4096})
	tests := []struct {
		name  string
		tool  agent.Tool
		input string
		waits string
	}{
		{name: "read_file", tool: agent.ReadFileTool(headTree, 1024), input: `{"path":"main.go"}`, waits: "head"},
		{name: "grep", tool: agent.GrepTool(headTree, 1024), input: `{"pattern":"main"}`, waits: "head"},
		{name: "list_files", tool: agent.ListFilesTool(headTree, 1024), waits: "head"},
		{name: "load_skill", tool: &skillTool{maxBytes: 1024}, input: `{"name":"x"}`, waits: "head"},
		{name: "run", tool: run, input: `{"command":"rg"}`, waits: "writes"},
		{name: "fetch_repo", tool: &fetchRepoTool{dir: fetched}, input: `{}`, waits: "writes"},
		{name: "fetch_oci", tool: &fetchOCITool{}, input: `{}`, waits: "oci"},
		{name: "search_code", tool: &searchTool{}, input: `{}`, waits: "search"},
		{name: "read_diff", tool: newReadDiffTool("", 1024), input: `{"path":"main.go"}`},
		{name: "fetched read_file at the head", tool: fetchedReadFile{Tool: agent.ReadFileTool(headTree, 1024), dir: fetched, maxBytes: 1024},
			input: `{"path":"main.go"}`, waits: "head"},
		{name: "fetched read_file of a fetched file", tool: fetchedReadFile{Tool: agent.ReadFileTool(headTree, 1024), dir: fetched, maxBytes: 1024},
			input: `{"path":"../upstream/1-x/README.md"}`, waits: "writes"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tool := lockTools([]agent.Tool{tt.tool}, locks)[0]
			// Whatever the tool makes of its input, it runs while any other
			// lock is held; the deadline fails a tool that waits for one
			// instead of stalling the test.
			for name, lock := range held {
				if name == tt.waits {
					continue
				}
				lock <- struct{}{}
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				_, err := tool.Run(ctx, json.RawMessage(tt.input))
				cancel()
				<-lock
				if errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("holding %s: %v", name, err)
				}
			}
			if tt.waits == "" {
				return
			}
			held[tt.waits] <- struct{}{}
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
			defer cancel()
			if _, err := tool.Run(ctx, json.RawMessage(tt.input)); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("holding %s: err = %v", tt.waits, err)
			}
			<-held[tt.waits]
		})
	}
}

// heldTool is a tool that runs until released, under head, as a slow read
// of the head would.
type heldTool struct{ entered, release chan struct{} }

func (heldTool) Def() model.ToolDef { return model.ToolDef{Name: "held"} }

func (t heldTool) Run(ctx context.Context, _ json.RawMessage) (string, error) {
	close(t.entered)
	select {
	case <-t.release:
		return "ok", nil
	case <-ctx.Done():
		return "", context.Cause(ctx)
	}
}

// TestPartLocksExclusion: a tool holds its lock for as long as it runs,
// so another part's tool of the same lock waits for it to end, while one
// of another lock runs meanwhile.
func TestPartLocksExclusion(t *testing.T) {
	trueBin, err := exec.LookPath("true")
	if err != nil {
		t.Skip(err)
	}
	locks := newPartLocks()
	slow := heldTool{entered: make(chan struct{}), release: make(chan struct{})}
	tools := lockTools([]agent.Tool{
		slow, agent.ListFilesTool(agent.NewTree(tree(t, map[string]string{"main.go": "package main\n"}), nil), 1024),
		agent.NewRunTool(agent.RunConfig{Dir: t.TempDir(), Commands: map[string]string{"rg": trueBin}, Timeout: 5 * time.Second, MaxOutputBytes: 4096}),
	}, locks)
	held, list, run := tools[0], tools[1], tools[2]
	done := make(chan error, 1)
	go func() {
		_, err := held.Run(t.Context(), nil)
		done <- err
	}()
	<-slow.entered
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if _, err := list.Run(ctx, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a read of the head ran beside another part's: %v", err)
	}
	rctx, rcancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer rcancel()
	if out, err := run.Run(rctx, json.RawMessage(`{"command":"rg"}`)); err != nil || !strings.HasPrefix(out, "exit code 0\n") {
		t.Fatalf("a command waited for another part's read of the head: %q, %v", out, err)
	}
	close(slow.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if out, err := list.Run(t.Context(), nil); err != nil || !strings.Contains(out, "main.go") {
		t.Fatalf("after the read ended: %q, %v", out, err)
	}
}

// TestPartLocksCheckpoint: a checkpoint holds writes and oci, released
// however it ends, and is not written once its part ended.
func TestPartLocksCheckpoint(t *testing.T) {
	locks := newPartLocks()
	failure := errors.New("not kept")
	err := locks.checkpoint(t.Context(), func() error {
		if len(locks.writes) != 1 || len(locks.oci) != 1 || len(locks.head) != 0 || len(locks.search) != 0 {
			t.Fatal("checkpoint runs without writes and oci, or with more")
		}
		return failure
	})
	if !errors.Is(err, failure) || len(locks.writes) != 0 || len(locks.oci) != 0 {
		t.Fatalf("err = %v, writes held %d, oci held %d", err, len(locks.writes), len(locks.oci))
	}
	locks.oci <- struct{}{}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	err = locks.checkpoint(ctx, func() error { t.Fatal("written past its part's end"); return nil })
	if !errors.Is(err, context.DeadlineExceeded) || len(locks.writes) != 0 {
		t.Fatalf("err = %v, writes held %d", err, len(locks.writes))
	}
}
