package configfile

import (
	"maps"
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
	t.Run("the parsed maps carry the session keys", func(t *testing.T) {
		if f.Providers["openrouter"].ChatGPTSessionKey() != "openrouter" || f.Accounts[0].Providers["own"].ChatGPTSessionKey() != "github/acme/own" {
			t.Fatalf("keys = %q, %q", f.Providers["openrouter"].ChatGPTSessionKey(), f.Accounts[0].Providers["own"].ChatGPTSessionKey())
		}
	})
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

func TestFileChatGPTProviders(t *testing.T) {
	plan := Provider{Type: ProviderChatGPT}
	api := Provider{Type: ProviderOpenAI}
	for _, tt := range []struct {
		name        string
		global, own map[string]Provider
		want        map[string]string
	}{
		{"disabled", map[string]Provider{"api": api}, nil, map[string]string{}},
		{"global plan", map[string]Provider{"plan": plan}, nil, map[string]string{"plan": "plan"}},
		{"account plan", nil, map[string]Provider{"own": plan}, map[string]string{"own": "github/acme/own"}},
		{"both scopes", map[string]Provider{"plan": plan, "api": api}, map[string]Provider{"own": plan}, map[string]string{"plan": "plan", "own": "github/acme/own"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := &File{Providers: tt.global}
			got := f.ChatGPTProviders(&Account{Forge: ForgeGitHub, Name: "acme", Providers: tt.own})
			keys := map[string]string{}
			for name, p := range got {
				keys[name] = p.ChatGPTSessionKey()
			}
			if !maps.Equal(keys, tt.want) {
				t.Fatalf("providers = %v, want %v", keys, tt.want)
			}
			if len(f.Providers) != len(tt.global) {
				t.Fatal("provider discovery changed configuration")
			}
		})
	}
	if got := (&File{Providers: map[string]Provider{"plan": plan}}).ChatGPTProviders(nil); len(got) != 1 {
		t.Fatalf("instance providers = %v", got)
	}
}
