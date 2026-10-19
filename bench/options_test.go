//go:build bench

package bench

import (
	"context"
	"testing"

	"github.com/home-operations/kritika/internal/agent"
	"github.com/home-operations/kritika/internal/model"
)

func TestBenchOptions(t *testing.T) {
	for _, tt := range []struct {
		name string
		env  map[string]string
		bad  bool
	}{
		{name: "defaults"},
		{name: "overrides", env: map[string]string{"KRITIKA_BENCH_EFFORT": "medium", "KRITIKA_BENCH_STEPS": "10", "KRITIKA_BENCH_OUTPUT_TOKENS": "2048", "KRITIKA_BENCH_TOOL_BYTES": "4096"}},
		{name: "bad effort", env: map[string]string{"KRITIKA_BENCH_EFFORT": "fast"}, bad: true},
		{name: "zero", env: map[string]string{"KRITIKA_BENCH_STEPS": "0"}, bad: true},
		{name: "negative", env: map[string]string{"KRITIKA_BENCH_OUTPUT_TOKENS": "-1"}, bad: true},
		{name: "overflow", env: map[string]string{"KRITIKA_BENCH_TOOL_BYTES": "999999999999999999999"}, bad: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			o, err := readOptions(func(key string) string { return tt.env[key] })
			if (err != nil) != tt.bad {
				t.Fatalf("options = %+v, %v", o, err)
			}
			if tt.bad {
				return
			}
			want := agent.Limits{MaxSteps: 1}.WithDefaults()
			if tt.name == "overrides" {
				want.MaxSteps, want.MaxOutputTokensPerStep, want.MaxToolOutputBytes = 10, 2048, 4096
			}
			if o.limits() != want {
				t.Fatalf("limits = %+v, want %+v", o.limits(), want)
			}
		})
	}
}

type recordingStepper struct{ request model.StepRequest }

func (s *recordingStepper) Step(_ context.Context, req model.StepRequest) (model.StepResponse, error) {
	s.request = req
	return model.StepResponse{}, nil
}

func TestBenchEffort(t *testing.T) {
	s := &recordingStepper{}
	w := effortStepper{Stepper: s, effort: model.EffortMedium}
	if _, err := w.Step(t.Context(), model.StepRequest{Model: "chosen-model", MaxTokens: 2048}); err != nil {
		t.Fatal(err)
	}
	if s.request.Effort != model.EffortMedium || s.request.MaxTokens != 2048 || s.request.Model != "chosen-model" {
		t.Fatalf("request = %+v", s.request)
	}
}
