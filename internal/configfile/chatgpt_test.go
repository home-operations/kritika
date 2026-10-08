package configfile

import (
	"slices"
	"strings"
	"testing"
)

const planRecord = `{"client_id":"oaiapp_1","access_token":"at-1","refresh_token":"rt-1","token_type":"Bearer","expires_in":3600,` +
	`"scopes":["openid","chatgpt.tokens.use.direct"],"saved_at":"2026-10-08T12:00:00Z"}`

func TestChatGPTProvider(t *testing.T) {
	t.Setenv("TEST_PRIVATE_KEY", "tok")
	t.Setenv("TEST_WEBHOOK_SECRET", "whsec")
	t.Setenv("TEST_PLAN", planRecord+"\n")
	t.Setenv("TEST_EMPTY", "")

	t.Run("the instance's", func(t *testing.T) {
		f, err := loadBytes(t, []byte("providers:\n  plan: { type: chatgpt, credentials: { env: TEST_PLAN } }\nreview: { model: plan/gpt-x }\n"+minimal))
		if err != nil {
			t.Fatal(err)
		}
		p := f.Providers["plan"]
		if c := p.ChatGPTCredentials(); p.Type != ProviderChatGPT || c.ClientID != "oaiapp_1" || c.AccessToken != "at-1" || c.RefreshToken != "rt-1" {
			t.Fatalf("provider = %+v, credentials = %+v", p, c)
		}
		if p.APIKeyValue().Value() != "" {
			t.Fatal("a chatgpt provider has no key")
		}
		if !slices.Contains(f.SecretEnv(), "TEST_PLAN") {
			t.Fatalf("SecretEnv = %v, want the credentials variable", f.SecretEnv())
		}
	})
	t.Run("an account's own", func(t *testing.T) {
		f, err := loadBytes(t, []byte(minimal+"accounts:\n  acme:\n    providers: { plan: { type: chatgpt, credentials: { env: TEST_PLAN } } }\n"))
		if err != nil {
			t.Fatal(err)
		}
		if p, ok := f.Provider(&f.Accounts[0], "plan"); !ok || p.ChatGPTCredentials().ClientID != "oaiapp_1" {
			t.Fatalf("Provider(acme, plan) = %+v, %v", p, ok)
		}
	})
	t.Run("from the environment", func(t *testing.T) {
		t.Setenv("KRITIKA_PROVIDERS_NAME", "chatgpt")
		t.Setenv("KRITIKA_PROVIDERS_CREDENTIALS", planRecord)
		f, err := loadBytes(t, []byte(minimal))
		if err != nil {
			t.Fatal(err)
		}
		if p := f.Providers["chatgpt"]; p.Type != ProviderChatGPT || p.ChatGPTCredentials().ClientID != "oaiapp_1" {
			t.Fatalf("provider = %+v", p)
		}
	})
	for _, tt := range []struct{ name, yaml, want string }{
		{"without credentials", "providers:\n  plan: { type: chatgpt }\n", "providers.plan.credentials: reference must set env"},
		{"with a key", "providers:\n  plan: { type: chatgpt, credentials: { env: TEST_PLAN }, apiKey: { env: TEST_WEBHOOK_SECRET } }\n",
			"a chatgpt provider takes credentials, not apiKey"},
		{"credentials on another type", "providers:\n  p: { type: openai, apiKey: { env: TEST_WEBHOOK_SECRET }, credentials: { env: TEST_PLAN } }\n",
			"credentials is for a chatgpt provider"},
		{"an empty record", "providers:\n  plan: { type: chatgpt, credentials: { env: TEST_EMPTY } }\n", "providers.plan.credentials: chatgpt: credentials:"},
		{"a record that is not a sign-in", "providers:\n  plan: { type: chatgpt, credentials: { env: TEST_WEBHOOK_SECRET } }\n", "providers.plan.credentials:"},
		{"as the embedder", "providers:\n  plan: { type: chatgpt, credentials: { env: TEST_PLAN } }\nembedding: { model: plan/e, dims: 8 }\n",
			"embedding.model names a chatgpt provider"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := loadBytes(t, []byte(tt.yaml+minimal)); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Parse = %v, want an error containing %q", err, tt.want)
			}
		})
	}
}
