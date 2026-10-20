package configfile

import (
	"fmt"
	"strings"
	"testing"
)

func TestProviderPricingValidation(t *testing.T) {
	t.Setenv("TEST_PROVIDER_KEY", "test-key")
	for _, field := range []string{"input", "output", "cacheRead", "cacheWrite"} {
		t.Run(field, func(t *testing.T) {
			for _, tc := range []struct {
				name, value string
				valid       bool
			}{
				{name: "zero", value: "0", valid: true},
				{name: "positive", value: "3.75", valid: true},
				{name: "negative", value: "-1"},
				{name: "not a number", value: ".nan"},
				{name: "positive infinity", value: ".inf"},
				{name: "negative infinity", value: "-.inf"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					doc := fmt.Sprintf(`providers:
  p:
    type: openai
    apiKey: { env: TEST_PROVIDER_KEY }
    pricing:
      test-model: { %s: %s }
review: { model: p/test-model }
`, field, tc.value)
					_, err := Parse([]byte(doc))
					if tc.valid {
						if err != nil {
							t.Fatalf("Parse: %v", err)
						}
					} else if err == nil || !strings.Contains(err.Error(), "providers.p.pricing.test-model") {
						t.Fatalf("Parse = %v, want a provider pricing error", err)
					}
				})
			}
		})
	}
}
