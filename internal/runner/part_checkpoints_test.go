package runner

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/home-operations/kritika/internal/agent"
	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/review"
	"github.com/home-operations/kritika/internal/store"
)

func TestPartInputIdentity(t *testing.T) {
	for _, tt := range []struct {
		name string
		edit func(*Spec, []reviewPart)
		same bool
	}{
		{"run identity", func(s *Spec, _ []reviewPart) { s.RunID = "another" }, true},
		{"available slots", func(s *Spec, _ []reviewPart) { s.Agent.Parallel++ }, true},
		{"token budget", func(s *Spec, _ []reviewPart) { s.Agent.MaxTokens-- }, true},
		{"continuation", func(s *Spec, _ []reviewPart) { s.Prompt.Continue = &Continuation{RunID: "r", Session: "s"} }, true},
		{"head", func(s *Spec, _ []reviewPart) { s.Head = "another" }, false},
		{"base", func(s *Spec, _ []reviewPart) { s.Base = "another" }, false},
		{"model", func(s *Spec, _ []reviewPart) { s.Model.Granted = "another" }, false},
		{"policy", func(s *Spec, _ []reviewPart) { s.CheckpointScope = "another" }, false},
		{"prompt", func(_ *Spec, p []reviewPart) { p[0].prompt.user = "another" }, false},
		{"partition", func(_ *Spec, p []reviewPart) { p[0].paths = []string{"b.go"} }, false},
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
	s.Model.GatewayURL, s.Agent.Parallel, s.Agent.TimeoutSeconds = gw.URL, 2, 5
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
	results, _, err := runPartLoops(ctx, s, Secrets{GatewayToken: "t"}, tree(t, map[string]string{"a.go": "package a\n"}), nil, agentTools{}, parts,
		slog.New(slog.DiscardHandler), saved, save)
	if err != nil {
		t.Fatal(err)
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
	s.Model.GatewayURL, s.Agent.Parallel, s.Agent.TimeoutSeconds = gw.URL, 2, 5
	parts := []reviewPart{{prompt: loopPrompt(s, false)}, {prompt: loopPrompt(s, false)}}
	saves := 0
	save := func(context.Context, int, agent.Result) error {
		saves++
		return errors.New("checkpoint refused")
	}
	results, _, err := runPartLoops(t.Context(), s, Secrets{GatewayToken: "t"}, tree(t, map[string]string{"a.go": "package a\n"}), nil,
		agentTools{}, parts, slog.New(slog.DiscardHandler), nil, save)
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
	s.Model.GatewayURL, s.Agent.Parallel, s.Agent.TimeoutSeconds = gw.URL, 2, 5
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
	results, _, err := runPartLoops(ctx, s, Secrets{GatewayToken: "t"}, tree(t, map[string]string{"a.go": "package a\n"}), nil,
		agentTools{}, parts, slog.New(slog.DiscardHandler), nil, save)
	if err != nil {
		t.Fatalf("cancellation lost the run: %v", err)
	}
	for i, res := range results {
		if res.Stop != agent.StopSubmitted {
			t.Fatalf("part %d: %+v", i+1, res)
		}
	}
}
