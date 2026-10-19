//go:build bench

package bench

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/home-operations/kritika/internal/model"
)

type stageCase struct {
	id      string
	request model.CompletionRequest
	check   func(string) error
}

type stageResult struct {
	ID         string                   `json:"id"`
	Request    model.CompletionRequest  `json:"request"`
	Response   model.CompletionResponse `json:"response"`
	Latency    time.Duration            `json:"latency_ns"`
	Error      string                   `json:"error,omitempty"`
	CheckError string                   `json:"check_error,omitempty"`
}

func evaluateStage(ctx context.Context, c stageCase, call *model.Structured) stageResult {
	r := stageResult{ID: c.id, Request: c.request}
	if call == nil {
		return r
	}
	start := time.Now()
	var err error
	r.Response, err = call.Complete(ctx, c.request)
	r.Latency = time.Since(start)
	if err != nil {
		r.Error = err.Error()
		return r
	}
	if err := c.check(r.Response.Raw); err != nil {
		r.CheckError = err.Error()
	}
	return r
}

func TestStageFailure(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		ctx, cancel := context.WithCancel(t.Context())
		if canceled {
			cancel()
		}
		call := &model.Structured{Stepper: model.StepperFunc(func(ctx context.Context, _ model.StepRequest) (model.StepResponse, error) {
			if err := ctx.Err(); err != nil {
				return model.StepResponse{}, err
			}
			return model.StepResponse{}, errors.New("provider unavailable")
		})}
		c := stageCase{id: "failed", check: func(string) error { t.Fatal("checked a failed response"); return nil }}
		r := evaluateStage(ctx, c, call)
		cancel()
		if r.Error == "" || r.CheckError != "" {
			t.Fatalf("result = %+v", r)
		}
	}
}

func runStageBench(t *testing.T, stage string, cases []stageCase, outputTokens int64) {
	t.Helper()
	opts, err := readOptions(os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("KRITIKA_BENCH_OUTPUT_TOKENS") == "" {
		opts.OutputTokens = outputTokens
	}
	dry := os.Getenv("KRITIKA_BENCH_DRY") == "1"
	key := os.Getenv("OPENROUTER_API_KEY")
	if key == "" && !dry {
		t.Skip("OPENROUTER_API_KEY not set; use KRITIKA_BENCH_DRY=1 for prompt construction only")
	}
	modelID := envOr("KRITIKA_BENCH_MODEL", "openai/gpt-6-sol")
	var call *model.Structured
	if !dry {
		stepper, err := model.NewStepper(model.ProviderOpenRouter, "", key, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		call = &model.Structured{Stepper: stepper}
	}
	when := time.Now().UTC()
	rep := struct {
		Stage   string        `json:"stage"`
		When    time.Time     `json:"when"`
		Dry     bool          `json:"dry"`
		Results []stageResult `json:"results"`
	}{Stage: stage, When: when, Dry: dry}
	for _, c := range cases {
		c.request.Model, c.request.Effort, c.request.MaxTokens = modelID, opts.Effort, opts.OutputTokens
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
		r := evaluateStage(ctx, c, call)
		cancel()
		t.Logf("%s: elapsed=%s input=%d output=%d error=%q check=%q", c.id, r.Latency,
			r.Response.InputTokens, r.Response.OutputTokens, r.Error, r.CheckError)
		rep.Results = append(rep.Results, r)
	}
	raw, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	dir := envOr("KRITIKA_BENCH_OUT", "results")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, stage+"-"+when.Format("20060102-150405.000000000")+".json")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("report: %s; checks are regression sentinels, outputs need human review", path)
}
