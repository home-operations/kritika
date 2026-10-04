package webapi

import (
	"time"

	"github.com/riverqueue/river/rivertype"
)

// JobArgs are the parts of a job's arguments the queue shows; fields a
// kind does not carry are zero.
type JobArgs struct {
	Repository string `json:"repository"`
	Number     int    `json:"number"`
	Head       string `json:"head"`
	Trigger    string `json:"trigger"`
	CommentID  int64  `json:"commentId"`
}

// Job is one River job of the account.
type Job struct {
	ID          int64              `json:"id"`
	Kind        string             `json:"kind"`
	State       rivertype.JobState `json:"state"`
	Attempt     int                `json:"attempt"`
	MaxAttempts int                `json:"maxAttempts"`
	CreatedAt   time.Time          `json:"createdAt"`
	ScheduledAt time.Time          `json:"scheduledAt"`
	AttemptedAt *time.Time         `json:"attemptedAt"`
	FinalizedAt *time.Time         `json:"finalizedAt"`
	Args        JobArgs            `json:"args"`
	LastError   string             `json:"lastError"`
	// Cause is why the last attempt failed, "" when the error does not say.
	Cause JobCause `json:"cause"`
}

// InstanceJob is a job with the slug of the account it is of.
type InstanceJob struct {
	Job
	Account string `json:"account"`
}

// ModelSlots is how many of an account's concurrency slots for a model
// are held by a running review. Slots is the account's limit, 0 for none.
type ModelSlots struct {
	Account string `json:"account"`
	Model   string `json:"model"`
	Held    int    `json:"held"`
	Slots   int    `json:"slots"`
}

// InstanceQueue is the queue of every account the viewer can read: the
// jobs that have not finished, oldest first, then the most recently
// finished, and the model slots that say why a job waits.
type InstanceQueue struct {
	Jobs  []InstanceJob `json:"jobs"`
	Slots []ModelSlots  `json:"slots"`
}
