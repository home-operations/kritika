package worker

import (
	"testing"
	"time"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/tuning"
)

func TestCarryWindow(t *testing.T) {
	for _, tc := range []struct {
		name string
		t    configfile.ProviderType
		ref  configfile.ModelRef
		want time.Duration
	}{
		{"an OpenAI model on OpenRouter", configfile.ProviderOpenRouter, "openrouter/openai/gpt-6.1-sol", tuning.Get().Worker.ContinueWindow},
		{"an Anthropic model on OpenRouter", configfile.ProviderOpenRouter, "openrouter/anthropic/claude-opus-5", tuning.Get().Worker.AnthropicContinueWindow},
		{"an Anthropic alias on OpenRouter", configfile.ProviderOpenRouter, "openrouter/~anthropic/claude-opus-latest", tuning.Get().Worker.AnthropicContinueWindow},
		{"Anthropic's own API", configfile.ProviderAnthropic, "anthropic/claude-opus-5", tuning.Get().Worker.AnthropicContinueWindow},
		{"OpenAI's own API", configfile.ProviderOpenAI, "openai/gpt-6.1-sol", tuning.Get().Worker.ContinueWindow},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := carryWindow(tc.t, tc.ref); got != tc.want {
				t.Fatalf("carryWindow(%s, %s) = %s, want %s", tc.t, tc.ref, got, tc.want)
			}
		})
	}
}
