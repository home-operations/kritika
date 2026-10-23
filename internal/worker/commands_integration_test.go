//go:build integration

package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertest"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/configfile/configfiletest"
	"github.com/home-operations/kritika/internal/forge"
	"github.com/home-operations/kritika/internal/ingest"
	"github.com/home-operations/kritika/internal/jobs"
	"github.com/home-operations/kritika/internal/store"
	"github.com/home-operations/kritika/internal/store/storetest"
	"github.com/home-operations/kritika/internal/webhook"
)

type reviewRequestForge struct {
	*localForge
	onEyes  func()
	refused bool
}

func (f *reviewRequestForge) React(ctx context.Context, owner, repo string, comment forge.Comment, content string) (int64, error) {
	if f.refused {
		return 0, errors.New("Resource not accessible by integration")
	}
	id, err := f.localForge.React(ctx, owner, repo, comment, content)
	if content == forge.ReactionEyes && f.onEyes != nil {
		fn := f.onEyes
		f.onEyes = nil
		fn()
	}
	return id, err
}

func TestReviewRequestLifecycle(t *testing.T) {
	ctx := t.Context()
	st := storetest.Open(t)
	t.Setenv("TEST_PEM", "pem")
	t.Setenv("TEST_SECRET", "test-provider-key")
	file := configfiletest.Load(t, configYAML)
	if err := st.ApplyConfig(ctx, file); err != nil {
		t.Fatal(err)
	}
	storetest.Repositories(t, st, file, "onedr0p/home-ops")
	queue, err := river.NewClient(riverpgxv5.New(st.App()), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	ctx = rivertest.WorkContext(ctx, queue)
	account, _ := file.Account(configfile.ForgeGitHub, "onedr0p")
	repoID := configfile.RepositoryID(account.ID(), "onedr0p/home-ops")
	svc := ingest.NewService(st, queue)
	tests := []struct {
		name               string
		status             store.ReviewStatus
		pollErr            bool
		cancelAfterEnqueue bool
		refused            bool
		missingEyes        bool
		ambiguousCommit    bool
		mark               string
	}{
		{name: "initial polling error", status: store.ReviewCompleted, pollErr: true, mark: forge.ReactionDone},
		{name: "canceled after enqueue", status: store.ReviewCompleted, cancelAfterEnqueue: true, mark: forge.ReactionDone},
		{name: "missing eyes after acknowledgment", missingEyes: true, status: store.ReviewCompleted, mark: forge.ReactionDone},
		{name: "ambiguous commit", ambiguousCommit: true, status: store.ReviewCompleted, mark: forge.ReactionDone},
		{name: "completed", status: store.ReviewCompleted, mark: forge.ReactionDone},
		{name: "failed", status: store.ReviewFailed, mark: forge.ReactionFailed},
		{name: "none ended", mark: forge.ReactionFailed},
		{name: "skipped", status: store.ReviewSkipped},
		{name: "superseded", status: store.ReviewSuperseded},
		{name: "capped", status: store.ReviewCapped},
		{name: "canceled", status: store.ReviewCanceled},
		{name: "reactions refused", status: store.ReviewCompleted, refused: true},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			number, commentID := 4300+i, commentBase+int64(i+1)
			head := fmt.Sprintf("head-%d", number)
			var prID string
			if err := st.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
				return tx.QueryRow(ctx, `INSERT INTO pull_requests (account_id, repository_id, number, head_sha)
					VALUES ($1, $2, $3, $4) RETURNING id`, account.ID(), repoID, number, head).Scan(&prID)
			}); err != nil {
				t.Fatal(err)
			}
			lf := &reviewRequestForge{localForge: &localForge{
				comments: map[int64]string{commentID: "@kritika review"}, authors: map[int64]string{commentID: "onedr0p"},
				permissions: map[string]forge.Permission{"onedr0p": forge.PermissionWrite},
			}, refused: tt.refused}
			w := &FollowUp{
				Store: st, Current: configfile.NewCurrent(file), Forges: &forges{f: lf}, Logger: slog.New(slog.DiscardHandler),
			}
			args := jobs.FollowUpArgs{AccountID: account.ID(), RepositoryID: repoID, Number: number, CommentID: commentID}
			t.Cleanup(func() { cleanupReviewRequest(ctx, t, w, prID, head, args) })
			inserted, err := queue.Insert(ctx, args, nil)
			if err != nil || inserted.UniqueSkippedAsDuplicate {
				t.Fatalf("insert follow-up = %+v, %v", inserted, err)
			}
			job := &river.Job[jobs.FollowUpArgs]{JobRow: inserted.Job, Args: args}
			job.Attempt = 1
			if tt.pollErr {
				// The queued job is made unreadable after enqueueing, so the
				// initial poll fails while the follow-up record can still commit.
				lf.onEyes = func() {
					if _, err := st.App().Exec(ctx, `UPDATE river_job SET args = jsonb_set(args, '{number}', '"invalid"')
						WHERE kind = 'review' AND args->>'account_id' = $1 AND args->>'repository_id' = $2
						AND args->>'head_sha' = $3`, account.ID(), repoID, head); err != nil {
						t.Fatal(err)
					}
				}
			}
			workCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			if tt.cancelAfterEnqueue {
				lf.onEyes = cancel
			}
			err = w.Work(workCtx, job)
			_, snoozed := errors.AsType[*river.JobSnoozeError](err)
			if tt.cancelAfterEnqueue {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("canceled request = %v, want context cancellation", err)
				}
			} else if tt.pollErr {
				if err == nil || snoozed {
					t.Fatalf("initial poll = %v, want a database error", err)
				}
			} else if !snoozed {
				t.Fatalf("initial poll = %v, want snooze", err)
			}
			var status, reason string
			if err := st.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
				return tx.QueryRow(ctx, `SELECT status, reason FROM followups WHERE pull_request_id = $1 AND comment_id = $2`,
					prID, commentID).Scan(&status, &reason)
			}); err != nil || status != string(store.FollowupAnswered) || reason != reviewRequested {
				t.Fatalf("pending request = %s (%s), %v; want answered (%s)", status, reason, err, reviewRequested)
			}
			checkReviewRequestRecovery(ctx, t, w, job, lf, prID, tt.ambiguousCommit, tt.missingEyes)
			// Finish the original review during the retry's backoff, and repair
			// the unreadable job so the database failure is transient.
			if err := st.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
				if _, err := tx.Exec(ctx, `UPDATE river_job SET state = 'completed', finalized_at = now(),
					args = jsonb_set(args, '{number}', to_jsonb($1::int))
					WHERE kind = 'review' AND args->>'account_id' = $2 AND args->>'repository_id' = $3
					AND args->>'head_sha' = $4`, number, account.ID(), repoID, head); err != nil {
					return err
				}
				if tt.status != "" {
					_, err := tx.Exec(ctx, `INSERT INTO reviews (account_id, pull_request_id, head_sha, status, finished_at)
						VALUES ($1, $2, $3, $4, clock_timestamp())`, account.ID(), prID, head, tt.status)
					return err
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			job.Attempt = 2
			if err := w.Work(ctx, job); err != nil {
				t.Fatalf("resume polling: %v", err)
			}
			var queued int
			if err := st.App().QueryRow(ctx, `SELECT count(*) FROM river_job
				WHERE kind = 'review' AND args->>'head_sha' = $1`, head).Scan(&queued); err != nil || queued != 1 {
				t.Fatalf("review jobs after retry = %d, %v; want 1", queued, err)
			}
			if got := lf.reactions[commentID]; got != tt.mark {
				t.Fatalf("settled reaction = %q, want %q", got, tt.mark)
			}
			if err := st.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
				return tx.QueryRow(ctx, `SELECT reason FROM followups WHERE pull_request_id = $1 AND comment_id = $2`,
					prID, commentID).Scan(&reason)
			}); err != nil || reason == reviewRequested {
				t.Fatalf("settled request = %q, %v; want a terminal reason", reason, err)
			}
			checkReviewRequestRedelivery(ctx, t, w, queue, svc, ingest.Request{File: file, Account: account}, job, lf, tt.mark)
		})
	}
}

