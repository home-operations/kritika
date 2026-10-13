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

func TestDiscoveredRepositoryWaitsForEnablement(t *testing.T) {
	svc, st, f := setupService(t)
	ctx := t.Context()
	a, _ := f.Account(configfile.ForgeGitHub, "onedr0p")
	var lastJob int64
	if err := st.App().QueryRow(ctx, `SELECT coalesce(max(id), 0) FROM river_job`).Scan(&lastJob); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := st.App().Exec(context.Background(), `DELETE FROM river_job WHERE id > $1`, lastJob); err != nil {
			t.Error(err)
		}
	})
	f.Defaults.Enabled = new(true)
	a.Enabled = new(true)
	for _, ev := range []webhook.Event{
		{Kind: webhook.KindPullRequest, Action: "opened", PullRequest: &webhook.PullRequest{Number: 1, HeadSHA: "new", State: "open"}},
		{Kind: webhook.KindComment, Action: "created", Comment: &webhook.Comment{ID: 1, Number: 1, Body: "@bot-ross review"}},
		{Kind: webhook.KindThread, Action: "resolved", Thread: &webhook.Thread{Number: 1, CommentID: 1}},
		{Kind: webhook.KindPush, Push: &webhook.Push{Ref: "refs/heads/main", After: "new"}},
	} {
		t.Run(string(ev.Kind), func(t *testing.T) {
			ev.Repository = repo("onedr0p/discovered-" + string(ev.Kind))
			for range 2 {
				out, err := svc.Dispatch(ctx, request(f, ev))
				if err != nil || out != (Outcome{Status: Skipped, Reason: reasonDisabled}) {
					t.Fatalf("dispatch before enablement = %+v, %v", out, err)
				}
			}
			if err := st.WithAccount(ctx, a.ID(), func(tx pgx.Tx) error {
				r, err := store.FindRepo(ctx, tx, ev.Repository.FullName)
				if err != nil {
					return err
				}
				if !r.Enabled || r.TurnedOn == nil || *r.TurnedOn {
					t.Fatalf("discovered repository should be reachable but turned off: %+v", r)
				}
				var n int
				if err := tx.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE args->>'repository_id' = $1`, r.ID).Scan(&n); err != nil {
					return err
				}
				if n != 0 {
					t.Fatalf("queued %d jobs before enablement", n)
				}
				return store.TurnOn(ctx, tx, r.ID, true)
			}); err != nil {
				t.Fatal(err)
			}
			out, err := svc.Dispatch(ctx, request(f, ev))
			if err != nil || (out.Status != Enqueued && out.Reason != reasonNotIndexed) {
				t.Fatalf("dispatch after enablement = %+v, %v", out, err)
			}
		})
	}
}
