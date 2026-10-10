//go:build integration

package store_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/home-operations/kritika/internal/chatgpt"
	"github.com/home-operations/kritika/internal/store/storetest"
)

func TestChatGPTSessionLifecycle(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	key := "plan-" + uuid.NewString()
	initial, err := st.PrepareChatGPTSession(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if initial.Credentials.HostID == "" || initial.Credentials.ClientID != "" {
		t.Fatalf("initial registration = %+v", initial)
	}
	current := initial.Credentials
	current.ClientID, current.Subject = "oaiapp_1", "user-1"
	current.AccessToken, current.RefreshToken, current.IDToken = "at-1", "rt-1", "id-1"
	current.ExpiresIn, current.SavedAt = 3600, time.Now().UTC()
	current.Scopes = []string{chatgpt.PlanScope}
	if err := st.ConnectChatGPTSession(ctx, key, current, ""); err != nil {
		t.Fatal(err)
	}
	repeated, err := st.PrepareChatGPTSession(ctx, key)
	if err != nil || repeated.Credentials.HostID != current.HostID || repeated.Credentials.RefreshToken != current.RefreshToken {
		t.Fatalf("repeated preparation = %+v, %v", repeated, err)
	}
	if err := st.ConnectChatGPTSession(ctx, key, current, ""); err == nil {
		t.Fatal("a stale first registration replaced the connected client")
	}
	newer := current
	newer.AccessToken, newer.RefreshToken = "at-2", "rt-2"
	if err := st.ConnectChatGPTSession(ctx, key, newer, current.ClientID); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name string
		run  func() error
	}{
		{"old refresh failure", func() error { return st.SignOutChatGPTSession(ctx, key, current.RefreshToken, "invalid_grant") }},
		{"old unauthorized response", func() error { return st.ExpireChatGPTSession(ctx, key, current.AccessToken) }},
		{"old successful refresh", func() error {
			changed, err := st.RefreshChatGPTSession(ctx, key, current, current.RefreshToken)
			if changed {
				t.Error("a stale refresh overwrote the new sign-in")
			}
			return err
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.run(); err != nil {
				t.Fatal(err)
			}
			s, ok, err := st.ChatGPTSession(ctx, key)
			if err != nil || !ok || s.SignedOut() || s.Credentials.RefreshToken != newer.RefreshToken || s.Credentials.ExpiresIn != 3600 {
				t.Fatalf("new sign-in after stale update = %+v, %v, %v", s, ok, err)
			}
		})
	}
	t.Run("runner cannot read tokens", func(t *testing.T) {
		var allowed bool
		if err := st.App().QueryRow(ctx, `SELECT has_table_privilege('kritika_runner', 'chatgpt_sessions', 'SELECT')`).Scan(&allowed); err != nil {
			t.Fatal(err)
		}
		if allowed {
			t.Fatal("runner role has access to provider credentials")
		}
	})
}

func TestChatGPTSessionRenewal(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	key := "plan-" + uuid.NewString()
	initial, err := st.PrepareChatGPTSession(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	current := initial.Credentials
	current.ClientID, current.Subject = "oaiapp_1", "user-1"
	current.AccessToken, current.RefreshToken, current.IDToken = "at-1", "rt-1", "id-1"
	current.ExpiresIn, current.SavedAt = 3600, time.Now().UTC()
	current.Scopes = []string{chatgpt.PlanScope}
	if err := st.ConnectChatGPTSession(ctx, key, current, ""); err != nil {
		t.Fatal(err)
	}
	if err := st.ExpireChatGPTSession(ctx, key, current.AccessToken); err != nil {
		t.Fatal(err)
	}
	expired, _, err := st.ChatGPTSession(ctx, key)
	if err != nil || !expired.Credentials.Due(time.Now()) {
		t.Fatalf("rejected token was not due: %+v, %v", expired, err)
	}
	renewed := current
	renewed.AccessToken, renewed.RefreshToken = "at-2", "rt-2"
	if changed, err := st.RefreshChatGPTSession(ctx, key, renewed, current.RefreshToken); err != nil || !changed {
		t.Fatalf("refresh = %v, %v", changed, err)
	}
	if err := st.SignOutChatGPTSession(ctx, key, renewed.RefreshToken, "invalid_grant"); err != nil {
		t.Fatal(err)
	}
	signedOut, err := st.PrepareChatGPTSession(ctx, key)
	if err != nil || !signedOut.SignedOut() || signedOut.Credentials.AccessToken != "" || signedOut.Credentials.RefreshToken != "" ||
		signedOut.Credentials.IDToken != "" || signedOut.Credentials.ClientID != current.ClientID || signedOut.Credentials.HostID != current.HostID {
		t.Fatalf("signed out registration = %+v, %v", signedOut, err)
	}
	if err := st.ConnectChatGPTSession(ctx, key, renewed, current.ClientID); err != nil {
		t.Fatal(err)
	}
	s, _, err := st.ChatGPTSession(ctx, key)
	if err != nil || s.SignedOut() || s.Credentials.AccessToken != renewed.AccessToken {
		t.Fatalf("reconnected session = %+v, %v", s, err)
	}
}
