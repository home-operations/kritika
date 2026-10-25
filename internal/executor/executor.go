// Package executor runs a runner for one review: as a Kubernetes Job in
// production, in-process for tests and local development. The worker owns
// the run row either way; the executor reports how the run ended.
package executor

import (
	"context"
	"time"

	"github.com/home-operations/kritika/internal/runspec"
)

// Spec is one runner invocation.
type Spec struct {
	// Labels are put on the Job for kubectl and Grafana: account, repository,
	// pr, kind.
	Labels map[string]string
	// Annotations carry the River job id and head SHA.
	Annotations map[string]string
	// Job is the runner's job document, mounted from the run's Secret as the
	// file KRITIKA_RUN_SPEC_FILE names.
	Job runspec.Spec
	// Secrets reach the runner through a Secret owned by its Job, and are
	// masked out of the log tail.
	Secrets runspec.Secrets
	// Deadline bounds the whole run.
	Deadline time.Duration
}

// Result is how a run ended, as far as the executor can tell.
type Result struct {
	JobName, PodName, NodeName string
	ScheduledAt, StartedAt     time.Time
	ExitCode                   int
	TerminationReason          string
	DeadlineExceeded           bool
	// NeverStarted is set when the run was given up on before its
	// container ran. StartedAt is no sign either way: the kubelet sets a
	// pod's start time before it pulls the pod's images.
	NeverStarted bool
	LogTail      string
	// JobUID is the UID of the run's Job, which its Events name.
	JobUID string
	// Abandoned is set when the executor deleted the run's Job because it
	// lost sight of the Job: it went missing from the executor's watch.
	Abandoned bool
	// Disruption says why Kubernetes stopped the run's pod for its own
	// reasons, a node drain, a preemption or an eviction, from the pod's
	// DisruptionTarget condition; empty when it did not.
	Disruption string
	// Err is set when the run did not complete successfully.
	Err error
}

// Executor runs a spec to completion.
type Executor interface {
	Run(ctx context.Context, spec Spec) Result
}

// An EventRecorder records an Event on the Job of a run its executor ran,
// without waiting for the write: the executors that run Jobs are one.
type EventRecorder interface {
	RunEvent(ctx context.Context, res Result, warning bool, reason, note string)
}
