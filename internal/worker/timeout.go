package worker

import (
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/jobs"
	"github.com/home-operations/kritika/internal/store"
	"github.com/home-operations/kritika/internal/tuning"
)

// Timeout implements river.Worker: the runner's deadline, which the agent's
// timeout can lengthen, plus the lease wait and the publish phase around it.
func (w *Review) Timeout(job *river.Job[jobs.ReviewArgs]) time.Duration {
	return riverTimeout(job.JobRow, agentRunTimeout(w.Current.Get(), job.Args.AccountID, true))
}

// agentRunTimeout is the timeout of a job that runs an agent in a runner for
// a repository of the account: a review, or a follow-up. A review can be
// split, each part with the agent's timeout, and its Job deadline covers the
// parts of its agents on one model, one at a time (sizing.rounds). Its size
// and its agents are read only once the job runs, so its timeout allows for
// the most agents, each with the most parts.
func agentRunTimeout(file *configfile.File, accountID string, review bool) time.Duration {
	account, _ := file.AccountByID(accountID)
	deadline := file.RunnerDeadline()
	if account != nil {
		agent := file.Settings(account).Agent
		timeout := agent.Timeout
		if review {
			timeout = partsTimeout(agent.MaxParts*(tuning.Get().ConfigFile.MaxReviewers+1), timeout)
		}
		deadline = agentDeadline(deadline, timeout)
	}
	jt := tuning.Get().JobTimeout
	return min(deadline+jt.LeaseWaitHeadroom+jt.PublishHeadroom, jt.MaxJobTimeout)
}

// Timeout implements river.Worker: the runner's deadline plus embedding
// and writing the generation, which waits on embedding leases.
func (w *Index) Timeout(job *river.Job[jobs.IndexArgs]) time.Duration {
	jt := tuning.Get().JobTimeout
	return riverTimeout(job.JobRow, min(w.Current.Get().RunnerDeadline()+jt.IndexWriteHeadroom, jt.MaxJobTimeout))
}

// Timeout implements river.Worker: a follow-up's agent runs in a runner as
// a review's does, around the same lease wait and forge write-back.
func (w *FollowUp) Timeout(job *river.Job[jobs.FollowUpArgs]) time.Duration {
	return riverTimeout(job.JobRow, agentRunTimeout(w.Current.Get(), job.Args.AccountID, false))
}

// Timeout implements river.Worker.
func (w *Thread) Timeout(job *river.Job[jobs.ThreadArgs]) time.Duration {
	return riverTimeout(job.JobRow, tuning.Get().Worker.ThreadTimeout)
}

// riverTimeout is a job's timeout as River reads it. River's executor reads
// the row as it was claimed, with no record of the attempt, and gets
// computed: this replica's timeout. River's rescuer can run on another
// replica, whose settings during a rollout can compute a shorter one. The
// row it reads carries the rescue horizon that the replica working the job
// recorded (store.RecordJobRescue), which it gets instead.
func riverTimeout(row *rivertype.JobRow, computed time.Duration) time.Duration {
	if after, ok := store.JobRescueAfter(row); ok {
		return after
	}
	return computed
}
