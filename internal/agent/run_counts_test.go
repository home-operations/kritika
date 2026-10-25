package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestRunToolExecutionCounts(t *testing.T) {
	for _, tt := range []struct {
		name, input string
		setup       func(*RunTool)
		want        int
	}{
		{"success", `{"command":"rg"}`, nil, 1},
		{"nonzero exit", `{"command":"rg","args":["exit","1"]}`, nil, 1},
		{"timeout", `{"command":"rg","args":["sleep"]}`, func(rt *RunTool) { rt.cfg.Timeout = 50 * time.Millisecond }, 1},
		{"unoffered", `{"command":"unknown"}`, nil, 0},
		{"install failure", `{"command":"rg"}`, func(rt *RunTool) {
			rt.cfg.Resolve = func(context.Context, string) (string, []string, error) {
				return "", nil, errors.New("install failed")
			}
		}, 0},
		{"start failure", `{"command":"rg"}`, func(rt *RunTool) { rt.cfg.Commands["rg"] = "/missing-binary" }, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rt, _ := newTestRunTool(t, false)
			if tt.setup != nil {
				tt.setup(rt)
			}
			for range 2 {
				// Both successful and failed calls contribute according to whether a process started.
				_, _ = rt.Run(t.Context(), json.RawMessage(tt.input))
			}
			got := rt.Executions()
			if got == nil || got["rg"] != 2*tt.want || len(got) != tt.want {
				t.Fatalf("counts = %v, want rg=%d", got, 2*tt.want)
			}
			got["rg"] = 999
			if rt.Executions()["rg"] != 2*tt.want {
				t.Fatal("caller changed the run's counts")
			}
		})
	}
	t.Run("canceled before start", func(t *testing.T) {
		rt, _ := newTestRunTool(t, false)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if _, err := rt.Run(ctx, json.RawMessage(`{"command":"rg"}`)); !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v", err)
		}
		if len(rt.Executions()) != 0 {
			t.Fatalf("counts = %v", rt.Executions())
		}
	})
}
