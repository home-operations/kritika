// Package jobs defines the River job arguments that ingest, the leader and
// the dashboard enqueue and the worker consumes. Uniqueness lives here because it is the contract
// between the two: a review is unique per head SHA so no push is ever lost,
// a follow-up per comment, a thread change per thread and state, an index
// run per target commit.
package jobs

import (
	"strings"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

// Queue names, one per job kind so the worker can bound each separately.
const (
	QueueReview   = "review"
	QueuePrepare  = "review_prepare"
	QueuePublish  = "review_publish"
	QueueFollowUp = "followup"
	QueueThread   = "thread"
	QueueIndex    = "index"
)

// Queues lists the queues whose backlog and pickup delay are monitored.
func Queues() []string {
	return []string{QueueReview, QueuePrepare, QueuePublish, QueueFollowUp, QueueThread, QueueIndex}
}

// ReviewKind reports whether a job owns a stage of a review.
func ReviewKind(kind string) bool {
	return kind == QueueReview || kind == QueuePrepare || kind == QueuePublish
}

// TriggerManual is the Trigger a human-requested re-run carries. It is the
// only trigger the worker lets bypass the bot-author patch-id skip; a
// review that replaces an undelivered manual one stands in for it (see
// store.ReviewOwed).
const TriggerManual = "manual"

// LabelChange reports whether trigger is a label added to or removed from
// the pull request: the head is the one already judged, so only a filter
// that reads labels can decide differently.
func LabelChange(trigger string) bool { return trigger == "labeled" || trigger == "unlabeled" }

// TriggerReindex is the Trigger EnqueueReindex gives a forced full reindex,
// as opposed to the worker-internal onboard and push triggers.
const TriggerReindex = "reindex"

// Index job triggers the service itself sets.
const (
	TriggerOnboard = "onboard"
	TriggerPush    = "push"
)

// OnboardRetryAfter is how long a repository whose onboarding job ended
// without building an index waits to be offered again: the feeder's pace,
// and the dashboard's, which shows where a repository waits for it. River
// keeps finished jobs longer than that.
const OnboardRetryAfter = time.Hour

// Index job priorities, highest first: River always fetches a higher
// priority first, so an onboarding wave never delays keeping an indexed
// repository current.
const (
	indexPriorityUpdate  = 1
	indexPriorityReindex = 2
	indexPriorityOnboard = 4
)

// indexAttempts bounds an index job's tries: transient failures (a fetch
// timeout, a runner killed at its deadline) get River's backoff, and an
// onboarding that fails every try is offered again later.
const indexAttempts = 3

// reviewAttempts bounds a review, follow-up or thread job's tries. With
// River's backoff the last comes over an hour after the first, which
// outlasts a restart or a database failover; a job that fails every try is
// failing on its input, and each further try would only repeat its failed
// review and commit status.
const reviewAttempts = 8

// LiveStates are the states a job is in while queued or running: what a
// job's unique key spans, and what counts as a job still to come.
var LiveStates = []rivertype.JobState{
	rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRetryable,
	rivertype.JobStateRunning, rivertype.JobStateScheduled,
}

// LiveStatesSQL is LiveStates as a SQL list, for a state IN (...) clause.
func LiveStatesSQL() string {
	quoted := make([]string, len(LiveStates))
	for i, s := range LiveStates {
		quoted[i] = "'" + string(s) + "'"
	}
	return strings.Join(quoted, ", ")
}

// ReviewArgs reviews one head of one pull request.
type ReviewArgs struct {
	AccountID    string `json:"account_id"     river:"unique"`
	RepositoryID string `json:"repository_id" river:"unique"`
	Number       int    `json:"number"        river:"unique"`
	HeadSHA      string `json:"head_sha"      river:"unique"`
	// Trigger is why: opened, synchronize, reopened, ready_for_review, poll,
	// labeled, unlabeled, manual.
	Trigger string `json:"trigger"`
	// Request distinguishes one manual re-run, or one label change, from
	// another. River dedupes by the river:"unique" fields, so every other
	// trigger leaves it empty and dedupes on account, repository, number
	// and head, and these two set a fresh value (a UUID) so the job is never
	// deduped against a prior run of the same head, including another of
	// its kind.
	Request string `json:"request,omitempty" river:"unique"`
	// Reviewer, on a re-run someone asked for, names the one reviewer it
	// runs, whatever its paths and conditions; "" runs every reviewer that
	// applies.
	Reviewer string `json:"reviewer,omitempty"`
	// Requested is the word a re-run's request wrote after review that
	// names no reviewer, which the review states it ran every reviewer in
	// place of; it leaves the request one of every reviewer.
	Requested string `json:"requested,omitempty"`
	// Focus is what a re-run's request asks the review to look hardest
	// at, the text written after review and the reviewer's name, as
	// review.CleanFocus keeps it; "" asks for nothing in particular.
	Focus string `json:"focus,omitempty"`
}

// Kind implements river.JobArgs.
func (ReviewArgs) Kind() string { return "review" }

// InsertOpts implements river.JobArgsWithInsertOpts.
func (ReviewArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: QueueReview, MaxAttempts: reviewAttempts, UniqueOpts: river.UniqueOpts{ByArgs: true}}
}

