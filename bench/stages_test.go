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

// Stop and Text are the step's when it made no answer to extract, as when
// an output budget cut the call off; Response then carries what it cost.
type stageResult struct {
	ID         string                   `json:"id"`
	Request    model.CompletionRequest  `json:"request"`
	Response   model.CompletionResponse `json:"response"`
	Stop       model.StopReason         `json:"stop,omitempty"`
	Text       string                   `json:"text,omitempty"`
	Latency    time.Duration            `json:"latency_ns"`
	Error      string                   `json:"error,omitempty"`
	CheckError string                   `json:"check_error,omitempty"`
}

// stepCapture keeps the step behind a structured answer, which Complete
// drops when the model answered without the call.
type stepCapture struct {
	model.Stepper
	last model.StepResponse
}

func (s *stepCapture) Step(ctx context.Context, req model.StepRequest) (model.StepResponse, error) {
	resp, err := s.Stepper.Step(ctx, req)
	s.last = resp
	return resp, err
}

func evaluateStage(ctx context.Context, c stageCase, stepper model.Stepper) stageResult {
	r := stageResult{ID: c.id, Request: c.request}
	if stepper == nil {
		return r
	}
	capture := &stepCapture{Stepper: stepper}
	start := time.Now()
	resp, err := model.Structured{Stepper: capture}.Complete(ctx, c.request)
	r.Latency = time.Since(start)
	if err != nil {
		r.Error = err.Error()
		last := capture.last
		r.Response = model.CompletionResponse{Model: last.Model, Upstream: last.Upstream, InputTokens: last.Usage.Prompt(),
			CachedTokens: last.Usage.CacheRead, OutputTokens: last.Usage.Output, CostUSD: last.CostUSD, Unpriced: last.Unpriced}
		r.Stop, r.Text = last.Stop, last.Text
		return r
	}
	r.Response = resp
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
		stepper := model.StepperFunc(func(ctx context.Context, _ model.StepRequest) (model.StepResponse, error) {
			if err := ctx.Err(); err != nil {
				return model.StepResponse{}, err
			}
			return model.StepResponse{}, errors.New("provider unavailable")
		})
		c := stageCase{id: "failed", check: func(string) error { t.Fatal("checked a failed response"); return nil }}
		r := evaluateStage(ctx, c, stepper)
		cancel()
		if r.Error == "" || r.CheckError != "" {
			t.Fatalf("result = %+v", r)
		}
	}
}

func TestStageWithoutAnswerKeepsStep(t *testing.T) {
	stepper := model.StepperFunc(func(context.Context, model.StepRequest) (model.StepResponse, error) {
		return model.StepResponse{Text: "The summary would", Stop: model.StopReason("length"), Model: "answering-model",
			Usage: model.Usage{Input: 1200, Output: 64}, CostUSD: 0.01}, nil
	})
	c := stageCase{id: "cut off", check: func(string) error { t.Fatal("checked a missing answer"); return nil }}
	r := evaluateStage(t.Context(), c, stepper)
	if r.Error == "" || r.CheckError != "" || r.Response.Raw != "" || r.Stop != "length" || r.Text != "The summary would" ||
		r.Response.Model != "answering-model" || r.Response.InputTokens != 1200 || r.Response.OutputTokens != 64 || r.Response.CostUSD != 0.01 {
		t.Fatalf("result = %+v", r)
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
	if os.Getenv("KRITIKA_BENCH_SCRIPT") == "1" {
		t.Skip("the corpus's script answers reviews alone; a stage bench is a live model's")
	}
	dry := os.Getenv("KRITIKA_BENCH_DRY") == "1"
	key := os.Getenv("OPENROUTER_API_KEY")
	if key == "" && !dry {
		t.Skip("OPENROUTER_API_KEY not set; use KRITIKA_BENCH_DRY=1 for prompt construction only")
	}
	modelID := envOr("KRITIKA_BENCH_MODEL", "openai/gpt-6-sol")
	var stepper model.Stepper
	if !dry {
		if stepper, err = model.NewStepper(model.ProviderOpenRouter, "", key, nil, nil, nil); err != nil {
			t.Fatal(err)
		}
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
		r := evaluateStage(ctx, c, stepper)
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
