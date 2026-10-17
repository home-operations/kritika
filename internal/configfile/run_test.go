package configfile

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/home-operations/kritika/internal/config"
	"github.com/home-operations/kritika/internal/jobtimeout"
)

// TestRun checks how kritika runs comes from the environment: its defaults,
// values set, an interval of 0s that turns polling off, and values refused.
func TestRun(t *testing.T) {
	t.Setenv("TEST_PRIVATE_KEY", "tok")
	t.Setenv("TEST_WEBHOOK_SECRET", "whsec")

	t.Run("defaults when unset", func(t *testing.T) {
		f := mustLoad(t, minimal)
		if f.PollInterval() != 10*time.Minute || f.PollLookback() != 24*time.Hour || f.OnboardWindow() != 4 ||
			f.DisabledIndexGrace() != 30*24*time.Hour || f.TranscriptRetention() != 30*24*time.Hour || f.DiffRetention() != 30*24*time.Hour ||
			f.RunnerDeadline() != config.DefaultRunnerDeadline {
			t.Fatalf("run = %+v", f.Run)
		}
	})

	t.Run("set values, and an interval of 0s turns polling off", func(t *testing.T) {
		for k, v := range map[string]string{
			"KRITIKA_POLL_INTERVAL": "0s", "KRITIKA_POLL_LOOKBACK": "1h", "KRITIKA_ONBOARD_WINDOW": "8", "KRITIKA_INDEX_GRACE": "48h",
			"KRITIKA_TRANSCRIPT_RETENTION": "72h", "KRITIKA_DIFF_RETENTION": "96h",
			"KRITIKA_RUNNER_JOB_TEMPLATE": `{"spec":{"activeDeadlineSeconds":600}}`,
		} {
			t.Setenv(k, v)
		}
		f := mustLoad(t, minimal)
		if f.PollInterval() != 0 || f.PollLookback() != time.Hour || f.OnboardWindow() != 8 || f.DisabledIndexGrace() != 48*time.Hour ||
			f.TranscriptRetention() != 72*time.Hour || f.DiffRetention() != 96*time.Hour || f.RunnerDeadline() != 10*time.Minute {
			t.Fatalf("run = %+v", f.Run)
		}
	})

	refused := []struct{ env, value, want string }{
		{"KRITIKA_POLL_INTERVAL", "-1m", "must not be negative"},
		{"KRITIKA_ONBOARD_WINDOW", "0", "KRITIKA_ONBOARD_WINDOW must be positive"},
		{"KRITIKA_TRANSCRIPT_RETENTION", "1h", "KRITIKA_TRANSCRIPT_RETENTION must be at least 24h"},
		{"KRITIKA_DIFF_RETENTION", "1h", "KRITIKA_DIFF_RETENTION must be at least 24h"},
		{"KRITIKA_RUNNER_JOB_TEMPLATE", fmt.Sprintf(`{"spec":{"activeDeadlineSeconds":%d}}`, int64(jobtimeout.MaxRunnerDeadline.Seconds())+1),
			"KRITIKA_RUNNER_JOB_TEMPLATE must be a whole number of seconds from 1 to 7200"},
		{"KRITIKA_RUNNER_POD_TEMPLATE", "[1]", "KRITIKA_RUNNER_POD_TEMPLATE"},
		{"KRITIKA_POLL_LOOKBACK", "soon", "KRITIKA_POLL_LOOKBACK"},
	}
	for _, tt := range refused {
		t.Run(tt.env+"="+tt.value, func(t *testing.T) {
			t.Setenv(tt.env, tt.value)
			if _, err := Parse([]byte(minimal)); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Parse = %v, want an error containing %q", err, tt.want)
			}
		})
	}
	t.Run("a deadline at the cap", func(t *testing.T) {
		t.Setenv("KRITIKA_RUNNER_JOB_TEMPLATE", fmt.Sprintf(`{"spec":{"activeDeadlineSeconds":%d}}`, int64(jobtimeout.MaxRunnerDeadline.Seconds())))
		mustLoad(t, minimal)
	})
}
