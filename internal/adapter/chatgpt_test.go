package adapter

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/home-operations/kritika/internal/chatgpt"
	"github.com/home-operations/kritika/internal/store"
)

// fakeSessions holds one session, or none, and counts the expiries asked
// of it.
type fakeSessions struct {
	session *store.ChatGPTSession
	err     error
	expired *int
}

func (f fakeSessions) ChatGPTSession(context.Context, string) (store.ChatGPTSession, bool, error) {
	if f.err != nil || f.session == nil {
		return store.ChatGPTSession{}, false, f.err
	}
	return *f.session, true, nil
}

func (f fakeSessions) ExpireChatGPTSession(context.Context, string) error {
	if f.expired != nil {
		*f.expired++
	}
	return nil
}

func TestPlanTokens(t *testing.T) {
	seed := chatgpt.Credentials{ClientID: "oaiapp_1", AccessToken: "at-seed", RefreshToken: "rt-seed"}
	live := store.ChatGPTSession{Credentials: chatgpt.Credentials{ClientID: "oaiapp_1", AccessToken: "at-live", RefreshToken: "rt-live"}}
	out := live
	out.SignedOutAt, out.SignedOutReason = time.Now(), "refresh_token_reused"
	for _, tt := range []struct {
		name     string
		sessions fakeSessions
		want     string
		wantErr  string
	}{
		{"the store's token", fakeSessions{session: &live}, "at-live", ""},
		{"the configured token until the leader seeds", fakeSessions{}, "at-seed", ""},
		{"a signed-out session", fakeSessions{session: &out}, "", "signed out (refresh_token_reused)"},
		{"the store unreachable", fakeSessions{err: errors.New("down")}, "", "down"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tok, err := planTokens{sessions: tt.sessions, seed: seed}.Token(t.Context())
			if tt.wantErr == "" && (err != nil || tok != tt.want) {
				t.Fatalf("Token = %q, %v, want %q", tok, err, tt.want)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("Token err = %v, want one containing %q", err, tt.wantErr)
			}
			if tt.name == "a signed-out session" && !errors.Is(err, chatgpt.ErrSignedOut) {
				t.Fatalf("err = %v, want ErrSignedOut", err)
			}
		})
	}
	t.Run("expire reaches the store", func(t *testing.T) {
		expired := 0
		planTokens{sessions: fakeSessions{session: &live, expired: &expired}, seed: seed}.Expire()
		if expired != 1 {
			t.Fatalf("expired %d times, want 1", expired)
		}
	})
}
