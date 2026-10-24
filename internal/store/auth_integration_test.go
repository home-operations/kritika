//go:build integration

package store

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// TestSessionGrant: a session keeps the grant its sign-in decided, and a
// grant without a role is refused.
func TestSessionGrant(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	now := time.Now()
	id := SignInIdentity{Provider: "github", Origin: "github:https://github.com",
		Subject: "store-test-" + now.Format(time.RFC3339Nano), Login: "x"}
	acct, err := s.UpsertIdentity(ctx, id)
	if err != nil {
		t.Fatalf("UpsertIdentity: %v", err)
	}
	for _, g := range []SessionGrant{
		{Role: RoleAdmin, Key: "k1"},
		{Role: RoleMember, AllAccounts: true, Key: "k2"},
		{Role: RoleMember, Accounts: []string{"github/org-1", "github/user-1"}, Key: "k3"},
	} {
		token, err := s.CreateSession(ctx, acct.ID, "github", "github:https://github.com", g, now, now.Add(time.Hour))
		if err != nil {
			t.Fatalf("CreateSession(%+v): %v", g, err)
		}
		sess, err := s.LookupSession(ctx, token, now)
		if err != nil {
			t.Fatalf("LookupSession: %v", err)
		}
		got := sess.Grant
		if got.Role != g.Role || got.AllAccounts != g.AllAccounts || got.Key != g.Key || len(got.Accounts) != len(g.Accounts) {
			t.Fatalf("grant = %+v, want %+v", got, g)
		}
		for i := range g.Accounts {
			if got.Accounts[i] != g.Accounts[i] {
				t.Fatalf("grant = %+v, want %+v", got, g)
			}
		}
	}
	if _, err := s.CreateSession(ctx, acct.ID, "github", "github:https://github.com", SessionGrant{}, now, now.Add(time.Hour)); err == nil {
		t.Fatal("a grant without a role was stored")
	}

	// A user's settings come back with every session of theirs, and a
	// later sign-in, which refreshes the profile, leaves them alone.
	token, err := s.CreateSession(ctx, acct.ID, "github", "github:https://github.com", SessionGrant{Role: RoleMember, Key: "k4"}, now, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if sess, err := s.LookupSession(ctx, token, now); err != nil || sess.User.Settings != (UserSettings{}) {
		t.Fatalf("a new user's settings = %+v, %v; want none", sess.User.Settings, err)
	}
	want := UserSettings{TimeZone: "Europe/Amsterdam", Clock: "24", Theme: "dark"}
	if err := s.SetUserSettings(ctx, acct.ID, want); err != nil {
		t.Fatalf("SetUserSettings: %v", err)
	}
	if _, err := s.UpsertIdentity(ctx, id); err != nil {
		t.Fatalf("UpsertIdentity again: %v", err)
	}
	if sess, err := s.LookupSession(ctx, token, now); err != nil || sess.User.Settings != want {
		t.Fatalf("settings = %+v, %v; want %+v", sess.User.Settings, err, want)
	}
	if err := s.SetUserSettings(ctx, "00000000-0000-0000-0000-000000000000", want); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetUserSettings of no user = %v, want ErrNotFound", err)
	}
}

func TestLoginStateConsumedOnce(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	now := time.Now()
	state, err := s.CreateLoginState(ctx, LoginState{Provider: "gh", Nonce: "n", PKCEVerifier: "v", ReturnTo: "#/x"}, "browser", now)
	if err != nil {
		t.Fatalf("CreateLoginState: %v", err)
	}
	if _, err := s.ConsumeLoginState(ctx, state, "another-browser", now); !errors.Is(err, ErrLoginState) {
		t.Fatalf("ConsumeLoginState from another browser = %v, want ErrLoginState", err)
	}
	ls, err := s.ConsumeLoginState(ctx, state, "browser", now)
	if err != nil || ls != (LoginState{Provider: "gh", Nonce: "n", PKCEVerifier: "v", ReturnTo: "#/x"}) {
		t.Fatalf("ConsumeLoginState = %+v, %v", ls, err)
	}
	if _, err := s.ConsumeLoginState(ctx, state, "browser", now); !errors.Is(err, ErrLoginState) {
		t.Fatalf("second ConsumeLoginState = %v, want ErrLoginState", err)
	}
}

// Unauthenticated sign-in starts each leave a row; past the cap they are
// refused until rows expire, rather than growing the table without bound.
func TestCreateLoginStateCap(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	now := time.Now()
	t.Cleanup(func() {
		_, _ = s.owner.Exec(context.Background(), `DELETE FROM login_states WHERE provider = 'cap-test'`)
	})
	if _, err := s.owner.Exec(ctx, `INSERT INTO login_states (state_hash, provider, nonce, pkce_verifier, expires_at, browser_hash)
		SELECT sha256(('cap-' || g)::bytea), 'cap-test', 'n', 'v', $1, sha256('b'::bytea)
		FROM generate_series(1, $2::int - (SELECT count(*)::int FROM login_states WHERE expires_at > $3)) g`,
		now.Add(time.Minute), MaxLoginStates, now); err != nil {
		t.Fatal(err)
	}
	ls := LoginState{Provider: "cap-test", Nonce: "n", PKCEVerifier: "v"}
	if _, err := s.CreateLoginState(ctx, ls, "browser", now); !errors.Is(err, ErrLoginStatesFull) {
		t.Fatalf("CreateLoginState at the cap = %v, want ErrLoginStatesFull", err)
	}
	// Once they expire the rows no longer count.
	if _, err := s.CreateLoginState(ctx, ls, "browser", now.Add(2*time.Minute)); err != nil {
		t.Fatalf("CreateLoginState after expiry = %v", err)
	}
}

