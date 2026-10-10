//go:build integration

package store_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/home-operations/kritika/internal/chatgpt"
	"github.com/home-operations/kritika/internal/store/storetest"
)

func TestChatGPTAllowanceLifecycle(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	key := "quota-" + uuid.NewString()
	check := func(connected bool, want map[string]float64) {
		t.Helper()
		gotConnected, got, err := st.ChatGPTAllowances(ctx, key)
		if err != nil || gotConnected != connected || len(got) != len(want) {
			t.Fatalf("allowances = %v, %+v, %v; want %v, %v", gotConnected, got, err, connected, want)
		}
		for id, used := range want {
			if got[id].Primary == nil || got[id].Primary.UsedPercent != used {
				t.Errorf("%s = %+v; want %v used", id, got[id], used)
			}
		}
	}
	check(false, nil)
	registration, err := st.PrepareChatGPTSession(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	check(false, nil)
	credentials := registration.Credentials
	credentials.ClientID, credentials.Subject = "oaiapp_quota", "user-quota"
	credentials.AccessToken, credentials.RefreshToken, credentials.IDToken = "at-1", "rt-1", "id-1"
	credentials.Scopes = []string{chatgpt.PlanScope}
	credentials.SavedAt, credentials.ExpiresIn = time.Now().UTC(), 3600
	if err := st.ConnectChatGPTSession(ctx, key, credentials, ""); err != nil {
		t.Fatal(err)
	}
	check(true, nil)
	now := time.Now().UTC()
	update := func(id string, used float64, observed time.Time) {
		t.Helper()
		if err := st.UpdateChatGPTAllowances(ctx, key, []chatgpt.Allowance{{
			LimitID: id, Primary: &chatgpt.AllowanceWindow{UsedPercent: used}, ObservedAt: observed,
		}}); err != nil {
			t.Fatal(err)
		}
	}
	update("codex", 20, now)
	update("codex_other", 30, now)
	update("codex", 10, now.Add(-time.Second))
	check(true, map[string]float64{"codex": 20, "codex_other": 30})
	update("codex", 40, now.Add(time.Second))
	renewed := credentials
	renewed.AccessToken, renewed.RefreshToken = "at-2", "rt-2"
	if changed, err := st.RefreshChatGPTSession(ctx, key, renewed, credentials.RefreshToken); err != nil || !changed {
		t.Fatalf("refresh = %v, %v", changed, err)
	}
	// A step that started on the replaced token still reports the account's quota.
	update("codex", 45, now.Add(2*time.Second))
	check(true, map[string]float64{"codex": 45, "codex_other": 30})
	if err := st.SignOutChatGPTSession(ctx, key, renewed.RefreshToken, "invalid_grant"); err != nil {
		t.Fatal(err)
	}
	update("codex", 99, now.Add(3*time.Second))
	check(false, nil)
	if err := st.ConnectChatGPTSession(ctx, key, renewed, credentials.ClientID); err != nil {
		t.Fatal(err)
	}
	update("codex", 50, now.Add(4*time.Second))
	check(true, map[string]float64{"codex": 50})
	if err := st.ConnectChatGPTSession(ctx, key, renewed, credentials.ClientID); err != nil {
		t.Fatal(err)
	}
	check(true, nil)
}
