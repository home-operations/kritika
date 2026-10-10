package webapi

import (
	"testing"

	"github.com/home-operations/kritika/internal/configfile"
)

func TestChatGPTProviders(t *testing.T) {
	plan := configfile.Provider{Type: configfile.ProviderChatGPT}
	api := configfile.Provider{Type: configfile.ProviderOpenAI}
	for _, tt := range []struct {
		name        string
		global, own map[string]configfile.Provider
		want        int
	}{
		{"disabled", map[string]configfile.Provider{"api": api}, nil, 0},
		{"global plan", map[string]configfile.Provider{"plan": plan}, nil, 1},
		{"account plan", nil, map[string]configfile.Provider{"own": plan}, 1},
		{"both scopes", map[string]configfile.Provider{"plan": plan}, map[string]configfile.Provider{"own": plan}, 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			file := &configfile.File{Providers: tt.global}
			got := chatGPTProviders(file, &configfile.Account{Providers: tt.own})
			if len(got) != tt.want {
				t.Fatalf("providers = %v", got)
			}
			if len(file.Providers) != len(tt.global) {
				t.Fatal("provider discovery changed configuration")
			}
		})
	}
}