func checkReviewRequestRecovery(ctx context.Context, t *testing.T, w *FollowUp, job *river.Job[jobs.FollowUpArgs],
	lf *reviewRequestForge, prID string, ambiguousCommit, missingEyes bool,
) {
	t.Helper()
	if ambiguousCommit {
		if err := w.Store.WithAccount(ctx, job.Args.AccountID, func(tx pgx.Tx) error {
			return store.RecordFollowup(ctx, tx, store.FollowupResult{AccountID: job.Args.AccountID, PullRequestID: prID,
				CommentID: job.Args.CommentID, Status: store.FollowupFailed, Reason: "commit response lost"})
		}); err != nil {
			t.Fatal(err)
		}
	}
	if missingEyes {
		delete(lf.reactions, job.Args.CommentID)
		job.Attempt++
		_, snoozed := errors.AsType[*river.JobSnoozeError](w.Work(ctx, job))
		if !snoozed || lf.reactions[job.Args.CommentID] != forge.ReactionEyes {
			t.Fatalf("pending retry: snoozed=%v, reaction=%q", snoozed, lf.reactions[job.Args.CommentID])
		}
	}
}

func cleanupReviewRequest(ctx context.Context, t *testing.T, w *FollowUp, prID, head string, args jobs.FollowUpArgs) {
	t.Helper()
	err := w.Store.WithAccount(ctx, args.AccountID, func(tx pgx.Tx) error {
		for _, query := range []string{
			`DELETE FROM followups WHERE pull_request_id = $1`,
			`DELETE FROM reviews WHERE pull_request_id = $1`,
			`DELETE FROM pull_requests WHERE id = $1`,
		} {
			if _, err := tx.Exec(ctx, query, prID); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `DELETE FROM river_job WHERE args->>'account_id' = $1 AND args->>'repository_id' = $2
			AND (args->>'head_sha' = $3 OR args->>'comment_id' = $4)`, args.AccountID, args.RepositoryID, head, fmt.Sprint(args.CommentID))
		return err
	})
	if err != nil {
		t.Errorf("clean up review request: %v", err)
	}
}

