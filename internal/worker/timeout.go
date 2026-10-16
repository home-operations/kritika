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
	return agentRunTimeout(w.Current.Get(), job.Args.AccountID, job.Args.RepositoryID, true)
}

// agentRunTimeout is the timeout of a job that runs an agent in a runner
// for the repository: a review, or a follow-up. A review may be split, each
// part with the agent's timeout, and its size is read only once the job
// runs, so its timeout allows for the most parts.
func agentRunTimeout(file *configfile.File, accountID, repositoryID string, review bool) time.Duration {
	account, _ := file.AccountByID(accountID)
	deadline, _ := file.RunnerFor()
	if account != nil {
		longest := deadline
		for _, s := range repoSettings(file, account, repositoryID) {
			timeout := s.Agent.Timeout
			if review {
				timeout = partsTimeout(s.Agent.MaxParts, timeout)
			}
			longest = max(longest, agentDeadline(deadline, timeout))
		}
		deadline = longest
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
	return agentRunTimeout(w.Current.Get(), job.Args.AccountID, job.Args.RepositoryID, false)
}

// repoSettings resolves the settings a repository may have from its id,
// which a job carries instead of the name the configuration is keyed by,
// and without the visibility only the store knows: one resolution without
// a visibility, then one under each visibility the file has an entry for.
// A repository the account does not list gets the account's settings, as
// in Settings.
func repoSettings(file *configfile.File, account *configfile.Account, repositoryID string) []configfile.Settings {
	var name string
	for _, r := range account.Repositories {
		if n := account.Name + "/" + r.Name; configfile.RepositoryID(account.ID(), n) == repositoryID {
			name = n
			break
		}
	}
	out := []configfile.Settings{file.Settings(account, name, configfile.RepoTraits{})}
	for _, v := range configfile.Visibilities {
		if file.Visibility[v] != nil {
			out = append(out, file.Settings(account, name, configfile.RepoTraits{Visibility: v}))
		}
	}
	return out
}
