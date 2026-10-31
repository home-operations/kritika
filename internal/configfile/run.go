package configfile

import (
	"time"

	"github.com/home-operations/kritika/internal/config"
)

// Run holds the application configuration used by the workers.
type Run = config.Settings

// PollInterval is how often the leader polls, 0 when polling is off.
func (f *File) PollInterval() time.Duration { return f.Run.PollInterval }

// PollLookback bounds how far back a first or long-idle poll looks.
func (f *File) PollLookback() time.Duration { return f.Run.PollLookback }

// OnboardWindow is how many onboarding index jobs can be queued or running.
func (f *File) OnboardWindow() int { return f.Run.OnboardWindow }

// DisabledIndexGrace is how long the index of a repository that stopped
// running is kept.
func (f *File) DisabledIndexGrace() time.Duration { return f.Run.IndexGrace }

// TranscriptRetention is how long a review's transcript is kept.
func (f *File) TranscriptRetention() time.Duration { return f.Run.TranscriptRetention }

// DiffRetention is how long a review's diff, context and repository files
// are kept.
func (f *File) DiffRetention() time.Duration { return f.Run.DiffRetention }

// RunnerDeadline bounds a runner Job, as its jobTemplate says.
func (f *File) RunnerDeadline() time.Duration { return f.Run.RunnerDeadline() }
