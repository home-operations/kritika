// Package storetest opens the store on the integration tests' database,
// the one the test-integration task provisions.
package storetest

import (
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/store"
)

// Env is the environment variable key. It skips the test when the key is
// unset.
func Env(t testing.TB, key string) string {
	t.Helper()
	v := os.Getenv(key)
	if v == "" {
		t.Skipf("%s not set", key)
	}
	return v
}

// Open opens the store on KRITIKA_TEST_APP_URL and KRITIKA_TEST_OWNER_URL,
// migrated for the kritika_app and kritika_runner roles and closed when the
// test ends.
func Open(t testing.TB) *store.Store {
	t.Helper()
	ctx := t.Context()
	st, err := store.Open(ctx, store.Options{
		AppURL: Env(t, "KRITIKA_TEST_APP_URL"), OwnerURL: Env(t, "KRITIKA_TEST_OWNER_URL"),
		Logger: slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatalf("storetest: open: %v", err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(ctx, "kritika_app", "kritika_runner"); err != nil {
		t.Fatalf("storetest: migrate: %v", err)
	}
	return st
}

// Repositories records the repositories named, "owner/name", as the forge
// reports them to their accounts in f, turned on in the dashboard. So a
// test's reviews and indexing can run on them.
func Repositories(t testing.TB, st *store.Store, f *configfile.File, names ...string) {
	t.Helper()
	ctx := t.Context()
	for _, name := range names {
		owner, _, _ := strings.Cut(name, "/")
		var account *configfile.Account
		for i := range f.Accounts {
			if strings.EqualFold(f.Accounts[i].Name, owner) {
				account = &f.Accounts[i]
			}
		}
		if account == nil {
			t.Fatalf("storetest: no account serves %s", name)
		}
		if err := st.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
			id, _, err := store.EnsureRepository(ctx, tx, account.ID(), store.ReachedRepository{FullName: name})
			if err != nil {
				return err
			}
			return store.TurnOn(ctx, tx, id, true)
		}); err != nil {
			t.Fatalf("storetest: repository %s: %v", name, err)
		}
	}
}
