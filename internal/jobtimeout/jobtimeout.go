// Package jobtimeout holds the River job timeout bounds shared between the
// worker, which computes each job's timeout, and configfile. configfile
// rejects admin configuration that makes River's cap cut a job short.
// configfile must not import worker, so these bounds live in this leaf
// package instead, which neither worker nor configfile's other leaf
// dependencies import back.
//
// River cancels a job's context once its timeout passes, and supervise
// deletes the runner Job when that happens. So a job's timeout must cover
// everything that the worker does around the runner as well as the runner.
// The bounds derive from tuning's JobTimeout area: MaxJobTimeout caps every
// review or index job, and the headrooms are what a job keeps around its
// runner, which leaves MaxRunnerDeadline to the runner and MaxAgentTimeout
// to the agent. Each function reads the process's current values.
package jobtimeout

import (
	"time"

	"github.com/home-operations/kritika/internal/tuning"
)

// HeartbeatAbandonedAfter is the age of a job's last beat past which it
// belongs to a replica that died: tuning's JobTimeout.AbandonedBeats missed
// beats, to allow for a slow database.
func HeartbeatAbandonedAfter() time.Duration {
	return tuning.Get().JobTimeout.HeartbeatAbandonedAfter()
}

// RescueStuckJobsAfter is how long a running job is left alone before River
// treats it as abandoned by a dead worker. It exceeds MaxJobTimeout, by
// JobTimeout.RescueGrace. Otherwise a job that is still working runs a
// second time.
func RescueStuckJobsAfter() time.Duration { return tuning.Get().JobTimeout.RescueStuckJobsAfter() }

// MaxRunnerDeadline is the largest runner.activeDeadlineSeconds that an
// account can set. Past it, one of two timeouts exceeds MaxJobTimeout: a
// review's or a follow-up's timeout (deadline plus LeaseWaitHeadroom and
// PublishHeadroom), or an index's timeout (deadline plus IndexWriteHeadroom).
// River then cuts the runner off before it finishes, rather than the
// deadline doing so.
func MaxRunnerDeadline() time.Duration { return tuning.Get().JobTimeout.MaxRunnerDeadline() }

// MaxAgentTimeout is the largest agent.timeout that a repository can set.
// Past it, agentDeadline's own contribution (agent.timeout plus
// AgentFetchHeadroom, LeaseWaitHeadroom and PublishHeadroom) alone exceeds
// MaxJobTimeout, regardless of the account's runner deadline.
func MaxAgentTimeout() time.Duration { return tuning.Get().JobTimeout.MaxAgentTimeout() }
