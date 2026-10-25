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

func TestRenamePreservesEnablement(t *testing.T) {
	svc, st, f := setupService(t)
	for _, tt := range []struct {
		name        string
		choice      *bool
		destination *bool
		transfer    bool
		want        *bool
	}{
		{name: "no-choice"},
		{name: "enabled", choice: new(true), want: new(true)},
		{name: "disabled", choice: new(false), want: new(false)},
		{name: "destination-choice", choice: new(true), destination: new(false), want: new(false)},
		{name: "transfer-needs-opt-in", choice: new(true), transfer: true, want: new(false)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := t.Context()
			a, _ := f.Account(configfile.ForgeGitHub, "onedr0p")
			to := a
			if tt.transfer {
				to, _ = f.Account(configfile.ForgeGitHub, "home-operations")
			}
			old, name := a.Name+"/rename-old", to.Name+"/rename-new"
			for _, seed := range []struct {
				account *configfile.Account
				name    string
				choice  *bool
			}{{a, old, tt.choice}, {to, name, tt.destination}} {
				id := configfile.RepositoryID(seed.account.ID(), seed.name)
				t.Cleanup(func() {
					ctx := context.Background()
					if err := st.WithAccount(ctx, seed.account.ID(), func(tx pgx.Tx) error {
						_, err := tx.Exec(ctx, `DELETE FROM repositories WHERE id = $1`, id)
						return err
					}); err != nil {
						t.Error(err)
					}
				})
				if seed.name == name && seed.choice == nil {
					continue
				}
				if err := st.WithAccount(ctx, seed.account.ID(), func(tx pgx.Tx) error {
					if _, _, err := store.EnsureRepository(ctx, tx, seed.account.ID(), store.ReachedRepository{FullName: seed.name}); err != nil {
						return err
					}
					if seed.choice != nil {
						return store.TurnOn(ctx, tx, id, *seed.choice)
					}
					_, err := tx.Exec(ctx, `UPDATE repositories SET turned_on = NULL WHERE id = $1`, id)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			action := "renamed"
			if tt.transfer {
				action = "transferred"
			}
			for range 2 {
				if _, err := svc.Dispatch(ctx, request(f, webhook.Event{
					Kind: webhook.KindRepository, Action: action, Repository: repo(name), Previous: old,
				})); err != nil {
					t.Fatal(err)
				}
			}
			if err := st.WithAccount(ctx, to.ID(), func(tx pgx.Tx) error {
				known, err := store.RepositoryTraits(ctx, tx, configfile.RepositoryID(to.ID(), name), nil)
				on := known.TurnedOn
				if err == nil && ((on == nil) != (tt.want == nil) || (on != nil && tt.want != nil && *on != *tt.want)) {
					t.Errorf("renamed repository enablement = %v, want %v", on, tt.want)
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
