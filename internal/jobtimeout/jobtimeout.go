// Package jobtimeout holds the River job timeout bounds shared between the
// worker, which computes each job's timeout, and configfile. configfile
// rejects admin configuration that makes River's cap cut a job short.
// configfile must not import worker, so these constants live in this leaf
// package instead, which neither worker nor configfile's other leaf
// dependencies import back.
package jobtimeout

import "time"

// River cancels a job's context once its timeout passes, and supervise
// deletes the runner Job when that happens. So a job's timeout must cover
// everything that the worker does around the runner as well as the runner.
const (
	// HeartbeatInterval is the worker liveness cadence. Six missed beats
	// allow for a slow database before a job is eligible for rescue.
	HeartbeatInterval       = 30 * time.Second
	HeartbeatAbandonedAfter = 6 * HeartbeatInterval

	// LeaseWaitHeadroom is the time a review or a follow-up can spend
	// taking its model lease before its runner starts.
	LeaseWaitHeadroom = 15 * time.Minute
	// PublishHeadroom covers the worker's side of a review after the
	// runner: reading the agent's run and the pack, and the forge
	// write-back, a few requests at most.
	PublishHeadroom = 5 * time.Minute
	// IndexWriteHeadroom covers embedding a repository's staged chunks and
	// swapping the generation after the index runner ends. The embedding
	// pass of a large repository is many model calls.
	IndexWriteHeadroom = 60 * time.Minute
	// AgentFetchHeadroom is the job time a review run keeps for fetching
	// and the context pack on top of the agent's own timeout.
	AgentFetchHeadroom = 5 * time.Minute
	// MaxJobTimeout bounds any review or index job. So a runner deadline
	// set absurdly high cannot hold a worker slot for longer than the
	// stuck-job rescuer waits.
	MaxJobTimeout = 3 * time.Hour
	// RescueStuckJobsAfter is how long a running job is left alone before
	// River treats it as abandoned by a dead worker. It must exceed
	// MaxJobTimeout. Otherwise a job that is still working runs a second time.
	RescueStuckJobsAfter = MaxJobTimeout + time.Hour
)

// MaxRunnerDeadline is the largest runner.activeDeadlineSeconds that an
// account can set. Past it, one of two timeouts exceeds MaxJobTimeout: a
// review's or a follow-up's timeout (deadline plus LeaseWaitHeadroom and
// PublishHeadroom), or an index's timeout (deadline plus IndexWriteHeadroom).
// River then cuts the runner off before it finishes, rather than the
// deadline doing so.
const MaxRunnerDeadline = min(MaxJobTimeout-LeaseWaitHeadroom-PublishHeadroom, MaxJobTimeout-IndexWriteHeadroom)

// MaxAgentTimeout is the largest agent.timeout that a repository can set.
// Past it, agentDeadline's own contribution (agent.timeout plus
// AgentFetchHeadroom, LeaseWaitHeadroom and PublishHeadroom) alone exceeds
// MaxJobTimeout, regardless of the account's runner deadline.
const MaxAgentTimeout = MaxJobTimeout - AgentFetchHeadroom - LeaseWaitHeadroom - PublishHeadroom
