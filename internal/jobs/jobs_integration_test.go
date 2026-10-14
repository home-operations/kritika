//go:build integration

package jobs_test

import (
	"testing"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/home-operations/kritika/internal/jobs"
	"github.com/home-operations/kritika/internal/store/storetest"
)

func TestFollowupUniqueness(t *testing.T) {
	st := storetest.Open(t)
	queue, err := river.NewClient(riverpgxv5.New(st.App()), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	tx, err := st.App().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, tt := range []struct {
		name string
		args jobs.FollowUpArgs
	}{
		{"conversation", jobs.FollowUpArgs{AccountID: "account", RepositoryID: "repo", Number: 1, CommentID: 987654321}},
		{"inline", jobs.FollowUpArgs{AccountID: "account", RepositoryID: "repo", Number: 1, CommentID: 987654321, Inline: true}},
		{"account", jobs.FollowUpArgs{AccountID: "other", RepositoryID: "repo", Number: 1, CommentID: 987654321}},
		{"repository", jobs.FollowUpArgs{AccountID: "account", RepositoryID: "other", Number: 1, CommentID: 987654321}},
		{"pull request", jobs.FollowUpArgs{AccountID: "account", RepositoryID: "repo", Number: 2, CommentID: 987654321}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			first, err := queue.InsertTx(ctx, tx, tt.args, nil)
			if err != nil {
				t.Fatal(err)
			}
			if first.UniqueSkippedAsDuplicate {
				t.Fatal("distinct comment was deduplicated")
			}
			again, err := queue.InsertTx(ctx, tx, tt.args, nil)
			if err != nil {
				t.Fatal(err)
			}
			if !again.UniqueSkippedAsDuplicate || again.Job.ID != first.Job.ID {
				t.Fatal("redelivered comment was not deduplicated")
			}
		})
	}
}
