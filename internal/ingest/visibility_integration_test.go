//go:build integration

package ingest

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/store"
	"github.com/home-operations/kritika/internal/webhook"
)

// TestDispatchRecordsVisibilityBeforeJudging: a pull request webhook records
// what the forge says of a known repository, its visibility included,
// whether or not the pull request is then reviewed; a payload without a
// visibility keeps the known one; and the poller's synthetic events, which
// carry what the store said when the poll read it, write nothing back.
func TestDispatchRecordsVisibilityBeforeJudging(t *testing.T) {
	svc, st, f := setupService(t)
	ctx := t.Context()
	account, _ := f.Account(configfile.ForgeGitHub, "onedr0p")
	name := "onedr0p/goes-public"
	id := configfile.RepositoryID(account.ID(), name)
	turnOn := func(ctx context.Context, on bool) {
		t.Helper()
		if err := st.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error { return store.TurnOn(ctx, tx, id, on) }); err != nil {
			t.Fatal(err)
		}
	}
	// The packages after this one share the database: the poller's tests
	// poll every repository that runs, and the worker's work queued jobs.
	t.Cleanup(func() {
		ctx := context.Background()
		turnOn(ctx, false)
		if _, err := st.App().Exec(ctx, `DELETE FROM river_job WHERE args->>'repository_id' = $1`, id); err != nil {
			t.Error(err)
		}
	})
	visibility := func() string {
		t.Helper()
		var v string
		if err := st.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT visibility FROM repositories WHERE id = $1`, id).Scan(&v)
		}); err != nil {
			t.Fatal(err)
		}
		return v
	}
	// Known as private, and off, as a newly discovered repository is.
	if err := st.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
		_, _, err := store.EnsureRepository(ctx, tx, account.ID(), store.ReachedRepository{
			FullName: name, DefaultBranch: "main", Traits: &configfile.RepoTraits{Visibility: "private"},
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	pr := &webhook.PullRequest{Number: 4242, Title: "t", Author: "someone", State: "open", HeadRef: "f", HeadSHA: "4242a", BaseRef: "main"}
	event := func(action, visibility string) webhook.Event {
		return webhook.Event{
			Kind: webhook.KindPullRequest, Action: action, Delivery: "visibility-" + action + "-" + visibility,
			Repository: &webhook.Repository{FullName: name, DefaultBranch: "main", Visibility: visibility}, PullRequest: pr,
		}
	}
	// The webhook says the repository is public now: the pull request is
	// skipped, the repository being off, and the visibility is recorded
	// all the same.
	if out, err := svc.Dispatch(ctx, request(f, event("opened", "public"))); err != nil || out != (Outcome{Status: Skipped, Reason: reasonDisabled}) {
		t.Fatalf("Dispatch = %+v, %v; want skipped, the repository being off", out, err)
	}
	if got := visibility(); got != "public" {
		t.Fatalf("visibility = %q after a skipped pull request webhook, want public", got)
	}
	// Turned on, a payload without a visibility is reviewed, and keeps the
	// known one.
	turnOn(ctx, true)
	if out, err := svc.Dispatch(ctx, request(f, event("synchronize", ""))); err != nil || out.Status != Enqueued {
		t.Fatalf("Dispatch = %+v, %v; want enqueued", out, err)
	}
	if got := visibility(); got != "public" {
		t.Fatalf("visibility = %q after a payload without one, want public", got)
	}
	// A poll that read the row before the webhook carries private, and
	// does not undo it.
	if _, err := svc.Dispatch(ctx, request(f, event(ActionPoll, "private"))); err != nil {
		t.Fatal(err)
	}
	if got := visibility(); got != "public" {
		t.Fatalf("visibility = %q after a synthetic poll event, want public", got)
	}
	// The poll's synthetic push carries the same stale traits.
	push := webhook.Event{
		Kind: webhook.KindPush, Action: ActionPoll, Delivery: "visibility-poll-push",
		Repository: &webhook.Repository{FullName: name, DefaultBranch: "main", Visibility: "private"},
		Push:       &webhook.Push{Ref: "refs/heads/main", After: "abc4242"},
	}
	if _, err := svc.Dispatch(ctx, request(f, push)); err != nil {
		t.Fatal(err)
	}
	if got := visibility(); got != "public" {
		t.Fatalf("visibility = %q after a synthetic push event, want public", got)
	}
}
