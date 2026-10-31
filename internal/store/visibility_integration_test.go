//go:build integration

package store

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/configfile"
)

// TestEnsureRepositoryVisibility: a report that says what a repository's
// visibility is records it. One that does not, an installation event's
// or an older forge's, keeps the known one.
func TestEnsureRepositoryVisibility(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	f := parse(t, twoAccounts)
	if err := s.ApplyConfig(ctx, f); err != nil {
		t.Fatal(err)
	}
	listRepos(t, s, "alpha", "alpha/one", "alpha/two")
	a, _ := f.Account(configfile.ForgeGitHub, "alpha")
	name := "alpha/visible"
	id := configfile.RepositoryID(a.ID(), name)
	t.Cleanup(func() {
		if _, err := s.owner.Exec(context.Background(), `DELETE FROM repositories WHERE id = $1`, id); err != nil {
			t.Error(err)
		}
	})
	visibility := func() string {
		t.Helper()
		var v string
		if err := s.WithAccount(ctx, a.ID(), func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT visibility FROM repositories WHERE id = $1`, id).Scan(&v)
		}); err != nil {
			t.Fatal(err)
		}
		return v
	}
	for _, step := range []struct {
		name   string
		traits *configfile.RepoTraits
		want   string
	}{
		{"named by an installation event", nil, ""},
		{"listed as public", &configfile.RepoTraits{Visibility: "public"}, "public"},
		{"named without a visibility", &configfile.RepoTraits{}, "public"},
		{"named by an installation event again", nil, "public"},
		{"made private", &configfile.RepoTraits{Visibility: "private"}, "private"},
	} {
		if err := s.WithAccount(ctx, a.ID(), func(tx pgx.Tx) error {
			_, _, err := EnsureRepository(ctx, tx, a.ID(), ReachedRepository{FullName: name, Traits: step.traits})
			return err
		}); err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		if got := visibility(); got != step.want {
			t.Fatalf("%s: visibility = %q, want %q", step.name, got, step.want)
		}
	}
}

// TestListReposPublic: the public kind lists the public repositories not
// archived, forks among them.
func TestListReposPublic(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	f := parse(t, twoAccounts)
	if err := s.ApplyConfig(ctx, f); err != nil {
		t.Fatal(err)
	}
	listRepos(t, s, "alpha", "alpha/one", "alpha/two")
	a, _ := f.Account(configfile.ForgeGitHub, "alpha")
	repos := map[string]configfile.RepoTraits{
		"alpha/visible-plain":    {Visibility: "public"},
		"alpha/visible-fork":     {Visibility: "public", Fork: true},
		"alpha/visible-archived": {Visibility: "public", Archived: true},
		"alpha/visible-private":  {Visibility: "private"},
		"alpha/visible-unknown":  {},
	}
	t.Cleanup(func() {
		for name := range repos {
			if _, err := s.owner.Exec(context.Background(), `DELETE FROM repositories WHERE id = $1`, configfile.RepositoryID(a.ID(), name)); err != nil {
				t.Error(err)
			}
		}
	})
	var listed []string
	if err := s.WithAccount(ctx, a.ID(), func(tx pgx.Tx) error {
		for name, traits := range repos {
			if _, _, err := EnsureRepository(ctx, tx, a.ID(), ReachedRepository{FullName: name, Traits: &traits}); err != nil {
				return err
			}
		}
		rows, _, err := ListRepos(ctx, tx, RepoFilter{Kind: RepoPublic}, Page{Limit: 100})
		for _, r := range rows {
			listed = append(listed, r.FullName)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]bool{
		"alpha/visible-plain": true, "alpha/visible-fork": true,
		"alpha/visible-archived": false, "alpha/visible-private": false, "alpha/visible-unknown": false,
	} {
		if got := slices.Contains(listed, name); got != want {
			t.Errorf("%s listed as public: %v, want %v (listed %v)", name, got, want, listed)
		}
	}
}

// TestRepositoryTraitsWritesOnlyChanges: a webhook's report of a known
// repository is recorded when it changes the row and leaves it unstamped
// when it does not. It keeps the known visibility when the payload names
// none. A nil report reads without writing.
func TestRepositoryTraitsWritesOnlyChanges(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	f := parse(t, twoAccounts)
	if err := s.ApplyConfig(ctx, f); err != nil {
		t.Fatal(err)
	}
	listRepos(t, s, "alpha", "alpha/one", "alpha/two")
	a, _ := f.Account(configfile.ForgeGitHub, "alpha")
	name := "alpha/visible-stamped"
	id := configfile.RepositoryID(a.ID(), name)
	t.Cleanup(func() {
		if _, err := s.owner.Exec(context.Background(), `DELETE FROM repositories WHERE id = $1`, id); err != nil {
			t.Error(err)
		}
	})
	updatedAt := func() time.Time {
		t.Helper()
		var at time.Time
		if err := s.WithAccount(ctx, a.ID(), func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT updated_at FROM repositories WHERE id = $1`, id).Scan(&at)
		}); err != nil {
			t.Fatal(err)
		}
		return at
	}
	traits := func(reported *configfile.RepoTraits) configfile.RepoTraits {
		t.Helper()
		var got configfile.RepoTraits
		if err := s.WithAccount(ctx, a.ID(), func(tx pgx.Tx) error {
			var err error
			got, err = RepositoryTraits(ctx, tx, id, reported)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return got
	}
	if err := s.WithAccount(ctx, a.ID(), func(tx pgx.Tx) error {
		_, err := RepositoryTraits(ctx, tx, id, &configfile.RepoTraits{Visibility: "public"})
		return err
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an unknown repository: %v, want ErrNotFound", err)
	}
	if err := s.WithAccount(ctx, a.ID(), func(tx pgx.Tx) error {
		_, _, err := EnsureRepository(ctx, tx, a.ID(), ReachedRepository{FullName: name, Traits: &configfile.RepoTraits{Visibility: "public"}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	public := configfile.RepoTraits{Visibility: "public", TurnedOn: new(false)}
	before := updatedAt()
	if got := traits(&configfile.RepoTraits{Visibility: "public"}); *got.TurnedOn != false || got.Visibility != "public" {
		t.Fatalf("traits = %+v after an unchanged report, want %+v", got, public)
	}
	if at := updatedAt(); !at.Equal(before) {
		t.Fatalf("updated_at = %s after an unchanged report, want unchanged %s", at, before)
	}
	if got := traits(&configfile.RepoTraits{Archived: true}); !got.Archived || got.Visibility != "public" {
		t.Fatalf("traits = %+v after a report without a visibility, want archived and still public", got)
	}
	if at := updatedAt(); !at.After(before) {
		t.Fatalf("updated_at = %s after a report that archived the repository, want later than %s", at, before)
	}
	before = updatedAt()
	if got := traits(nil); !got.Archived || got.Visibility != "public" {
		t.Fatalf("traits = %+v read back, want archived and public", got)
	}
	if at := updatedAt(); !at.Equal(before) {
		t.Fatalf("updated_at = %s after a nil report, want unchanged %s", at, before)
	}
}

// TestRegisterRepositoriesLeavesNewerTraits: a listing taken before a
// webhook wrote the row does not undo what the webhook said. One taken
// after it does.
func TestRegisterRepositoriesLeavesNewerTraits(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	f := parse(t, twoAccounts)
	if err := s.ApplyConfig(ctx, f); err != nil {
		t.Fatal(err)
	}
	listRepos(t, s, "alpha", "alpha/one", "alpha/two")
	a, _ := f.Account(configfile.ForgeGitHub, "alpha")
	name := "alpha/visible-listed"
	id := configfile.RepositoryID(a.ID(), name)
	t.Cleanup(func() {
		if _, err := s.owner.Exec(context.Background(), `DELETE FROM repositories WHERE id = $1`, id); err != nil {
			t.Error(err)
		}
	})
	report := func(traits configfile.RepoTraits) {
		t.Helper()
		if err := s.WithAccount(ctx, a.ID(), func(tx pgx.Tx) error {
			_, _, err := EnsureRepository(ctx, tx, a.ID(), ReachedRepository{FullName: name, DefaultBranch: "main", Traits: &traits})
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	known := func() (configfile.RepoTraits, string) {
		t.Helper()
		var traits configfile.RepoTraits
		var branch string
		if err := s.WithAccount(ctx, a.ID(), func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT archived, fork, visibility, default_branch FROM repositories WHERE id = $1`, id).
				Scan(&traits.Archived, &traits.Fork, &traits.Visibility, &branch)
		}); err != nil {
			t.Fatal(err)
		}
		return traits, branch
	}
	report(configfile.RepoTraits{Visibility: "private"})
	listedAt, err := s.Now(ctx)
	if err != nil {
		t.Fatal(err)
	}
	stale := []ReachedRepository{{FullName: name, DefaultBranch: "master", Traits: &configfile.RepoTraits{Visibility: "private", Fork: true}}}
	// The webhook lands after the listing was taken and before it is
	// registered.
	report(configfile.RepoTraits{Visibility: "public"})
	if _, _, err := s.RegisterRepositories(ctx, a.ID(), stale, listedAt); err != nil {
		t.Fatal(err)
	}
	if traits, branch := known(); traits != (configfile.RepoTraits{Visibility: "public"}) || branch != "main" {
		t.Fatalf("traits = %+v, branch %q after a stale listing, want the webhook's public and main", traits, branch)
	}
	if listedAt, err = s.Now(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.RegisterRepositories(ctx, a.ID(), stale, listedAt); err != nil {
		t.Fatal(err)
	}
	if traits, branch := known(); traits != (configfile.RepoTraits{Visibility: "private", Fork: true}) || branch != "master" {
		t.Fatalf("traits = %+v, branch %q after a fresh listing, want the listing's", traits, branch)
	}
}

// TestRegisterRepositoriesLeavesNewerDisable: a repository that a webhook
// disabled after a listing was taken, its transaction begun before it,
// stays disabled when that listing is registered. Its index sweep's clock
// is kept. A listing taken since enables it again, whatever else was
// written to it meanwhile.
func TestRegisterRepositoriesLeavesNewerDisable(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	f := parse(t, twoAccounts)
	if err := s.ApplyConfig(ctx, f); err != nil {
		t.Fatal(err)
	}
	listRepos(t, s, "alpha", "alpha/one", "alpha/two")
	a, _ := f.Account(configfile.ForgeGitHub, "alpha")
	name := "alpha/removed-listed"
	id := configfile.RepositoryID(a.ID(), name)
	t.Cleanup(func() {
		if _, err := s.owner.Exec(context.Background(), `DELETE FROM repositories WHERE id = $1`, id); err != nil {
			t.Error(err)
		}
	})
	listing := []ReachedRepository{{FullName: name, DefaultBranch: "main"}}
	now := func() time.Time {
		t.Helper()
		at, err := s.Now(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return at
	}
	register := func(listedAt time.Time) {
		t.Helper()
		if _, _, err := s.RegisterRepositories(ctx, a.ID(), listing, listedAt); err != nil {
			t.Fatal(err)
		}
	}
	state := func() (enabled, disabledAt, stoppedAt bool) {
		t.Helper()
		if err := s.owner.QueryRow(ctx, `SELECT enabled, disabled_at IS NOT NULL, stopped_at IS NOT NULL FROM repositories WHERE id = $1`, id).
			Scan(&enabled, &disabledAt, &stoppedAt); err != nil {
			t.Fatal(err)
		}
		return enabled, disabledAt, stoppedAt
	}
	register(now())
	// The webhook's transaction begins, the listing is taken, and then the
	// webhook disables the repository.
	var listedAt time.Time
	if err := s.WithAccount(ctx, a.ID(), func(tx pgx.Tx) error {
		listedAt = now()
		return DisableForgeRepositories(ctx, tx, a.ID(), id)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.owner.Exec(ctx, `UPDATE repositories SET stopped_at = now() WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	register(listedAt)
	if enabled, disabledAt, stoppedAt := state(); enabled || !disabledAt || !stoppedAt {
		t.Fatalf("after a stale listing: enabled %v, disabled_at set %v, stopped_at set %v; want the webhook's disable kept",
			enabled, disabledAt, stoppedAt)
	}
	// An admin's choice, made after the next listing was taken, is not a
	// disable.
	listedAt = now()
	if err := s.WithAccount(ctx, a.ID(), func(tx pgx.Tx) error {
		return TurnOn(ctx, tx, id, true)
	}); err != nil {
		t.Fatal(err)
	}
	register(listedAt)
	if enabled, disabledAt, stoppedAt := state(); !enabled || disabledAt || stoppedAt {
		t.Fatalf("after a fresh listing: enabled %v, disabled_at set %v, stopped_at set %v; want it enabled again",
			enabled, disabledAt, stoppedAt)
	}
}