// PrepareArgs finalizes a saved runner answer, including merge and confidence calls.
type PrepareArgs struct {
	ReviewArgs
	ReviewID string `json:"review_id" river:"unique"`
}

func (PrepareArgs) Kind() string { return QueuePrepare }

func (PrepareArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: QueuePrepare, MaxAttempts: reviewAttempts, UniqueOpts: river.UniqueOpts{ByArgs: true}}
}

// PublishArgs delivers a finalized answer without calling a model.
type PublishArgs PrepareArgs

func (PublishArgs) Kind() string { return QueuePublish }

func (PublishArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: QueuePublish, MaxAttempts: reviewAttempts, UniqueOpts: river.UniqueOpts{ByArgs: true}}
}

// FollowUpArgs answers one comment that addressed the bot, or, with Reply,
// one that replied in a thread of its own without addressing it.
type FollowUpArgs struct {
	AccountID    string `json:"account_id"`
	RepositoryID string `json:"repository_id"`
	Number       int    `json:"number"`
	CommentID    int64  `json:"comment_id" river:"unique"`
	Inline       bool   `json:"inline"`
	// Reply says the comment may be answered as a reply in one of the
	// bot's finding threads when it does not mention the bot.
	Reply bool `json:"reply,omitempty"`
}

// Kind implements river.JobArgs.
func (FollowUpArgs) Kind() string { return "followup" }

// InsertOpts implements river.JobArgsWithInsertOpts.
func (FollowUpArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: QueueFollowUp, MaxAttempts: reviewAttempts, UniqueOpts: river.UniqueOpts{ByArgs: true}}
}

// ThreadArgs applies a review thread someone resolved or unresolved to the
// finding it holds.
type ThreadArgs struct {
	AccountID    string `json:"account_id"`
	RepositoryID string `json:"repository_id"`
	Number       int    `json:"number"`
	// CommentID is the inline comment that opened the thread.
	CommentID int64 `json:"comment_id" river:"unique"`
	// Resolved is the thread's state when the event was emitted.
	Resolved bool `json:"resolved" river:"unique"`
	// Outdated is whether a push had changed the lines the comment was made
	// on by the time the thread changed state.
	Outdated bool `json:"outdated"`
	// Sender is who changed it.
	Sender string `json:"sender"`
}

// Kind implements river.JobArgs.
func (ThreadArgs) Kind() string { return "thread" }

// InsertOpts implements river.JobArgsWithInsertOpts. A job is unique per
// thread and state while queued or running: a thread resolved and then
// unresolved is two jobs, the same change delivered twice is one, and a
// thread resolved again later is a new job.
func (ThreadArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		Queue: QueueThread, MaxAttempts: reviewAttempts, UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: LiveStates},
	}
}

// IndexArgs builds or advances a repository's index to its default branch
// tip, whatever the tip is when the job runs.
type IndexArgs struct {
	AccountID    string `json:"account_id"`
	RepositoryID string `json:"repository_id" river:"unique"`
	// CommitSHA is the commit a push moved the default branch to, for the
	// record only: a burst of pushes needs one job, not one each, so it is
	// not part of the unique key.
	CommitSHA string `json:"commit_sha"`
	// Trigger is why: TriggerOnboard, TriggerPush or TriggerReindex.
	Trigger string `json:"trigger"`
	// Full forces a full rebuild even when the active generation already
	// covers the tip. It is part of the unique key, so a forced rebuild is
	// queued beside an update rather than folded into it, and dedupes only
	// onto another forced rebuild.
	Full bool `json:"full,omitempty" river:"unique"`
	// CatchUp identifies the rebuild whose publication needs a tip check.
	// Its update must not dedupe onto a push that published before the rebuild.
	CatchUp string `json:"catch_up,omitempty" river:"unique"`
}

// Kind implements river.JobArgs.
func (IndexArgs) Kind() string { return "index" }

// InsertOpts implements river.JobArgsWithInsertOpts. A job is unique per
// repository while queued or running (River requires running in the set):
// a push while the repository's job is running is absorbed by it, and the
// worker indexes the tip again when it finds the branch moved. Full and
// CatchUp key a forced rebuild and a rebuild's catch-up apart, so either
// can run beside an update.
func (a IndexArgs) InsertOpts() river.InsertOpts {
	priority := indexPriorityUpdate
	switch a.Trigger {
	case TriggerReindex:
		priority = indexPriorityReindex
	case TriggerOnboard:
		priority = indexPriorityOnboard
	}
	return river.InsertOpts{
		Queue: QueueIndex, Priority: priority, MaxAttempts: indexAttempts,
		UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: LiveStates},
	}
}
