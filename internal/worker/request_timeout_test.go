package worker

import (
	"testing"
	"time"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/store"
)

// TestGatewayRequestTimeout: the runner's request to the gateway outlasts
// the longest step budget of the run's models, the reviewers' included, by
// the time the gateway takes to record an answer.
func TestGatewayRequestTimeout(t *testing.T) {
	t.Setenv("TEST_PRIVATE_KEY", "pem")
	t.Setenv("TEST_WEBHOOK_SECRET", "s")
	t.Setenv("TEST_PROVIDER_KEY", "k")
	file, err := configfile.Parse([]byte(`
apps:
  acme-bot: { accounts: [acme], clientId: x, privateKey: { env: TEST_PRIVATE_KEY }, webhookSecret: { env: TEST_WEBHOOK_SECRET } }
providers:
  fast: { type: openrouter, apiKey: { env: TEST_PROVIDER_KEY } }
  slow: { type: openai, baseUrl: http://llm.local/v1, apiKey: { env: TEST_PROVIDER_KEY }, timeout: 15m }
review: { model: fast/big }
accounts:
  acme: {}
`))
	if err != nil {
		t.Fatal(err)
	}
	account := &file.Accounts[0]
	for _, tt := range []struct {
		name      string
		models    configfile.Models
		reviewers map[string]store.ReviewerGrant
		want      time.Duration
	}{
		{"defaults", configfile.Models{Review: "fast/big"}, nil, 17 * time.Minute},
		{"a slow fallback", configfile.Models{Review: "fast/big", Fallback: "slow/small"}, nil, 27 * time.Minute},
		{"a reviewer on the slow model", configfile.Models{Review: "fast/big"},
			map[string]store.ReviewerGrant{"security": {Model: "slow/big"}}, 37 * time.Minute},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := gatewayRequestTimeout(file, account, tt.models, tt.reviewers); got != tt.want {
				t.Fatalf("gatewayRequestTimeout = %s, want %s", got, tt.want)
			}
		})
	}
}
