package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/home-operations/kritika/internal/agent"
	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/review"
	"github.com/home-operations/kritika/internal/runspec"
	"github.com/home-operations/kritika/internal/store"
	"github.com/home-operations/kritika/internal/tuning"
)

func TestPartInputIdentity(t *testing.T) {
	for _, tt := range []struct {
		name string
		edit func(*runspec.Spec, []reviewPart)
		same bool
	}{
		{"run identity", func(s *runspec.Spec, _ []reviewPart) { s.RunID = "another" }, true},
		{"available slots", func(s *runspec.Spec, _ []reviewPart) { s.Agent.Slots = defaultSlots(2) }, true},
		{"token budget", func(s *runspec.Spec, _ []reviewPart) { s.Agent.MaxTokens-- }, true},
		{"continuation", func(s *runspec.Spec, _ []reviewPart) {
			s.Prompt.Continue = &runspec.Continuation{RunID: "r", Session: "s"}
		}, true},
		{"head", func(s *runspec.Spec, _ []reviewPart) { s.Head = "another" }, false},
		{"base", func(s *runspec.Spec, _ []reviewPart) { s.Base = "another" }, false},
		{"model", func(s *runspec.Spec, _ []reviewPart) { s.Model.Granted = "another" }, false},
		{"policy", func(s *runspec.Spec, _ []reviewPart) { s.CheckpointScope = "another" }, false},
		{"prompt", func(_ *runspec.Spec, p []reviewPart) { p[0].prompt.user = "another" }, false},
		{"partition", func(_ *runspec.Spec, p []reviewPart) { p[0].paths = []string{"b.go"} }, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := reviewSpec()
			parts := []reviewPart{{paths: []string{"a.go"}, prompt: loopPrompt(s, false)}}
			before, err := partInputHash(s, parts, agentTools{})
			if err != nil {
				t.Fatal(err)
			}
			tt.edit(&s, parts)
			after, err := partInputHash(s, parts, agentTools{})
			if err != nil || (before == after) != tt.same {
				t.Fatalf("same identity = %v, want %v: %v", before == after, tt.same, err)
			}
		})
	}
}

// TestPartInputIdentityFollowsTuning: the same spec, prompts and tools under
// another search budget are another input, so its checkpoints are not
// reused.
func TestPartInputIdentityFollowsTuning(t *testing.T) {
	s := reviewSpec()
	parts := []reviewPart{{paths: []string{"a.go"}, prompt: loopPrompt(s, false)}}
	before, err := partInputHash(s, parts, agentTools{})
	if err != nil {
		t.Fatal(err)
	}
	tuning.Override(t, func(v *tuning.Values) { v.Runner.SearchCalls++ })
	after, err := partInputHash(s, parts, agentTools{})
	if err != nil || before == after {
		t.Fatalf("identity unchanged by runner.searchCalls: %v", err)
	}
}

// TestPartInputIdentityIgnoresOtherTuning: an override that no part's agent
// reads, the server's idle timeout, leaves the identity, and the parts
// already paid for, as they were.
func TestPartInputIdentityIgnoresOtherTuning(t *testing.T) {
	s := reviewSpec()
	parts := []reviewPart{{paths: []string{"a.go"}, prompt: loopPrompt(s, false)}}
	before, err := partInputHash(s, parts, agentTools{})
	if err != nil {
		t.Fatal(err)
	}
	tuning.Override(t, func(v *tuning.Values) { v.Server.IdleTimeout += time.Second })
	after, err := partInputHash(s, parts, agentTools{})
	if err != nil || before != after {
		t.Fatalf("identity changed by server.idleTimeout: %v", err)
	}
}

// TestPartInputIdentityAtDefaults: at default tuning the identity is the
// hash of the spec, prompts and tools alone, as a build before tuning keyed
// its checkpoints, so those are still reused.
func TestPartInputIdentityAtDefaults(t *testing.T) {
	s := reviewSpec()
	s.RunID, s.Agent.Slots, s.Agent.MaxTokens, s.Prompt.Continue = "", nil, 0, nil
	parts := []reviewPart{{paths: []string{"a.go"}, reviewer: "default", prompt: loopPrompt(s, false)}}
	got, err := partInputHash(s, parts, agentTools{})
	if err != nil {
		t.Fatal(err)
	}
	type prompt struct {
		Paths        []string
		Reviewer     string
		System, User string
		Submit       model.ToolDef
	}
	b, err := json.Marshal(struct {
		Spec    runspec.Spec
		Prompts []prompt
		Tools   []model.ToolDef
	}{Spec: s, Prompts: []prompt{{parts[0].paths, parts[0].reviewer, parts[0].prompt.system, parts[0].prompt.user, parts[0].prompt.submit}}})
	if err != nil {
		t.Fatal(err)
	}
	if sum := sha256.Sum256(b); got != hex.EncodeToString(sum[:]) {
		t.Fatalf("identity %s at default tuning, want the hash of the spec, prompts and tools alone", got)
	}
}

