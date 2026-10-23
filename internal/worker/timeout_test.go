package worker

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	"github.com/riverqueue/river"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/configfile/configfiletest"
	"github.com/home-operations/kritika/internal/jobs"
	"github.com/home-operations/kritika/internal/jobtimeout"
)

const timeoutConfigYAML = `
providers:
  gateway:
    type: openai
    baseUrl: https://models.example.com/v1
    apiKey: { env: TEST_SECRET }
review:
  model: gateway/review-model
apps:
  acme-bot:
    accounts: [acme]
    clientId: Iv1.x
    privateKey: { env: TEST_PEM }
    webhookSecret: { env: TEST_SECRET }
`

func TestJobTimeouts(t *testing.T) {
	t.Setenv("TEST_PEM", "pem")
	t.Setenv("TEST_SECRET", "s")
	tests := []struct {
		name string
		// root is written at the configuration's root.
		root      string
		accountID string
		review    time.Duration
		index     time.Duration
		followUp  time.Duration
	}{
		// The agent's 20m plus 5m of fetch headroom outlasts the runner
		// deadline, plus 15m to take the lease and 5m to publish; the index
		// has 15m + 60m to embed.
		{name: "a review that is never split", root: "agent: { parts: 1 }\n", accountID: "acme", review: 45 * time.Minute,
			index: 75 * time.Minute, followUp: 45 * time.Minute},
		// Eight parts of 20m each pass the agent's cap, so a review may run
		// to the job cap; a follow-up is never split.
		{name: "the default agent timeout", accountID: "acme", review: jobtimeout.MaxJobTimeout, index: 75 * time.Minute, followUp: 45 * time.Minute},
		{name: "a longer agent timeout", root: "agent: { timeout: 50m }\n", accountID: "acme",
			review: jobtimeout.MaxJobTimeout, index: 75 * time.Minute, followUp: 75 * time.Minute},
		{name: "unknown account", accountID: "missing", review: 35 * time.Minute, index: 75 * time.Minute, followUp: 35 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.review <= time.Minute || tt.index <= time.Minute || tt.followUp <= time.Minute {
				t.Fatal("a job timeout must outlast River's one-minute default")
			}
			file := configfiletest.Load(t, tt.root+timeoutConfigYAML)
			current := configfile.NewCurrent(file)
			accountID := tt.accountID
			if a, ok := file.Account(configfile.ForgeGitHub, tt.accountID); ok {
				accountID = a.ID()
			}
			review, index, followUp := &Review{Current: current}, &Index{Current: current}, &FollowUp{Current: current}
			got := review.Timeout(&river.Job[jobs.ReviewArgs]{Args: jobs.ReviewArgs{AccountID: accountID}})
			if got != tt.review {
				t.Errorf("review timeout = %s, want %s", got, tt.review)
			}
			got = index.Timeout(&river.Job[jobs.IndexArgs]{Args: jobs.IndexArgs{AccountID: accountID}})
			if got != tt.index {
				t.Errorf("index timeout = %s, want %s", got, tt.index)
			}
			got = followUp.Timeout(&river.Job[jobs.FollowUpArgs]{Args: jobs.FollowUpArgs{AccountID: accountID}})
			if got != tt.followUp {
				t.Errorf("follow-up timeout = %s, want %s", got, tt.followUp)
			}
		})
	}

	// A runner deadline at configfile's max: the index job lands exactly on
	// MaxJobTimeout.
	t.Setenv("KRITIKA_RUNNER_JOB_TEMPLATE", fmt.Sprintf(`{"spec":{"activeDeadlineSeconds":%d}}`, int64(jobtimeout.MaxRunnerDeadline/time.Second)))
	file := configfiletest.Load(t, "agent: { parts: 1 }\n"+timeoutConfigYAML)
	acme, _ := file.Account(configfile.ForgeGitHub, "acme")
	current := configfile.NewCurrent(file)
	review, index := &Review{Current: current}, &Index{Current: current}
	if got, want := review.Timeout(&river.Job[jobs.ReviewArgs]{Args: jobs.ReviewArgs{AccountID: acme.ID()}}),
		jobtimeout.MaxRunnerDeadline+jobtimeout.LeaseWaitHeadroom+jobtimeout.PublishHeadroom; got != want {
		t.Errorf("review timeout at the max deadline = %s, want %s", got, want)
	}
	if got := index.Timeout(&river.Job[jobs.IndexArgs]{}); got != jobtimeout.MaxRunnerDeadline+jobtimeout.IndexWriteHeadroom {
		t.Errorf("index timeout at the max deadline = %s", got)
	}
}

// TestThreadTimeout: a thread job's few forge requests may each wait out
// a rate limit of up to a minute, longer than River's default timeout.
func TestThreadTimeout(t *testing.T) {
	if got := (&Thread{}).Timeout(&river.Job[jobs.ThreadArgs]{}); got != 5*time.Minute {
		t.Fatalf("thread timeout = %s, want 5m: room for a few forge requests that each wait out a rate limit", got)
	}
}

func TestDetach(t *testing.T) {
	parent, cancelParent := context.WithCancel(t.Context())
	ctx, cancel := detach(parent)
	defer cancel()
	cancelParent()
	if ctx.Err() != nil {
		t.Fatalf("detached ctx ended with its parent: %v", ctx.Err())
	}
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > detachTimeout {
		t.Fatalf("deadline = %v, %v; want one within %v", deadline, ok, detachTimeout)
	}
}

// TestDetachUnlessFenced: the job's end leaves the detached context
// running, unless the job is fenced, before River ends it or after.
func TestDetachUnlessFenced(t *testing.T) {
	for _, tt := range []struct {
		name string
		// end ends the job: stop as River does, fence as its heartbeat does.
		end  func(stop, fence context.CancelCauseFunc)
		ends bool
	}{
		{"the job stops", func(stop, _ context.CancelCauseFunc) { stop(context.Canceled) }, false},
		{"the job times out", func(stop, _ context.CancelCauseFunc) { stop(context.DeadlineExceeded) }, false},
		{"the job is fenced", func(stop, fence context.CancelCauseFunc) {
			fence(errJobFenced)
			stop(errJobFenced)
		}, true},
		{"the job is fenced after it stopped", func(stop, fence context.CancelCauseFunc) {
			stop(context.Canceled)
			fence(errJobFenced)
		}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				fenced, fence := context.WithCancelCause(context.Background())
				defer fence(nil)
				parent, stop := context.WithCancelCause(context.WithValue(t.Context(), jobFenceKey{}, fenced))
				defer stop(nil)
				ctx, cancel := detachUnlessFenced(parent)
				defer cancel()
				tt.end(stop, fence)
				synctest.Wait()
				if ended := ctx.Err() != nil; ended != tt.ends || ended && !errors.Is(context.Cause(ctx), errJobFenced) {
					t.Fatalf("detached ctx ended = %v (%v), want %v", ended, context.Cause(ctx), tt.ends)
				}
			})
		})
	}
}
