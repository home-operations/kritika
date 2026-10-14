//go:build integration

package jobs_test

import (
	"testing"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/home-operations/kritika/internal/jobs"
	"github.com/home-operations/kritika/internal/store/storetest"
)

// legacyFollowupArgs freezes the queue identity of workers deployed before
// the scoped follow-up store key, including jobs that survive a rollout.
type legacyFollowupArgs struct {
	AccountID    string `json:"account_id"`
	RepositoryID string `json:"repository_id"`
	Number       int    `json:"number"`
	CommentID    int64  `json:"comment_id" river:"unique"`
	Inline       bool   `json:"inline"`
}

func (legacyFollowupArgs) Kind() string { return "followup" }

func (legacyFollowupArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: jobs.QueueFollowUp, MaxAttempts: 8, UniqueOpts: river.UniqueOpts{ByArgs: true}}
}

func TestFollowupRollingUniqueness(t *testing.T) {
	st := storetest.Open(t)
	queue, err := river.NewClient(riverpgxv5.New(st.App()), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	for i, name := range []string{"old first", "new first"} {
		t.Run(name, func(t *testing.T) {
			tx, err := st.App().Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(ctx) }()
			id := int64(987654321 + i)
			current := jobs.FollowUpArgs{AccountID: "account", RepositoryID: "repo", Number: 1, CommentID: id}
			legacy := legacyFollowupArgs{AccountID: "account", RepositoryID: "repo", Number: 1, CommentID: id}
			args := []river.JobArgs{legacy, current, current}
			if i == 1 {
				args[0], args[1] = args[1], args[0]
			}
			var jobID int64
			for j, arg := range args {
				inserted, err := queue.InsertTx(ctx, tx, arg, nil)
				if err != nil {
					t.Fatal(err)
				}
				if j == 0 {
					jobID = inserted.Job.ID
					if inserted.UniqueSkippedAsDuplicate {
						t.Fatal("first delivery deduplicated")
					}
				} else if !inserted.UniqueSkippedAsDuplicate || inserted.Job.ID != jobID {
					t.Fatal("delivery across worker versions was not deduplicated")
				}
			}
		})
	}
}
