//go:build integration

package worker

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/home-operations/kritika/internal/chatgpt"
	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/configfile/configfiletest"
	"github.com/home-operations/kritika/internal/store/storetest"
)

const chatGPTConfigYAML = `
providers:
  fresh: { type: chatgpt, credentials: { env: TEST_PLAN_FRESH } }
  due: { type: chatgpt, credentials: { env: TEST_PLAN_DUE } }
review:
  model: due/gpt-x
apps:
  acme-bot:
    accounts: [acme]
    clientId: Iv1.x
    privateKey: { env: TEST_PEM }
    webhookSecret: { env: TEST_SECRET }
accounts:
  acme:
    providers:
      own: { type: chatgpt, credentials: { env: TEST_PLAN_OWN } }
`

func planJSON(t *testing.T, client, refresh string, savedAt time.Time) string {
	t.Helper()
	raw, err := json.Marshal(chatgpt.Credentials{
		ClientID: client, AccessToken: "at-" + client, RefreshToken: refresh, TokenType: "Bearer", ExpiresIn: 3600,
		Scopes: []string{chatgpt.PlanScope}, SavedAt: savedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// TestChatGPTRefresher: a pass seeds the configured plans, renews the sets
// near expiry with the token endpoint, signs out the one it refuses, and
// leaves the fresh, the unconfigured and the renewed-elsewhere alone.
func TestChatGPTRefresher(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	t.Setenv("TEST_PEM", "pem")
	t.Setenv("TEST_SECRET", "s")
	t.Setenv("TEST_PLAN_FRESH", planJSON(t, "oaiapp_fresh", "rt-fresh", now))
	t.Setenv("TEST_PLAN_DUE", planJSON(t, "oaiapp_due", "rt-due", now.Add(-56*time.Minute)))
	t.Setenv("TEST_PLAN_OWN", planJSON(t, "oaiapp_own", "rt-own", now.Add(-2*time.Hour)))
	file := configfiletest.Load(t, chatGPTConfigYAML)

	var refreshes atomic.Int32
	tokens := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshes.Add(1)
		_ = r.ParseForm()
		w.Header().Set("Content-Type", "application/json")
		switch r.PostForm.Get("refresh_token") {
		case "rt-due":
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at-due-2", "refresh_token": "rt-due-2", "token_type": "Bearer", "expires_in": 3600})
		case "rt-own":
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "refresh_token_reused"})
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	t.Cleanup(tokens.Close)

	// A plan no configuration names, seeded by an earlier one, is left to
	// expire.
	stale := chatgpt.Credentials{ClientID: "oaiapp_stale", AccessToken: "at", RefreshToken: "rt-stale", ExpiresIn: 0, Scopes: []string{chatgpt.PlanScope}, SavedAt: now}
	if err := st.SeedChatGPTSession(ctx, stale); err != nil {
		t.Fatal(err)
	}
	r := &ChatGPTRefresher{
		Store: st, Current: configfile.NewCurrent(file), Logger: slog.New(slog.DiscardHandler),
		Client: tokens.Client(), TokenURL: tokens.URL, now: func() time.Time { return now },
	}
	if err := r.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if refreshes.Load() != 2 {
		t.Fatalf("the token endpoint got %d refreshes, want 2: the due set and the account's own", refreshes.Load())
	}
	want := map[string]struct {
		refresh   string
		signedOut bool
	}{
		"oaiapp_fresh": {"rt-fresh", false},
		"oaiapp_due":   {"rt-due-2", false},
		"oaiapp_own":   {"rt-own", true},
		"oaiapp_stale": {"rt-stale", false},
	}
	for client, w := range want {
		s, ok, err := st.ChatGPTSession(ctx, client)
		if err != nil || !ok {
			t.Fatalf("%s: session = %v, %v", client, ok, err)
		}
		if s.Credentials.RefreshToken != w.refresh || s.SignedOut() != w.signedOut {
			t.Fatalf("%s: session = %+v, want refresh token %q, signed out %v", client, s, w.refresh, w.signedOut)
		}
		if w.signedOut && !strings.Contains(s.SignedOutReason, "refresh_token_reused") {
			t.Fatalf("%s: reason = %q", client, s.SignedOutReason)
		}
	}
	if s, _, _ := st.ChatGPTSession(ctx, "oaiapp_due"); !s.Credentials.SavedAt.Equal(now) || s.Credentials.AccessToken != "at-due-2" {
		t.Fatalf("the renewed set = %+v", s.Credentials)
	}

	t.Run("a second pass renews nothing", func(t *testing.T) {
		if err := r.Refresh(ctx); err != nil {
			t.Fatal(err)
		}
		if refreshes.Load() != 2 {
			t.Fatalf("the token endpoint got %d refreshes, want 2 still", refreshes.Load())
		}
	})
	t.Run("a passing failure is reported and tried again", func(t *testing.T) {
		r.now = func() time.Time { return now.Add(57 * time.Minute) }
		err := r.Refresh(ctx)
		if err == nil || !strings.Contains(err.Error(), "oaiapp_due") {
			t.Fatalf("Refresh = %v, want the 500 reported for the due set", err)
		}
		if s, _, _ := st.ChatGPTSession(ctx, "oaiapp_due"); s.Credentials.RefreshToken != "rt-due-2" || s.SignedOut() {
			t.Fatalf("session after a failed renewal = %+v", s)
		}
	})
}