func TestPartsResumeAndCheckpoint(t *testing.T) {
	ctx := t.Context()
	checkpointed := make(chan struct{})
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		part := r.Header.Get(model.PartHeader)
		switch part {
		case "1":
			t.Error("reused part called the model")
		case "3":
			select {
			case <-checkpointed:
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		out, err := model.EncodeChatResponse("step", model.StepResponse{Model: "test", Stop: model.StopToolUse,
			ToolCalls: []model.ToolCall{{ID: "submit", Name: submitReview,
				Input: json.RawMessage(`{"summary":{"take":"t","praise":[]},"findings":[]}`)}},
		})
		if err != nil {
			t.Error(err)
			return
		}
		_, _ = w.Write(out)
	}))
	defer gw.Close()
	s := reviewSpec()
	s.GatewayURL, s.Agent.Slots, s.Agent.TimeoutSeconds = gw.URL, defaultSlots(2), 5
	parts := []reviewPart{{prompt: loopPrompt(s, false)}, {prompt: loopPrompt(s, false)}, {prompt: loopPrompt(s, false)}}
	saved := []partCheckpoint{{SourceRun: "prior", Result: agent.Result{
		Stop: agent.StopSubmitted, Model: "saved-model", Submitted: json.RawMessage(`{"summary":{"take":"saved"},"findings":[]}`),
	}}}
	saves := make(chan int, 2)
	save := func(_ context.Context, part int, res agent.Result) error {
		if res.Stop != agent.StopSubmitted {
			t.Errorf("checkpointed unfinished part: %s", res.Stop)
		}
		saves <- part
		if part == 2 {
			close(checkpointed)
		}
		return nil
	}
	var remaining []int
	report := func(_ context.Context, n, _ map[string]int) map[string]int {
		remaining = append(remaining, n[review.DefaultReviewer])
		return defaultSlots(2)
	}
	results, _, err := runPartLoops(ctx, s, runspec.Secrets{GatewayToken: "t"}, tree(t, map[string]string{"a.go": "package a\n"}), nil, agentTools{}, newPartLocks(), parts,
		slog.New(slog.DiscardHandler), saved, save, report)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) == 0 || remaining[0] != 2 || remaining[len(remaining)-1] != 0 {
		t.Fatalf("reused part counted as unfinished work: %v", remaining)
	}
	if len(saves) != 2 || results[0].Model != "saved-model" || results[0].Steps != 0 || results[0].CostUSD != 0 {
		t.Fatalf("saves=%d reused=%+v", len(saves), results[0])
	}
	for i, res := range results {
		if res.Stop != agent.StopSubmitted {
			t.Fatalf("part %d: %+v", i+1, res)
		}
	}
}

// TestPartsCheckpointFailureKeepsAnswer: a part whose checkpoint cannot be
// written still ends submitted, since the checkpoint only shortens a retry.
func TestPartsCheckpointFailureKeepsAnswer(t *testing.T) {
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		out, err := model.EncodeChatResponse("step", model.StepResponse{Model: "test", Stop: model.StopToolUse,
			ToolCalls: []model.ToolCall{{ID: "submit", Name: submitReview,
				Input: json.RawMessage(`{"summary":{"take":"t","praise":[]},"findings":[]}`)}},
		})
		if err != nil {
			t.Error(err)
			return
		}
		_, _ = w.Write(out)
	}))
	defer gw.Close()
	s := reviewSpec()
	s.GatewayURL, s.Agent.Slots, s.Agent.TimeoutSeconds = gw.URL, defaultSlots(2), 5
	parts := []reviewPart{{prompt: loopPrompt(s, false)}, {prompt: loopPrompt(s, false)}}
	saves := 0
	save := func(context.Context, int, agent.Result) error {
		saves++
		return errors.New("checkpoint refused")
	}
	results, _, err := runPartLoops(t.Context(), s, runspec.Secrets{GatewayToken: "t"}, tree(t, map[string]string{"a.go": "package a\n"}), nil,
		agentTools{}, newPartLocks(), parts, slog.New(slog.DiscardHandler), nil, save, nil)
	if err != nil {
		t.Fatalf("a failed checkpoint failed the run: %v", err)
	}
	if saves != len(parts) {
		t.Fatalf("saves = %d, want %d", saves, len(parts))
	}
	for i, res := range results {
		if res.Stop != agent.StopSubmitted {
			t.Fatalf("part %d: %+v", i+1, res)
		}
	}
}

