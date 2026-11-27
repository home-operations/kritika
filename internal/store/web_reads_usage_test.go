package store

import (
	"strings"
	"testing"
	"time"

	"github.com/home-operations/kritika/internal/tuning"
)

// TestJobColumnsFollowTuning: the queue view's overdue heartbeat and recent
// failure bounds are the process's values when it reads, not the defaults
// at start-up.
func TestJobColumnsFollowTuning(t *testing.T) {
	for _, want := range []string{"< now() -\n\t\t\tinterval '180 seconds'", ">= now() - interval '86400 seconds'"} {
		if got := jobColumns(); !strings.Contains(got, want) {
			t.Errorf("jobColumns at the defaults lacks %q", want)
		}
	}
	tuning.Override(t, func(v *tuning.Values) {
		v.JobTimeout.HeartbeatInterval = 10 * time.Second
		v.Store.RecentFailureWindow = 90 * time.Minute
	})
	for _, want := range []string{"< now() -\n\t\t\tinterval '60 seconds'", ">= now() - interval '5400 seconds'"} {
		if got := jobColumns(); !strings.Contains(got, want) {
			t.Errorf("jobColumns overridden lacks %q", want)
		}
	}
}

func TestIntervalSQL(t *testing.T) {
	for d, want := range map[time.Duration]string{
		time.Minute:             "interval '60 seconds'",
		1500 * time.Millisecond: "interval '1.5 seconds'",
	} {
		if got := intervalSQL(d); got != want {
			t.Errorf("intervalSQL(%s) = %q, want %q", d, got, want)
		}
	}
}
