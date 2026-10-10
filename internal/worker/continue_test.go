package worker

import (
	"testing"
	"time"

	"github.com/home-operations/kritika/internal/configfile"
)

func TestCarryWindow(t *testing.T) {
	for _, tc := range []struct {
		name string
		t    configfile.ProviderType
		ref  configfile.ModelRef
		want time.Duration
	}{
		{"an OpenAI model on OpenRouter", configfile.ProviderOpenRouter, "openrouter/openai/gpt-6.1-sol", continueWindow},
		{"an Anthropic model on OpenRouter", configfile.ProviderOpenRouter, "openrouter/anthropic/claude-opus-5", anthropicContinueWindow},
		{"an Anthropic alias on OpenRouter", configfile.ProviderOpenRouter, "openrouter/~anthropic/claude-opus-latest", anthropicContinueWindow},
		{"Anthropic's own API", configfile.ProviderAnthropic, "anthropic/claude-opus-5", anthropicContinueWindow},
		{"OpenAI's own API", configfile.ProviderOpenAI, "openai/gpt-6.1-sol", continueWindow},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := carryWindow(tc.t, tc.ref); got != tc.want {
				t.Fatalf("carryWindow(%s, %s) = %s, want %s", tc.t, tc.ref, got, tc.want)
			}
		})
	}
}
