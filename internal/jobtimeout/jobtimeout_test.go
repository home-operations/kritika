package jobtimeout

import (
	"testing"
	"time"

	"github.com/home-operations/kritika/internal/tuning"
)

func TestBounds(t *testing.T) {
	tests := []struct {
		name string
		got  func() time.Duration
		want time.Duration
	}{
		{"max runner deadline", MaxRunnerDeadline, 2 * time.Hour},
		{"index at the max runner deadline", func() time.Duration {
			return MaxRunnerDeadline() + tuning.Get().JobTimeout.IndexWriteHeadroom
		}, tuning.Get().JobTimeout.MaxJobTimeout},
		{"max agent timeout", MaxAgentTimeout, 3*time.Hour - 25*time.Minute},
		{"abandoned after", HeartbeatAbandonedAfter, 3 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.got(); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
	if RescueStuckJobsAfter() <= tuning.Get().JobTimeout.MaxJobTimeout {
		t.Errorf("RescueStuckJobsAfter %v must exceed MaxJobTimeout %v", RescueStuckJobsAfter(), tuning.Get().JobTimeout.MaxJobTimeout)
	}
}

// TestBoundsFollowTuning: the chain recomputes from the process's values.
func TestBoundsFollowTuning(t *testing.T) {
	tuning.Override(t, func(v *tuning.Values) {
		v.JobTimeout.MaxJobTimeout = 4 * time.Hour
		v.JobTimeout.HeartbeatInterval = 10 * time.Second
	})
	for name, tt := range map[string]struct{ got, want time.Duration }{
		"max runner deadline":     {MaxRunnerDeadline(), 3 * time.Hour},
		"max agent timeout":       {MaxAgentTimeout(), 4*time.Hour - 25*time.Minute},
		"rescue stuck jobs after": {RescueStuckJobsAfter(), 5 * time.Hour},
		"abandoned after":         {HeartbeatAbandonedAfter(), time.Minute},
	} {
		if tt.got != tt.want {
			t.Errorf("%s = %v, want %v", name, tt.got, tt.want)
		}
	}
}
