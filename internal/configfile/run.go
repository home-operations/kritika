package configfile

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/home-operations/kritika/internal/config"
	"github.com/home-operations/kritika/internal/jobtimeout"
)

// Run holds the application settings used by the workers.
type Run = config.Settings

func validateRun(r Run) error {
	switch {
	case r.PollInterval < 0 || r.PollLookback < 0:
		return errors.New("configfile: KRITIKA_POLL_INTERVAL and KRITIKA_POLL_LOOKBACK must not be negative")
	case r.OnboardWindow < 1:
		return errors.New("configfile: KRITIKA_ONBOARD_WINDOW must be positive")
	case r.IndexGrace < 0:
		return errors.New("configfile: KRITIKA_INDEX_GRACE must not be negative")
	case r.TranscriptRetention < minRetention:
		return fmt.Errorf("configfile: KRITIKA_TRANSCRIPT_RETENTION must be at least %s", minRetention)
	case r.DiffRetention < minRetention:
		return fmt.Errorf("configfile: KRITIKA_DIFF_RETENTION must be at least %s", minRetention)
	case r.RunnerDeadline <= 0:
		return errors.New("configfile: KRITIKA_RUNNER_DEADLINE must be positive")
	case r.RunnerDeadline > jobtimeout.MaxRunnerDeadline:
		return fmt.Errorf("configfile: KRITIKA_RUNNER_DEADLINE must not exceed %s, "+
			"or River's %s job timeout cap would cut the runner off early", jobtimeout.MaxRunnerDeadline, jobtimeout.MaxJobTimeout)
	}
	return validateTools(r.Tools)
}

// PollInterval is how often the leader polls, 0 when polling is off.
func (f *File) PollInterval() time.Duration { return f.Run.PollInterval }

// PollLookback bounds how far back a first or long-idle poll looks.
func (f *File) PollLookback() time.Duration { return f.Run.PollLookback }

// OnboardWindow is how many onboarding index jobs may be queued or running.
func (f *File) OnboardWindow() int { return f.Run.OnboardWindow }

// DisabledIndexGrace is how long the index of a repository that stopped
// running is kept.
func (f *File) DisabledIndexGrace() time.Duration { return f.Run.IndexGrace }

// TranscriptRetention is how long a review's transcript is kept.
func (f *File) TranscriptRetention() time.Duration { return f.Run.TranscriptRetention }

// DiffRetention is how long a review's diff, context and repository files
// are kept.
func (f *File) DiffRetention() time.Duration { return f.Run.DiffRetention }

// RunnerFor resolves a runner Job's deadline and resources.
func (f *File) RunnerFor() (deadline time.Duration, resources map[string]any) {
	return f.Run.RunnerDeadline, f.Run.RunnerResources
}

// ToolsFor returns the tools that provide any of commands, the ones a run
// whose agent may run commands mounts; nil when none does.
func (f *File) ToolsFor(commands []string) []Tool {
	var out []Tool
	for _, t := range f.Run.Tools {
		if slices.ContainsFunc(t.Provides(), func(c string) bool { return slices.Contains(commands, c) }) {
			out = append(out, t)
		}
	}
	return out
}
