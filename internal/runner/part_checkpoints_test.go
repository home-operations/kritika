package runner

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/home-operations/kritika/internal/agent"
	"github.com/home-operations/kritika/internal/model"
)

func TestPartInputIdentity(t *testing.T) {
	for _, tt := range []struct {
		name string
		edit func(*Spec, []reviewPart)
		same bool
	}{
		{"run identity", func(s *Spec, _ []reviewPart) { s.RunID = "another" }, true},
		{"available slots", func(s *Spec, _ []reviewPart) { s.Agent.Parallel++ }, true},
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