func checkReviewRequestRedelivery(
	ctx context.Context, t *testing.T, w *FollowUp, queue *river.Client[pgx.Tx], svc *ingest.Service,
	req ingest.Request, job *river.Job[jobs.FollowUpArgs], lf *reviewRequestForge, mark string,
) {
	t.Helper()
	args := job.Args
	// River's retention cleanup allows the webhook to create a fresh
	// job whose creation time excludes the original review.
	if _, err := w.Store.App().Exec(ctx, `UPDATE river_job SET state = 'completed', finalized_at = now() WHERE id = $1`, job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Store.App().Exec(ctx, `DELETE FROM river_job WHERE id = $1`, job.ID); err != nil {
		t.Fatal(err)
	}
	req.Event = webhook.Event{
		Kind: webhook.KindComment, Action: "created", Account: "onedr0p",
		Repository: &webhook.Repository{FullName: "onedr0p/home-ops", DefaultBranch: "main"},
		Comment:    &webhook.Comment{ID: args.CommentID, Number: args.Number, Author: "onedr0p", Body: "@kritika review"},
	}
	out, err := svc.Dispatch(ctx, req)
	if err != nil || out.Status != ingest.Enqueued {
		t.Fatalf("redelivery = %+v, %v; want enqueued", out, err)
	}
	redelivered, err := queue.Insert(ctx, args, nil)
	if err != nil || !redelivered.UniqueSkippedAsDuplicate || redelivered.Job.ID == job.ID {
		t.Fatalf("redelivered job = %+v, %v; want the fresh job from ingest", redelivered, err)
	}
	marked := len(lf.reacted)
	for _, attempt := range []int{1, 2} {
		redelivered.Job.Attempt = attempt
		if err := w.Work(ctx, &river.Job[jobs.FollowUpArgs]{JobRow: redelivered.Job, Args: args}); err != nil {
			t.Fatalf("redelivery attempt %d: %v", attempt, err)
		}
		if got := lf.reactions[args.CommentID]; got != mark || len(lf.reacted) != marked {
			t.Fatalf("redelivery changed settled reactions: mark=%q, reactions=%d; want %q and %d", got, len(lf.reacted), mark, marked)
		}
	}
	var reviews int
	if err := w.Store.App().QueryRow(ctx, `SELECT count(*) FROM river_job WHERE kind = 'review'
		AND args->>'account_id' = $1 AND args->>'repository_id' = $2 AND (args->>'number')::int = $3`,
		args.AccountID, args.RepositoryID, args.Number).Scan(&reviews); err != nil || reviews != 1 {
		t.Fatalf("review jobs = %d, %v; want one", reviews, err)
	}
	if !redelivered.Job.CreatedAt.After(job.CreatedAt) || len(lf.comments) != 1 {
		t.Fatal("redelivery did not create a later job, or the request got a reply")
	}
}
