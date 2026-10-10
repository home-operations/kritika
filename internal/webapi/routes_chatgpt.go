package webapi

import (
	"maps"
	"net/http"
	"slices"

	"github.com/home-operations/kritika/internal/chatgpt"
	"github.com/home-operations/kritika/internal/configfile"
)

// ChatGPTProviderUsage is a configured provider's connection and quota data.
type ChatGPTProviderUsage struct {
	Provider   string              `json:"provider"`
	Connected  bool                `json:"connected"`
	Allowances []chatgpt.Allowance `json:"allowances"`
}

func chatGPTProviders(file *configfile.File, account *configfile.Account) map[string]configfile.Provider {
	providers := make(map[string]configfile.Provider, len(file.Providers)+len(account.Providers))
	maps.Copy(providers, file.Providers)
	maps.Copy(providers, account.Providers)
	for name, p := range providers {
		if p.Type != configfile.ProviderChatGPT {
			delete(providers, name)
		}
	}
	return providers
}

func (s *Server) getChatGPTAllowances(w http.ResponseWriter, r *http.Request, t *accountScope) error {
	providers := chatGPTProviders(t.file, t.account)
	out := make([]ChatGPTProviderUsage, 0, len(providers))
	for _, name := range slices.Sorted(maps.Keys(providers)) {
		p, _ := t.file.Provider(t.account, name)
		connected, allowances, err := s.store.ChatGPTAllowances(r.Context(), p.ChatGPTSessionKey())
		if err != nil {
			return err
		}
		usage := ChatGPTProviderUsage{Provider: name, Connected: connected, Allowances: []chatgpt.Allowance{}}
		for _, id := range slices.Sorted(maps.Keys(allowances)) {
			usage.Allowances = append(usage.Allowances, allowances[id])
		}
		out = append(out, usage)
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}
