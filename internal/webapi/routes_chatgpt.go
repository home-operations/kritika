package webapi

import (
	"maps"
	"net/http"
	"slices"

	"github.com/home-operations/kritika/internal/chatgpt"
)

// ChatGPTProviderUsage is a configured provider's connection and quota data.
type ChatGPTProviderUsage struct {
	Provider   string              `json:"provider"`
	Connected  bool                `json:"connected"`
	Allowances []chatgpt.Allowance `json:"allowances"`
}

func (s *Server) getChatGPTAllowances(w http.ResponseWriter, r *http.Request, t *accountScope) error {
	providers := t.file.ChatGPTProviders(t.account)
	out := make([]ChatGPTProviderUsage, 0, len(providers))
	for _, name := range slices.Sorted(maps.Keys(providers)) {
		connected, allowances, err := s.store.ChatGPTAllowances(r.Context(), providers[name].ChatGPTSessionKey())
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
