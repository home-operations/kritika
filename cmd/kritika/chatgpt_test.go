package main

import (
	"strings"
	"testing"

	"github.com/home-operations/kritika/internal/configfile"
)

func TestChatGPTProvider(t *testing.T) {
	f := &configfile.File{
		Providers: map[string]configfile.Provider{"plan": {Type: configfile.ProviderChatGPT}, "api": {Type: configfile.ProviderOpenAI}},
		Accounts: []configfile.Account{{Forge: configfile.ForgeGitHub, Name: "acme", Providers: map[string]configfile.Provider{
			"own": {Type: configfile.ProviderChatGPT},
		}}},
	}
	for _, tt := range []struct {
		name string
		args []string
		key  string
		want string
	}{
		{"instance", []string{"plan"}, "plan", ""},
		{"account", []string{"github/acme", "own"}, "github/acme/own", ""},
		{"shared", []string{"github/acme", "plan"}, "plan", ""},
		{"unknown account", []string{"github/missing", "own"}, "", "not configured"},
		{"missing forge", []string{"acme", "own"}, "", "must be <forge/account>"},
		{"API key provider", []string{"api"}, "", "type chatgpt"},
		{"unknown provider", []string{"missing"}, "", "type chatgpt"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p, err := chatGPTProvider(f, tt.args)
			if tt.want != "" {
				if err == nil || !strings.Contains(err.Error(), tt.want) {
					t.Fatalf("err = %v, want %s", err, tt.want)
				}
				return
			}
			if err != nil || p.ChatGPTSessionKey() != tt.key {
				t.Fatalf("key = %s, err = %v", p.ChatGPTSessionKey(), err)
			}
		})
	}
}
