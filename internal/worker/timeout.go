package worker

import (
	"time"

	"github.com/riverqueue/river"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/jobs"
	"github.com/home-operations/kritika/internal/jobtimeout"
)

// Timeout implements river.Worker: the runner's deadline, which the
// agent's timeout may lengthen, plus the lease wait and the publish phase
// around it.
func (w *Review) Timeout(job *river.Job[jobs.ReviewArgs]) time.Duration {
	return agentRunTimeout(w.Current.Get(), job.Args.AccountID, true)
}

// agentRunTimeout is the timeout of a job that runs an agent in a runner
// for a repository of the account: a review, or a follow-up. A review may
// be split, each part with the agent's timeout, and its size is read only
// once the job runs, so its timeout allows for the most parts.
func agentRunTimeout(file *configfile.File, accountID string, review bool) time.Duration {
	account, _ := file.AccountByID(accountID)
	deadline := file.RunnerDeadline()
	if account != nil {
		agent := file.Settings(account).Agent
		timeout := agent.Timeout
		if review {
			timeout = partsTimeout(agent.MaxParts, timeout)
		}
		deadline = agentDeadline(deadline, timeout)
	}
	return min(deadline+jobtimeout.LeaseWaitHeadroom+jobtimeout.PublishHeadroom, jobtimeout.MaxJobTimeout)
}

// Timeout implements river.Worker: the runner's deadline plus embedding
// and writing the generation, which waits on embedding leases.
func (w *Index) Timeout(*river.Job[jobs.IndexArgs]) time.Duration {
	return min(w.Current.Get().RunnerDeadline()+jobtimeout.IndexWriteHeadroom, jobtimeout.MaxJobTimeout)
}

// Timeout implements river.Worker: a follow-up's agent runs in a runner as
// a review's does, around the same lease wait and forge write-back.
func (w *FollowUp) Timeout(job *river.Job[jobs.FollowUpArgs]) time.Duration {
	return agentRunTimeout(w.Current.Get(), job.Args.AccountID, false)
}

// threadTimeout bounds a thread job: a few forge requests, each of which
// may wait out a rate limit for up to a minute, River's default timeout.
const threadTimeout = 5 * time.Minute

// Timeout implements river.Worker.
func (w *Thread) Timeout(*river.Job[jobs.ThreadArgs]) time.Duration { return threadTimeout }
