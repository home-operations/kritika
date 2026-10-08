package worker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/home-operations/kritika/internal/agent"
	"github.com/home-operations/kritika/internal/executor"
	"github.com/home-operations/kritika/internal/forge"
	"github.com/home-operations/kritika/internal/jobtimeout"
	"github.com/home-operations/kritika/internal/store"
)

func TestAgentRunStopError(t *testing.T) {
	tests := []struct {
		name string
		run  store.AgentRunRow
		want string
	}{
		{name: "submitted", run: store.AgentRunRow{StopReason: string(agent.StopSubmitted), Result: []byte(`{}`)}},
		{name: "submitted without a result", run: store.AgentRunRow{StopReason: string(agent.StopSubmitted)}, want: "agent stopped: submitted without a result"},
		{name: "max steps", run: store.AgentRunRow{StopReason: string(agent.StopMaxSteps)}, want: "agent stopped: max_steps"},
		{name: "budget", run: store.AgentRunRow{StopReason: string(agent.StopBudget)}, want: "agent stopped: budget"},
		{name: "no submit", run: store.AgentRunRow{StopReason: string(agent.StopNoSubmit)}, want: "agent stopped: no_submit"},
		{name: "model error", run: store.AgentRunRow{StopReason: string(agent.StopError), Error: "model: 500"}, want: "agent stopped: error: model: 500"},
		{name: "timed out", run: store.AgentRunRow{StopReason: string(agent.StopCanceled), Error: "agent timeout (20m0s) reached"},
			want: "agent stopped: canceled: agent timeout (20m0s) reached"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := stopError(tt.run)
			if got := errText(err); got != tt.want {
				t.Fatalf("stopError = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAgentDeadline(t *testing.T) {
	tests := []struct {
		runner, timeout, want time.Duration
	}{
		{runner: 10 * time.Minute, timeout: 20 * time.Minute, want: 25 * time.Minute},
		{runner: time.Hour, timeout: 20 * time.Minute, want: time.Hour},
		{runner: 25 * time.Minute, timeout: 20 * time.Minute, want: 25 * time.Minute},
	}
	for _, tt := range tests {
		if got := agentDeadline(tt.runner, tt.timeout); got != tt.want {
			t.Fatalf("agentDeadline(%s, %s) = %s, want %s", tt.runner, tt.timeout, got, tt.want)
		}
	}
}

func TestStopped(t *testing.T) {
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	failed := executor.Result{Err: errors.New("job failed")}
	tests := []struct {
		name  string
		ctx   context.Context
		res   executor.Result
		cause error
		want  bool
	}{
		{name: "succeeded", ctx: t.Context(), res: executor.Result{}},
		{name: "failed on its own", ctx: t.Context(), res: failed},
		{name: "superseded", ctx: t.Context(), res: failed, cause: errSuperseded, want: true},
		{name: "job context ended", ctx: canceled, res: failed, want: true},
		{name: "deadline", ctx: t.Context(), res: executor.Result{Err: failed.Err, DeadlineExceeded: true}, want: true},
		{name: "finished despite a cancel", ctx: canceled, res: executor.Result{}, cause: errSuperseded},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stopped(tt.ctx, tt.res, tt.cause); got != tt.want {
				t.Fatalf("stopped = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAgentBudget(t *testing.T) {
	tests := []struct {
		name                     string
		agentMax, perMonth, used int64
		want                     int64
		capped                   bool
	}{
		{name: "no monthly cap", agentMax: 4_000_000, used: 9_000_000, want: 4_000_000},
		{name: "plenty left", agentMax: 4_000_000, perMonth: 10_000_000, used: 1_000_000, want: 4_000_000},
		{name: "cut to what is left", agentMax: 4_000_000, perMonth: 10_000_000, used: 9_000_000, want: 1_000_000},
		{name: "repository budget below what is left", agentMax: 200_000, perMonth: 10_000_000, used: 9_000_000, want: 200_000},
		{name: "at the floor", agentMax: 4_000_000, perMonth: 1_000_000, used: 1_000_000 - minAgentTokens, capped: true},
		{name: "over the cap", agentMax: 4_000_000, perMonth: 1_000_000, used: 1_200_000, capped: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, reason := agentBudget(tt.agentMax, tt.perMonth, tt.used)
			if got != tt.want || (reason != "") != tt.capped {
				t.Fatalf("agentBudget = %d, %q; want %d, capped %v", got, reason, tt.want, tt.capped)
			}
		})
	}
}

// TestEstimateParts: the forge's counts of a pull request's lines and files
// size its diff in parts of review.PartBytes, one at least and maxParts at
// most.
func TestEstimateParts(t *testing.T) {
	tests := []struct {
		name                          string
		additions, deletions, changed int
		maxParts, want                int
	}{
		{name: "a small change", additions: 40, deletions: 10, changed: 3, maxParts: 8, want: 1},
		{name: "no counts", maxParts: 8, want: 1},
		{name: "#667's pull request", additions: 6824, deletions: 291, changed: 188, maxParts: 8, want: 8},
		{name: "a medium change", additions: 1000, deletions: 400, changed: 40, maxParts: 8, want: 2},
		{name: "held to maxParts", additions: 50_000, changed: 900, maxParts: 4, want: 4},
		{name: "never split", additions: 50_000, changed: 900, maxParts: 1, want: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pr := forge.OpenPullRequest{Additions: tt.additions, Deletions: tt.deletions, ChangedFiles: tt.changed}
			if got := estimateParts(pr, tt.maxParts); got != tt.want {
				t.Fatalf("estimateParts = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestAdmissionBudget: a split review's grant is each part's budget times
// the parts, within what the month leaves.
func TestAdmissionBudget(t *testing.T) {
	tests := []struct {
		name  string
		a     admission
		parts int
		want  int64
	}{
		{name: "one part", a: admission{maxTokens: 4_000_000}, parts: 1, want: 4_000_000},
		{name: "parts multiply it", a: admission{maxTokens: 4_000_000}, parts: 3, want: 12_000_000},
		{name: "the month caps it", a: admission{maxTokens: 4_000_000, monthLeft: 10_000_000}, parts: 3, want: 10_000_000},
		{name: "under the month's cap", a: admission{maxTokens: 1_000_000, monthLeft: 10_000_000}, parts: 3, want: 3_000_000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.a.budget(tt.parts); got != tt.want {
				t.Fatalf("budget(%d) = %d, want %d", tt.parts, got, tt.want)
			}
		})
	}
}

// TestPartsTimeout: a review's agent time covers its parts in rounds of
// as many as it holds model slots for, up to the cap.
func TestPartsTimeout(t *testing.T) {
	for _, tt := range []struct {
		size    sizing
		timeout time.Duration
		want    time.Duration
	}{
		{size: sizing{parts: 1, slots: 1}, timeout: 20 * time.Minute, want: 20 * time.Minute},
		{size: sizing{parts: 3, slots: 1}, timeout: 20 * time.Minute, want: time.Hour},
		{size: sizing{parts: 3, slots: 2}, timeout: 20 * time.Minute, want: 40 * time.Minute},
		{size: sizing{parts: 8, slots: 8}, timeout: 20 * time.Minute, want: 20 * time.Minute},
		{size: sizing{parts: 8, slots: 1}, timeout: 50 * time.Minute, want: jobtimeout.MaxAgentTimeout},
	} {
		if got := partsTimeout(tt.size.rounds(), tt.timeout); got != tt.want {
			t.Fatalf("%+v at %s each = %s, want %s", tt.size, tt.timeout, got, tt.want)
		}
	}
}