func TestRestorePart(t *testing.T) {
	s := reviewSpec()
	prompt := loopPrompt(s, false)
	prompt.fallback = review.Lenient(review.Check)
	parts := []reviewPart{{paths: []string{"a.go"}, prompt: prompt}}
	checkpoint := func(res agent.Result) json.RawMessage {
		b, err := json.Marshal(partCheckpoint{Result: res})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	submitted := func(raw json.RawMessage) agent.Result {
		return agent.Result{Stop: agent.StopSubmitted, Submitted: raw, Model: "m"}
	}
	valid := submission("a.go", "t")
	for _, tt := range []struct {
		name   string
		row    store.PartCheckpoint
		reused bool
	}{
		{"submitted", store.PartCheckpoint{Part: 1, SourceRun: "prior", Payload: checkpoint(submitted(valid))}, true},
		{"taken on the fallback", store.PartCheckpoint{Part: 1, SourceRun: "prior",
			Payload: checkpoint(submitted(json.RawMessage(`{"extra":1,` + string(valid[1:]))))}, true},
		{"refused answer", store.PartCheckpoint{Part: 1, SourceRun: "prior",
			Payload: checkpoint(submitted(json.RawMessage(`{"summary":"no","findings":"no"}`)))}, false},
		{"unfinished", store.PartCheckpoint{Part: 1, SourceRun: "prior", Payload: checkpoint(agent.Result{Stop: agent.StopCanceled})}, false},
		{"outside the plan", store.PartCheckpoint{Part: 2, SourceRun: "prior", Payload: checkpoint(submitted(valid))}, false},
		{"undecodable", store.PartCheckpoint{Part: 1, SourceRun: "prior", Payload: json.RawMessage(`{`)}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			part, err := restorePart(tt.row, parts)
			if (err == nil) != tt.reused {
				t.Fatalf("err = %v, want reused %v", err, tt.reused)
			}
			if tt.reused && (part.SourceRun != "prior" || part.Result.Stop != agent.StopSubmitted || part.Result.Model != "m") {
				t.Fatalf("restored %+v", part)
			}
		})
	}
}

// TestPartsCancellationWhileCheckpointing: a part cancelled while it waits
// to checkpoint its answer keeps it, so the run's record and the other
// parts' results survive the cancellation.
func TestPartsCancellationWhileCheckpointing(t *testing.T) {
	holding := make(chan struct{})
	served := make(chan struct{})
	release := make(chan struct{})
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The second part answers once the first holds the lock to checkpoint.
		if r.Header.Get(model.PartHeader) == "2" {
			select {
			case <-holding:
			case <-r.Context().Done():
				return
			}
			defer close(served)
		}
		w.Header().Set("Content-Type", "application/json")
		out, err := model.EncodeChatResponse("step", model.StepResponse{Model: "test", Stop: model.StopToolUse,
			ToolCalls: []model.ToolCall{{ID: "submit", Name: submitReview,
				Input: json.RawMessage(`{"summary":{"take":"t","praise":[]},"findings":[]}`)}},
		})
		if err != nil {
			t.Error(err)
			return
		}
		_, _ = w.Write(out)
	}))
	defer gw.Close()
	s := reviewSpec()
	s.GatewayURL, s.Agent.Slots, s.Agent.TimeoutSeconds = gw.URL, defaultSlots(2), 5
	parts := []reviewPart{{prompt: loopPrompt(s, false)}, {prompt: loopPrompt(s, false)}}
	ctx, cancel := context.WithCancelCause(t.Context())
	defer cancel(nil)
	save := func(_ context.Context, part int, _ agent.Result) error {
		if part == 1 {
			close(holding)
			<-release
		}
		return nil
	}
	go func() {
		<-served
		// The second part is waiting for the lock by now, or about to.
		time.Sleep(100 * time.Millisecond)
		cancel(errors.New("stop"))
		close(release)
	}()
	results, _, err := runPartLoops(ctx, s, runspec.Secrets{GatewayToken: "t"}, tree(t, map[string]string{"a.go": "package a\n"}), nil,
		agentTools{}, newPartLocks(), parts, slog.New(slog.DiscardHandler), nil, save, nil)
	if err != nil {
		t.Fatalf("cancellation lost the run: %v", err)
	}
	for i, res := range results {
		if res.Stop != agent.StopSubmitted {
			t.Fatalf("part %d: %+v", i+1, res)
		}
	}
}

// TestLoadPartsOverTheAnswerLimit: checkpoints whose answer is over
// runner.maxReportAnswer are not reused, and every part runs again, rather
// than the review failing on an answer cut short.
func TestLoadPartsOverTheAnswerLimit(t *testing.T) {
	tuning.Override(t, func(v *tuning.Values) { v.Runner.MaxReportAnswer = 64 })
	rows := []runspec.PartCheckpoint{{Part: 1, SourceRun: "run", Payload: json.RawMessage(`{"pad":"` + strings.Repeat("x", 200) + `"}`)}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(rows)
	}))
	defer srv.Close()
	saved, err := loadParts(t.Context(), &reporter{gatewayURL: srv.URL}, "hash", make([]reviewPart, 2), slog.New(slog.DiscardHandler))
	if err != nil || len(saved) != 2 || saved[0].SourceRun != "" || saved[1].SourceRun != "" {
		t.Fatalf("loadParts = %+v, %v; want no checkpoints and no error", saved, err)
	}
}