// TestListUsers: users are listed with their sign-ins and the sessions
// still live, the most recently signed in first, and signing one out
// everywhere ends their sessions, expired ones included, but keeps when
// they last signed in.
func TestListUsers(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	now := time.Now().Truncate(time.Microsecond)
	suffix := now.Format(time.RFC3339Nano)
	origin := "github:https://github.com"
	ada, err := s.UpsertIdentity(ctx, SignInIdentity{Provider: "github", Origin: origin, Subject: "list-users-a-" + suffix, Login: "ada"})
	if err != nil {
		t.Fatalf("UpsertIdentity: %v", err)
	}
	bob, err := s.UpsertIdentity(ctx, SignInIdentity{Provider: "oidc", Origin: "oidc:https://idp.example", Subject: "list-users-b-" + suffix, Login: "bob"})
	if err != nil {
		t.Fatalf("UpsertIdentity: %v", err)
	}
	// A session that expired before now, then a live one: each sign-in
	// sweeps only the sessions expired by its own time.
	if _, err := s.CreateSession(ctx, ada.ID, "github", origin, SessionGrant{Role: RoleMember, Accounts: []string{"github/org-1"}, Key: "k"},
		now.Add(-2*time.Hour), now.Add(-30*time.Minute)); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err := s.CreateSession(ctx, ada.ID, "github", origin, SessionGrant{Role: RoleAdmin, Key: "k"}, now.Add(-time.Hour), now.Add(time.Hour)); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	// A sign-in that commits after a later one leaves the later time.
	if _, err := s.CreateSession(ctx, ada.ID, "github", origin, SessionGrant{Role: RoleMember, Key: "k"},
		now.Add(-90*time.Minute), now.Add(-45*time.Minute)); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	a, b := listTwo(t, s, now, ada.ID, bob.ID)
	if a.LastSignInAt == nil || !a.LastSignInAt.Equal(now.Add(-time.Hour)) || len(a.Sessions) != 1 || a.Sessions[0].Grant.Role != RoleAdmin ||
		len(a.Identities) != 1 || a.Identities[0].Login != "ada" || a.Identities[0].Provider != "github" {
		t.Fatalf("ada = %+v; want her last sign-in, her live admin session alone and her login", a)
	}
	if b.LastSignInAt != nil || len(b.Sessions) != 0 || len(b.Identities) != 1 || b.Identities[0].Login != "bob" {
		t.Fatalf("bob = %+v; want no sign-in or session and his login", b)
	}

	if ended := endSessions(t, s, ada.ID); ended != 3 {
		t.Fatalf("DeleteUserSessions ended %d sessions, want 3", ended)
	}
	if a, _ = listTwo(t, s, now, ada.ID, bob.ID); len(a.Sessions) != 0 || a.LastSignInAt == nil || !a.LastSignInAt.Equal(now.Add(-time.Hour)) {
		t.Fatalf("ada signed out = %+v; want no session and her last sign-in kept", a)
	}
}

// listTwo returns users a and b as ListUsers lists them at now, failing
// unless it lists both, a first.
func listTwo(t *testing.T, s *Store, now time.Time, a, b string) (UserSummary, UserSummary) {
	t.Helper()
	users, err := s.ListUsers(t.Context(), now)
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	ia := slices.IndexFunc(users, func(u UserSummary) bool { return u.ID == a })
	ib := slices.IndexFunc(users, func(u UserSummary) bool { return u.ID == b })
	if ia < 0 || ib < 0 || ia > ib {
		t.Fatalf("ListUsers placed a at %d and b at %d; want both, a first", ia, ib)
	}
	return users[ia], users[ib]
}

// endSessions signs the user out everywhere, as an admin would, after
// checking GetUser finds them and no one else, and returns how many
// sessions it ended.
func endSessions(t *testing.T, s *Store, userID string) int64 {
	t.Helper()
	ctx := t.Context()
	var ended int64
	if err := s.WithAccount(ctx, "", func(tx pgx.Tx) error {
		if _, err := GetUser(ctx, tx, "00000000-0000-0000-0000-000000000000"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("GetUser of no user = %v, want ErrNotFound", err)
		}
		if u, err := GetUser(ctx, tx, userID); err != nil || u.ID != userID {
			t.Fatalf("GetUser = %+v, %v", u, err)
		}
		var err error
		ended, err = DeleteUserSessions(ctx, tx, userID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return ended
}
