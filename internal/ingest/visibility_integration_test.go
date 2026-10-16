//go:build integration

package ingest

import (
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/configfile/configfiletest"
	"github.com/home-operations/kritika/internal/store"
	"github.com/home-operations/kritika/internal/webhook"
)

// TestDispatchRecordsVisibilityBeforeJudging: a pull request webhook records
// what the forge says of a known repository, its visibility included,
// whether or not the pull request is then reviewed; a payload without a
// visibility is judged by the known one; and the poller's synthetic events,
// which carry what the store said when the poll read it, write nothing
// back.
func TestDispatchRecordsVisibilityBeforeJudging(t *testing.T) {
	svc, st, f := setupService(t)
	ctx := t.Context()
	skipPublic := configfiletest.Load(t, configYAML+"visibility:\n  public: { trigger: { exclude: [{ name: public, expr: 'true' }] } }\n")
	account, _ := f.Account(configfile.ForgeGitHub, "onedr0p")
	name := "onedr0p/goes-public"
	id := configfile.RepositoryID(account.ID(), name)
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
	// Known as private, and turned on in the dashboard.
	if err := st.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
		if _, _, err := store.EnsureRepository(ctx, tx, account.ID(), store.ReachedRepository{
			FullName: name, DefaultBranch: "main", Traits: &configfile.RepoTraits{Visibility: "private"},
		}); err != nil {
			return err
		}
		return store.TurnOn(ctx, tx, id, true)
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
	// The webhook says the repository is public now: the public entry's
	// filter skips the pull request, and the visibility is recorded anyway.
	if out, err := svc.Dispatch(ctx, request(skipPublic, event("opened", "public"))); err != nil || out != (Outcome{Status: Skipped, Reason: reasonFilter}) {
		t.Fatalf("Dispatch = %+v, %v; want skipped by the public entry's filter", out, err)
	}
	if got := visibility(); got != "public" {
		t.Fatalf("visibility = %q after a skipped pull request webhook, want public", got)
	}
	// A payload without a visibility is judged by the known one.
	if out, err := svc.Dispatch(ctx, request(skipPublic, event("synchronize", ""))); err != nil || out != (Outcome{Status: Skipped, Reason: reasonFilter}) {
		t.Fatalf("Dispatch = %+v, %v; want skipped by the public entry's filter from the known visibility", out, err)
	}
	if got := visibility(); got != "public" {
		t.Fatalf("visibility = %q after a payload without one, want public", got)
	}
	// Without the entry, the same pull request is reviewed.
	if out, err := svc.Dispatch(ctx, request(f, event("synchronize", ""))); err != nil || out.Status != Enqueued {
		t.Fatalf("Dispatch = %+v, %v; want enqueued without a public entry", out, err)
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
