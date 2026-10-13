//go:build integration

package worker

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/configfile/configfiletest"
	"github.com/home-operations/kritika/internal/store"
	"github.com/home-operations/kritika/internal/store/storetest"
)

func TestOnboardingWaitsForEnablement(t *testing.T) {
	ctx := t.Context()
	st := storetest.Open(t)
	t.Setenv("TEST_PEM", "pem")
	t.Setenv("TEST_SECRET", "s3cret")
	doc := strings.Replace(configYAML, "repositories:\n  onedr0p/home-ops: {}\n", "", 1)
	file := configfiletest.Load(t, strings.ReplaceAll(doc, "onedr0p", "onboard-opt-in"))
	file.Defaults.Enabled = new(true)
	if err := st.ApplyConfig(ctx, file); err != nil {
		t.Fatal(err)
	}
	a := &file.Accounts[0]
	var id string
	if err := st.WithAccount(ctx, a.ID(), func(tx pgx.Tx) error {
		var err error
		id, _, err = store.EnsureRepository(ctx, tx, a.ID(), store.ReachedRepository{FullName: "onboard-opt-in/new"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		if err := st.WithAccount(ctx, a.ID(), func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `DELETE FROM river_job WHERE args->>'repository_id' = $1`, id); err != nil {
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
	inFlight, err := st.OnboardingInFlight(ctx)
	if err != nil {
		t.Fatal(err)
	}
	file.Run.OnboardWindow = inFlight + 1
	o := &Onboarder{Store: st, Queue: queue, Current: configfile.NewCurrent(file), Logger: slog.New(slog.DiscardHandler)}
	for _, on := range []bool{false, true} {
		if on {
			if err := st.WithAccount(ctx, a.ID(), func(tx pgx.Tx) error { return store.TurnOn(ctx, tx, id, true) }); err != nil {
				t.Fatal(err)
			}
		}
		if err := o.Offer(ctx); err != nil {
			t.Fatal(err)
		}
		var n int
		if err := st.App().QueryRow(ctx, `SELECT count(*) FROM river_job WHERE kind = 'index' AND args->>'repository_id' = $1`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		want := 0
		if on {
			want = 1
		}
		if n != want {
			t.Fatalf("enabled=%v: got %d onboarding jobs with an open slot, want %d", on, n, want)
		}
	}
}
