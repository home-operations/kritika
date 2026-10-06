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
	return agentRunTimeout(w.Current.Get(), job.Args.AccountID, job.Args.RepositoryID)
}

// agentRunTimeout is the timeout of a job that runs an agent in a runner
// for the repository: a review, or a follow-up.
func agentRunTimeout(file *configfile.File, accountID, repositoryID string) time.Duration {
	account, _ := file.AccountByID(accountID)
	deadline, _ := file.RunnerFor()
	if account != nil {
		deadline = agentDeadline(deadline, repoSettings(file, account, repositoryID).Agent.Timeout)
	}
	return min(deadline+jobtimeout.LeaseWaitHeadroom+jobtimeout.PublishHeadroom, jobtimeout.MaxJobTimeout)
}

// Timeout implements river.Worker: the runner's deadline plus embedding
// and writing the generation, which waits on embedding leases.
func (w *Index) Timeout(*river.Job[jobs.IndexArgs]) time.Duration {
	deadline, _ := w.Current.Get().RunnerFor()
	return min(deadline+jobtimeout.IndexWriteHeadroom, jobtimeout.MaxJobTimeout)
}

// Timeout implements river.Worker: a follow-up's agent runs in a runner as
// a review's does, around the same lease wait and forge write-back.
func (w *FollowUp) Timeout(job *river.Job[jobs.FollowUpArgs]) time.Duration {
	return agentRunTimeout(w.Current.Get(), job.Args.AccountID, job.Args.RepositoryID)
}

// repoSettings resolves a repository's settings from its id, which a job
// carries instead of the name the configuration is keyed by. A repository
// the account does not list gets the account's settings, as in Settings.
func repoSettings(file *configfile.File, account *configfile.Account, repositoryID string) configfile.Settings {
	for _, r := range account.Repositories {
		if name := account.Name + "/" + r.Name; configfile.RepositoryID(account.ID(), name) == repositoryID {
			return file.Settings(account, name)
		}
	}
	return file.Settings(account, "")
}
