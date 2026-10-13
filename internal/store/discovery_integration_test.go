//go:build integration

package store

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/configfile"
)

func TestConfigTakesOverDiscoveryDefault(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	for _, tt := range []struct {
		name            string
		discoveredFirst bool
		choice          *bool
	}{
		{"config-first", false, nil},
		{"discovery-first", true, nil},
		{"admin-off", true, new(false)},
		{"admin-on", true, new(true)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := parse(t, twoAccounts)
			if err := s.ApplyConfig(ctx, f); err != nil {
				t.Fatal(err)
			}
			a, _ := f.Account(configfile.ForgeGitHub, "alpha")
			name := "alpha/" + tt.name
			id := configfile.RepositoryID(a.ID(), name)
			t.Cleanup(func() {
				if _, err := s.owner.Exec(context.Background(), `DELETE FROM repositories WHERE id = $1`, id); err != nil {
					t.Error(err)
				}
			})
			if tt.discoveredFirst {
				if err := s.WithAccount(ctx, a.ID(), func(tx pgx.Tx) error {
					if _, _, err := EnsureRepository(ctx, tx, a.ID(), ReachedRepository{FullName: name}); err != nil {
						return err
					}
					if tt.choice != nil {
						return TurnOn(ctx, tx, id, *tt.choice)
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			listed := parse(t, twoAccounts+"  "+name+": {}\n")
			for range 2 {
				if err := s.ApplyConfig(ctx, listed); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.WithAccount(ctx, a.ID(), func(tx pgx.Tx) error {
				if _, _, err := EnsureRepository(ctx, tx, a.ID(), ReachedRepository{FullName: name}); err != nil {
					return err
				}
				on, err := TurnedOn(ctx, tx, id)
				if err == nil && ((on == nil) != (tt.choice == nil) || (on != nil && tt.choice != nil && *on != *tt.choice)) {
					t.Errorf("config takeover got %v, want %v", on, tt.choice)
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
