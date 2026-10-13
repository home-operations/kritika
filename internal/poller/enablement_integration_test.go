//go:build integration

package poller

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/configfile/configfiletest"
	"github.com/home-operations/kritika/internal/forge"
	"github.com/home-operations/kritika/internal/ingest"
	"github.com/home-operations/kritika/internal/store"
	"github.com/home-operations/kritika/internal/store/storetest"
	"github.com/home-operations/kritika/internal/webhook"
)

func TestEnablementCatchesUpOpenPullRequests(t *testing.T) {
	for _, previousPoll := range []bool{false, true} {
		name := "first-poll"
		if previousPoll {
			name = "past-cursor"
		}
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			st := storetest.Open(t)
			t.Setenv("TEST_PEM", "pem")
			t.Setenv("TEST_SECRET", "s")
			doc := strings.Replace(configYAML, "repositories:\n  onedr0p/home-ops: {}\n", "", 1)
			file := configfiletest.Load(t, strings.ReplaceAll(doc, "onedr0p", name))
			if err := st.ApplyConfig(ctx, file); err != nil {
				t.Fatal(err)
			}
			a := &file.Accounts[0]
			fullName := a.Name + "/new"
			id := configfile.RepositoryID(a.ID(), fullName)
			t.Cleanup(func() {
				ctx := context.Background()
				if err := st.WithAccount(ctx, a.ID(), func(tx pgx.Tx) error {
					if _, err := tx.Exec(ctx, `DELETE FROM river_job WHERE args->>'repository_id' = $1`, id); err != nil {
						return err
					}
					if _, err := tx.Exec(ctx, `DELETE FROM pull_requests WHERE repository_id = $1`, id); err != nil {
						return err
					}
					_, err := tx.Exec(ctx, `DELETE FROM repositories WHERE id = $1`, id)
					return err
				}); err != nil {
					t.Error(err)
				}
			})
			queue, err := river.NewClient(riverpgxv5.New(st.App()), &river.Config{})
			if err != nil {
				t.Fatal(err)
			}
			svc := ingest.NewService(st, queue)
			pr := forge.OpenPullRequest{Number: 1, State: "open", HeadSHA: "waiting", BaseRef: "main",
				UpdatedAt: time.Now().Add(-48 * time.Hour), DefaultBranch: "main"}
			out, err := svc.Dispatch(ctx, ingest.Request{File: file, Account: a, Event: webhook.Event{
				Kind: webhook.KindPullRequest, Action: "opened", Repository: &webhook.Repository{FullName: fullName, DefaultBranch: "main"},
				PullRequest: &pr.PullRequest,
			}})
			if err != nil || out.Status != ingest.Skipped || out.Reason != "disabled" {
				t.Fatalf("webhook while off = %+v, %v", out, err)
			}
			p := &Poller{Store: st, Current: configfile.NewCurrent(file), Dispatcher: svc,
				Forges: &forges{f: &listForge{prs: []forge.OpenPullRequest{pr}}}, Logger: slog.New(slog.DiscardHandler)}
			in := file.ConnectionFor(a)
			if previousPoll {
				if n, err := p.Poll(ctx, file, a, in); err != nil || n != 0 {
					t.Fatalf("poll while off = %d, %v", n, err)
				}
			}
			if err := st.WithAccount(ctx, a.ID(), func(tx pgx.Tx) error { return store.TurnOn(ctx, tx, id, true) }); err != nil {
				t.Fatal(err)
			}
			for _, want := range []int{1, 0} {
				if n, err := p.Poll(ctx, file, a, in); err != nil || n != want {
					t.Fatalf("poll after enablement = %d, %v; want %d", n, err, want)
				}
				var jobs int
				if err := st.App().QueryRow(ctx, `SELECT count(*) FROM river_job WHERE kind = 'review' AND args->>'repository_id' = $1`, id).Scan(&jobs); err != nil {
					t.Fatal(err)
				}
				if jobs != 1 {
					t.Fatalf("queued %d reviews after enablement, want 1", jobs)
				}
			}
		})
	}
}
