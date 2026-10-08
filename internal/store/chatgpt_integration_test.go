//go:build integration

package store

import (
	"context"
	"testing"
	"time"

	"github.com/home-operations/kritika/internal/chatgpt"
)

func planRecord(client, access, refresh string) chatgpt.Credentials {
	return chatgpt.Credentials{
		ClientID: client, AccessToken: access, RefreshToken: refresh, TokenType: "Bearer", ExpiresIn: 3600,
		Scopes: []string{chatgpt.PlanScope}, SavedAt: time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC),
	}
}

func TestChatGPTSessions(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	seed := planRecord("oaiapp_store", "at-1", "rt-1")
	if _, ok, err := s.ChatGPTSession(ctx, seed.ClientID); err != nil || ok {
		t.Fatalf("before seeding: ok = %v, %v", ok, err)
	}
	if err := s.SeedChatGPTSession(ctx, seed); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.ChatGPTSession(ctx, seed.ClientID)
	if err != nil || !ok || got.Credentials.RefreshToken != "rt-1" || got.SignedOut() || !got.Credentials.SavedAt.Equal(seed.SavedAt) {
		t.Fatalf("seeded session = %+v, %v, %v", got, ok, err)
	}

	t.Run("a refresh replaces the set whose token it had", func(t *testing.T) {
		renewed := planRecord(seed.ClientID, "at-2", "rt-2")
		if replaced, err := s.RefreshChatGPTSession(ctx, renewed, "rt-1"); err != nil || !replaced {
			t.Fatalf("RefreshChatGPTSession = %v, %v", replaced, err)
		}
		stale := planRecord(seed.ClientID, "at-x", "rt-x")
		if replaced, err := s.RefreshChatGPTSession(ctx, stale, "rt-1"); err != nil || replaced {
			t.Fatalf("a refresh of a replaced token set = %v, %v; want it left alone", replaced, err)
		}
		got, _, _ := s.ChatGPTSession(ctx, seed.ClientID)
		if got.Credentials.RefreshToken != "rt-2" || got.Credentials.AccessToken != "at-2" {
			t.Fatalf("session = %+v", got.Credentials)
		}
	})
	t.Run("the same seed leaves the refreshed set alone", func(t *testing.T) {
		if err := s.SeedChatGPTSession(ctx, seed); err != nil {
			t.Fatal(err)
		}
		if got, _, _ := s.ChatGPTSession(ctx, seed.ClientID); got.Credentials.RefreshToken != "rt-2" {
			t.Fatalf("session = %+v, want the refreshed set", got.Credentials)
		}
	})
	t.Run("expire spends the access token", func(t *testing.T) {
		if err := s.ExpireChatGPTSession(ctx, seed.ClientID); err != nil {
			t.Fatal(err)
		}
		if got, _, _ := s.ChatGPTSession(ctx, seed.ClientID); got.Credentials.ExpiresIn != 0 || got.Credentials.RefreshToken != "rt-2" {
			t.Fatalf("session = %+v, want expires_in 0 and the tokens kept", got.Credentials)
		}
	})
	t.Run("a sign-out is recorded once and a new seed ends it", func(t *testing.T) { testChatGPTSignOut(t, s, seed) })
	t.Run("every session", func(t *testing.T) {
		other := planRecord("oaiapp_other", "at-o", "rt-o")
		if err := s.SeedChatGPTSession(ctx, other); err != nil {
			t.Fatal(err)
		}
		all, err := s.ChatGPTSessions(ctx)
		if err != nil || len(all) < 2 {
			t.Fatalf("ChatGPTSessions = %d sessions, %v", len(all), err)
		}
	})
}

func testChatGPTSignOut(t *testing.T, s *Store, seed chatgpt.Credentials) {
	ctx := context.Background()
	if err := s.SignOutChatGPTSession(ctx, seed.ClientID, "refresh_token_reused"); err != nil {
		t.Fatal(err)
	}
	got, _, _ := s.ChatGPTSession(ctx, seed.ClientID)
	if !got.SignedOut() || got.SignedOutReason != "refresh_token_reused" {
		t.Fatalf("session = %+v", got)
	}
	if err := s.SignOutChatGPTSession(ctx, seed.ClientID, "later"); err != nil {
		t.Fatal(err)
	}
	if again, _, _ := s.ChatGPTSession(ctx, seed.ClientID); !again.SignedOutAt.Equal(got.SignedOutAt) || again.SignedOutReason != "refresh_token_reused" {
		t.Fatalf("a second sign-out changed the record: %+v", again)
	}
	fresh := planRecord(seed.ClientID, "at-3", "rt-3")
	if err := s.SeedChatGPTSession(ctx, fresh); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := s.ChatGPTSession(ctx, seed.ClientID); got.SignedOut() || got.Credentials.RefreshToken != "rt-3" {
		t.Fatalf("session after a new seed = %+v", got)
	}
}
