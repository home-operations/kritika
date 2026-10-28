//go:build bench

package bench

import (
	"context"
	"fmt"
	"strconv"

	"github.com/home-operations/kritika/internal/agent"
	"github.com/home-operations/kritika/internal/model"
)

type benchOptions struct {
	Effort       model.Effort `json:"effort,omitempty"`
	Steps        int          `json:"steps"`
	OutputTokens int64        `json:"output_tokens"`
	ToolBytes    int          `json:"tool_bytes"`
	// Judge is the model that judges a finding off an expected range,
	// "" for none, and JudgeEffort how hard it reasons.
	Judge       string       `json:"judge,omitempty"`
	JudgeEffort model.Effort `json:"judge_effort,omitempty"`
}

func readOptions(getenv func(string) string) (benchOptions, error) {
	o := benchOptions{Effort: model.Effort(getenv("KRITIKA_BENCH_EFFORT")), Judge: getenv("KRITIKA_BENCH_JUDGE"),
		JudgeEffort: model.Effort(getenv("KRITIKA_BENCH_JUDGE_EFFORT"))}
	if o.Effort != "" && !o.Effort.Valid() {
		return o, fmt.Errorf("bench: effort must be %s", model.EffortLevels)
	}
	if o.JudgeEffort != "" && !o.JudgeEffort.Valid() {
		return o, fmt.Errorf("bench: judge effort must be %s", model.EffortLevels)
	}
	values := []struct {
		name     string
		fallback int64
	}{
		{"KRITIKA_BENCH_STEPS", 1},
		{"KRITIKA_BENCH_OUTPUT_TOKENS", agent.DefaultLimits.MaxOutputTokensPerStep},
		{"KRITIKA_BENCH_TOOL_BYTES", int64(agent.DefaultLimits.MaxToolOutputBytes)},
	}
	n := make([]int64, len(values))
	for i, v := range values {
		n[i] = v.fallback
		if raw := getenv(v.name); raw != "" {
			parsed, err := strconv.ParseInt(raw, 10, 32)
			if err != nil || parsed <= 0 {
				return o, fmt.Errorf("bench: %s must be a positive 32-bit integer", v.name)
			}
			n[i] = parsed
		}
	}
	o.Steps, o.OutputTokens, o.ToolBytes = int(n[0]), n[1], int(n[2])
	return o, nil
}

func (o benchOptions) limits() agent.Limits {
	return agent.Limits{MaxSteps: o.Steps, MaxOutputTokensPerStep: o.OutputTokens, MaxToolOutputBytes: o.ToolBytes}.WithDefaults()
}

type effortStepper struct {
	model.Stepper
	effort model.Effort
}

func (s effortStepper) Step(ctx context.Context, req model.StepRequest) (model.StepResponse, error) {
	req.Effort = s.effort
	return s.Stepper.Step(ctx, req)
}
