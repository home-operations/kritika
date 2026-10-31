//go:build integration

package store

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// TestAPITokens: a token is looked up by its value until it expires or is
// deleted. It records its use at most once a minute. Only its owner, or
// anyone when no owner is given, deletes it.
func TestAPITokens(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	now := time.Now().Truncate(time.Microsecond)
	suffix := now.Format(time.RFC3339Nano)
	origin := "oidc:https://idp.example"
	owner, err := s.UpsertIdentity(ctx, SignInIdentity{Provider: "oidc", Origin: origin, Subject: "tokens-a-" + suffix, DisplayName: "Ada"})
	if err != nil {
		t.Fatalf("UpsertIdentity: %v", err)
	}
	other, err := s.UpsertIdentity(ctx, SignInIdentity{Provider: "oidc", Origin: origin, Subject: "tokens-b-" + suffix})
	if err != nil {
		t.Fatalf("UpsertIdentity: %v", err)
	}
	expired := now.Add(-time.Minute)
	base := APIToken{User: owner, Identity: SignInIdentity{Provider: "oidc", Origin: origin},
		Grant: SessionGrant{Role: RoleMember, Accounts: []string{"github/org-1"}, Key: "k"}, CreatedAt: now.Add(-time.Hour)}
	var live, old, liveID, oldID string
	if err := s.WithAccount(ctx, "", func(tx pgx.Tx) error {
		lived, stale := base, base
		lived.Name, stale.Name, stale.ExpiresAt, stale.CreatedAt = "laptop", "old", &expired, now.Add(-2*time.Hour)
		var err error
		if live, liveID, err = CreateAPIToken(ctx, tx, lived); err != nil {
			return err
		}
		old, oldID, err = CreateAPIToken(ctx, tx, stale)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(live, APITokenPrefix) || live == old {
		t.Fatalf("tokens %q and %q; want two distinct, prefixed %q", live, old, APITokenPrefix)
	}

	got, err := s.LookupAPIToken(ctx, live, now)
	if err != nil || got.ID != liveID || got.User.ID != owner.ID || got.User.DisplayName != "Ada" || got.Name != "laptop" ||
		got.Grant.Role != RoleMember || len(got.Grant.Accounts) != 1 || got.Grant.Key != "k" || got.Identity.Origin != origin ||
		got.LastUsedAt == nil || !got.LastUsedAt.Equal(now) {
		t.Fatalf("LookupAPIToken = %+v, %v", got, err)
	}
	checkLookupRefusals(t, s, live, old, now)
	checkTokenOwnership(t, s, owner.ID, other.ID, liveID, oldID)
	if _, err := s.LookupAPIToken(ctx, live, now); !errors.Is(err, ErrSession) {
		t.Fatalf("LookupAPIToken of a deleted token = %v, want ErrSession", err)
	}
}

// checkLookupRefusals makes sure that a lookup within a minute of the last
// use leaves the use alone. It also makes sure that an expired, unknown or
// empty value finds no token.
func checkLookupRefusals(t *testing.T, s *Store, live, old string, now time.Time) {
	t.Helper()
	if again, err := s.LookupAPIToken(t.Context(), live, now.Add(30*time.Second)); err != nil || !again.LastUsedAt.Equal(now) {
		t.Fatalf("a lookup within the minute = %+v, %v; want the use left at %v", again.LastUsedAt, err, now)
	}
	// Two lookups that both read the same last use write one of them, and
	// a later time is never replaced by an earlier one.
	var id string
	if err := s.App().QueryRow(t.Context(), `SELECT id FROM api_tokens WHERE token_hash = $1`, tokenHash(live)).Scan(&id); err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct {
		at   time.Duration
		want bool
	}{{2 * time.Minute, true}, {2 * time.Minute, false}, {90 * time.Second, false}, {150 * time.Second, false}, {3 * time.Minute, true}} {
		if marked, err := markAPITokenUsed(t.Context(), s.App(), id, now.Add(step.at)); err != nil || marked != step.want {
			t.Fatalf("marking a use at +%v = %v, %v; want %v", step.at, marked, err, step.want)
		}
	}
	for name, value := range map[string]string{"expired": old, "unknown": APITokenPrefix + "nothing", "empty": ""} {
		if _, err := s.LookupAPIToken(t.Context(), value, now); !errors.Is(err, ErrSession) {
			t.Errorf("LookupAPIToken of an %s token = %v, want ErrSession", name, err)
		}
	}
}

// checkTokenOwnership makes sure that a user lists their own tokens alone,
// newest first, and that another user cannot delete them. It also makes sure
// that their owner, or anyone when no owner is given, deletes them.
func checkTokenOwnership(t *testing.T, s *Store, owner, other, liveID, oldID string) {
	t.Helper()
	ctx := t.Context()
	if err := s.WithAccount(ctx, "", func(tx pgx.Tx) error {
		mine, err := ListAPITokens(ctx, tx, owner)
		if err != nil || len(mine) != 2 || mine[0].ID != liveID || mine[1].ID != oldID {
			t.Fatalf("ListAPITokens of the owner = %+v, %v; want both, the newest first", mine, err)
		}
		if theirs, err := ListAPITokens(ctx, tx, other); err != nil || len(theirs) != 0 {
			t.Fatalf("ListAPITokens of another user = %+v, %v", theirs, err)
		}
		if _, err := DeleteAPIToken(ctx, tx, liveID, other); !errors.Is(err, ErrNotFound) {
			t.Fatalf("DeleteAPIToken by another user = %v, want ErrNotFound", err)
		}
		gone, err := DeleteAPIToken(ctx, tx, liveID, owner)
		if err != nil || gone.Name != "laptop" || gone.User.ID != owner {
			t.Fatalf("DeleteAPIToken by its owner = %+v, %v", gone, err)
		}
		_, err = DeleteAPIToken(ctx, tx, oldID, "")
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
