package model

import (
	"net/http"
	"testing"
)

func TestUnpricedModels(t *testing.T) {
	for _, provider := range []ProviderType{ProviderOpenAI, ProviderAnthropic} {
		t.Run(string(provider), func(t *testing.T) {
			for _, tt := range []struct {
				name     string
				ref      string
				pricing  Pricing
				cost     float64
				unpriced bool
			}{
				{"missing", "new-model", nil, 0, true},
				{"explicit zero", "new-model", Pricing{"new-model": {}}, 0, false},
				{"priced", "new-model", Pricing{"new-model": {Input: 2}}, 2, false},
				{"alias rollover", "~family-latest@new-model", Pricing{"old-model": {Input: 2}}, 0, true},
				{"alias priced", "~family-latest@new-model", Pricing{"~family-latest": {Input: 3}}, 3, false},
				{"alias zero", "~family-latest@new-model", Pricing{"~family-latest": {}}, 0, false},
				{"concrete overrides alias", "~family-latest@new-model", Pricing{"new-model": {}, "~family-latest": {Input: 3}}, 0, false},
			} {
				t.Run(tt.name, func(t *testing.T) {
					body := chatCompletion(`{"role":"assistant","content":"ok"}`, "stop", `{"prompt_tokens":1000000,"completion_tokens":10}`, "")
					if provider == ProviderAnthropic {
						body = anthropicMessage(`[{"type":"text","text":"ok"}]`, "end_turn", `{"input_tokens":1000000,"output_tokens":10}`)
					}
					srv, _ := fakeProvider(t, http.StatusOK, body)
					s, err := NewStepper(provider, srv.URL, "test-key", tt.pricing, nil)
					if err != nil {
						t.Fatal(err)
					}
					resp, err := s.Step(t.Context(), StepRequest{Model: tt.ref, Messages: []Message{{Role: RoleUser, Text: "hi"}}})
					if err != nil {
						t.Fatal(err)
					}
					if resp.Model != "new-model" || resp.CostUSD != tt.cost || resp.Unpriced != tt.unpriced {
						t.Fatalf("response = %+v; want cost %v, unpriced %v", resp, tt.cost, tt.unpriced)
					}
				})
			}
		})
	}
}

func TestReportedCost(t *testing.T) {
	for _, tt := range []struct {
		name     string
		usage    string
		pricing  Pricing
		cost     float64
		unpriced bool
	}{
		{"reported zero", `{"prompt_tokens":1000000,"cost":0}`, Pricing{"requested": {Input: 2}}, 0, false},
		{"reported paid", `{"prompt_tokens":1000000,"cost":0.25}`, nil, 0.25, false},
		{"null cost", `{"prompt_tokens":1000000,"cost":null}`, nil, 0, true},
		{"unpriced server fallback", `{"prompt_tokens":1000000}`, Pricing{"requested": {Input: 2}}, 0, true},
		{"priced server fallback", `{"prompt_tokens":1000000}`, Pricing{"acme/large": {Input: 3}}, 3, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body := chatCompletion(`{"role":"assistant","content":"ok"}`, "stop", tt.usage, "")
			srv, _ := fakeProvider(t, http.StatusOK, body)
			c := newTestOpenAI(t, srv, true, tt.pricing)
			resp, err := c.Step(t.Context(), StepRequest{Model: "requested", Messages: []Message{{Role: RoleUser, Text: "hi"}}})
			if err != nil {
				t.Fatal(err)
			}
			if resp.CostUSD != tt.cost || resp.Unpriced != tt.unpriced {
				t.Fatalf("response = %+v; want cost %v, unpriced %v", resp, tt.cost, tt.unpriced)
			}
		})
	}
}

func TestChatCostRoundTrip(t *testing.T) {
	for _, tt := range []struct {
		name string
		resp StepResponse
	}{
		{"unpriced", StepResponse{Unpriced: true}},
		{"known zero", StepResponse{}},
		{"paid", StepResponse{CostUSD: 0.25}},
		{"plan", StepResponse{ChatGPTPlan: true}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tt.resp.Model, tt.resp.Stop = "served", StopEndTurn
			tt.resp.Usage = Usage{Input: 100, Output: 10}
			srv, _ := fakeGateway(t, tt.resp)
			c, err := NewOpenAI(OpenAIConfig{BaseURL: srv.URL, APIKey: "test-key", ReportsModel: true, Gateway: true})
			if err != nil {
				t.Fatal(err)
			}
			resp, err := c.Step(t.Context(), StepRequest{Model: "review", Messages: []Message{{Role: RoleUser, Text: "hi"}}})
			if err != nil {
				t.Fatal(err)
			}
			if resp.CostUSD != tt.resp.CostUSD || resp.Unpriced != tt.resp.Unpriced || resp.Usage != tt.resp.Usage {
				t.Fatalf("response = %+v; want billing from %+v", resp, tt.resp)
			}
		})
	}
}
