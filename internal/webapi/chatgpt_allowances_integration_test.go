//go:build integration

package webapi

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/home-operations/kritika/internal/chatgpt"
	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/configfile/configfiletest"
)

func testChatGPTAllowances(t *testing.T, e *apiEnv) {
	ctx := t.Context()
	file := configfiletest.Load(t, integrationConfig+`
providers:
  shared-plan: { type: chatgpt }
accounts:
  wa:
    providers:
      own-plan: { type: chatgpt }
`)
	e.srv.current.Set(file)
	t.Cleanup(func() { e.srv.current.Set(e.file) })
	wa, _ := file.Account(configfile.ForgeGitHub, "wa")
	provider, ok := file.Provider(wa, "own-plan")
	if !ok {
		t.Fatal("account's provider is missing")
	}
	key := provider.ChatGPTSessionKey()
	initial, err := e.st.PrepareChatGPTSession(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	credentials := initial.Credentials
	credentials.ClientID, credentials.Subject = "oaiapp_webquota", "private-subject"
	credentials.AccessToken, credentials.RefreshToken, credentials.IDToken = "private-access", "private-refresh", "private-id"
	credentials.SavedAt, credentials.ExpiresIn = time.Now().UTC(), 3600
	credentials.Scopes = []string{chatgpt.PlanScope}
	if err := e.st.ConnectChatGPTSession(ctx, key, credentials, ""); err != nil {
		t.Fatal(err)
	}
	if err := e.st.UpdateChatGPTAllowances(ctx, key, []chatgpt.Allowance{{
		LimitID: "codex", ObservedAt: time.Now().UTC(), Primary: &chatgpt.AllowanceWindow{UsedPercent: 25},
	}}); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/accounts/github/wa/chatgpt/allowances"
	status, body := e.getBody("member-a", path)
	var providers []ChatGPTProviderUsage
	if err := json.Unmarshal(body, &providers); status != 200 || err != nil || len(providers) != 2 {
		t.Fatalf("allowances = %d %s, %v", status, body, err)
	}
	if providers[0].Provider != "own-plan" || !providers[0].Connected || len(providers[0].Allowances) != 1 ||
		providers[0].Allowances[0].Primary.UsedPercent != 25 || providers[1].Provider != "shared-plan" || providers[1].Connected {
		t.Errorf("provider allowances = %+v", providers)
	}
	for _, private := range []string{"private-access", "private-refresh", "private-id", "private-subject", "oaiapp_webquota", "credentials"} {
		if bytes.Contains(body, []byte(private)) {
			t.Errorf("allowance response leaked %q", private)
		}
	}
	if status, body := e.getBody("member-b", path); status != 404 {
		t.Fatalf("other account can read allowances: %d %s", status, body)
	}
	status, body = e.getBody("member-b", "/api/v1/accounts/github/wb/chatgpt/allowances")
	if err := json.Unmarshal(body, &providers); status != 200 || err != nil || len(providers) != 1 || providers[0].Provider != "shared-plan" {
		t.Fatalf("account B providers = %d %s, %v", status, body, err)
	}
	status, body = e.getBody("member-a", "/api/v1/accounts")
	if status != 200 || !bytes.Contains(body, []byte(`"chatgptEnabled":true`)) {
		t.Fatalf("enabled summary = %d %s", status, body)
	}
	e.srv.current.Set(e.file)
	status, body = e.getBody("member-a", path)
	if status != 200 || string(bytes.TrimSpace(body)) != "[]" {
		t.Fatalf("disabled providers still visible = %d %s", status, body)
	}
	status, body = e.getBody("member-a", "/api/v1/accounts")
	if status != 200 || bytes.Contains(body, []byte(`"chatgptEnabled"`)) {
		t.Fatalf("disabled summary = %d %s", status, body)
	}
}
