package configfile

import (
	"strings"
	"testing"
)

func TestChatGPTProviders(t *testing.T) {
	setInstanceEnv(t)
	doc := strings.ReplaceAll(fileWithDefaults, "openrouter: { type: openrouter, apiKey: { env: TEST_PROVIDER_KEY } }", "openrouter: { type: chatgpt }")
	doc = strings.ReplaceAll(doc, "embedding: { model: openrouter/e1, dims: 8 }\n", "")
	doc = strings.ReplaceAll(doc, "  acme: {}", "  acme: { providers: { own: { type: chatgpt } } }")
	f, err := Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct{ name, key string }{{"openrouter", "openrouter"}, {"own", "github/acme/own"}} {
		t.Run(tt.name, func(t *testing.T) {
			p, ok := f.Provider(&f.Accounts[0], tt.name)
			if !ok || p.Type != ProviderChatGPT || p.ChatGPTSessionKey() != tt.key || p.APIKeyValue().Value() != "" {
				t.Fatalf("provider = %+v, %v", p, ok)
			}
		})
	}
	for _, tt := range []struct{ name, doc, want string }{
		{"API key", strings.ReplaceAll(doc, "type: chatgpt", "type: chatgpt, apiKey: { env: TEST_PROVIDER_KEY }"), "uses a stored sign-in"},
		{"pricing", strings.ReplaceAll(doc, "type: chatgpt", "type: chatgpt, pricing: { gpt-x: { input: 1, output: 2 } }"), "covered by a plan"},
		{"embedding", doc + "embedding: { model: openrouter/e1, dims: 8 }\n", "embedding.model"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Parse([]byte(tt.doc)); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %s", err, tt.want)
			}
		})
	}
	t.Run("environment can declare a disconnected provider", func(t *testing.T) {
		t.Setenv("KRITIKA_PROVIDERS_NAME", "plan")
		t.Setenv("KRITIKA_PROVIDERS_TYPE", "chatgpt")
		t.Setenv("KRITIKA_REVIEW_MODEL", "plan/gpt-x")
		f, err := Parse([]byte(minimal))
		if err != nil {
			t.Fatal(err)
		}
		p, ok := f.Provider(nil, "plan")
		if !ok || p.Type != ProviderChatGPT || p.ChatGPTSessionKey() != "plan" {
			t.Fatalf("environment provider = %+v, %v", p, ok)
		}
	})
}
