package chatgpt

import (
	"net/http"
	"testing"
	"time"
)

func TestAllowancesFromHeaders(t *testing.T) {
	now := time.Now().UTC()
	for _, tt := range []struct {
		name    string
		headers map[string]string
		count   int
		percent float64
	}{
		{"absent", nil, 0, 0},
		{"API limits are not plan quotas", map[string]string{"x-ratelimit-remaining-tokens": "5000"}, 0, 0},
		{"unused allowance", map[string]string{"x-codex-primary-used-percent": "0"}, 1, 0},
		{"fractional usage", map[string]string{"x-codex-primary-used-percent": "25.5"}, 1, 25.5},
		{"overage", map[string]string{"x-codex-primary-used-percent": "101"}, 1, 101},
		{"malformed", map[string]string{"x-codex-primary-used-percent": "unknown"}, 0, 0},
		{"nonfinite", map[string]string{"x-codex-primary-used-percent": "NaN"}, 0, 0},
		{"infinite", map[string]string{"x-codex-primary-used-percent": "+Inf"}, 0, 0},
		{"negative", map[string]string{"x-codex-primary-used-percent": "-1"}, 0, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := make(http.Header)
			for name, value := range tt.headers {
				h.Set(name, value)
			}
			got := AllowancesFromHeaders(h, now)
			if len(got) != tt.count {
				t.Fatalf("allowances = %+v", got)
			}
			if len(got) > 0 && (got[0].Primary.UsedPercent != tt.percent || got[0].ObservedAt != now ||
				got[0].Primary.WindowDurationMins != nil || got[0].Primary.ResetsAt != nil) {
				t.Fatalf("allowance = %+v, window %+v", got[0], got[0].Primary)
			}
		})
	}
	t.Run("multiple buckets and window lengths", func(t *testing.T) {
		h := make(http.Header)
		for name, value := range map[string]string{
			"x-codex-primary-used-percent": "25", "x-codex-primary-window-minutes": "300", "x-codex-primary-reset-at": "1800000000",
			"x-codex-secondary-used-percent": "60", "x-codex-secondary-window-minutes": "10080",
			"x-codex-other-primary-used-percent": "5", "x-codex-other-primary-window-minutes": "60",
		} {
			h.Set(name, value)
		}
		got := AllowancesFromHeaders(h, now)
		if len(got) != 2 || got[0].LimitID != "codex" || got[1].LimitID != "codex_other" ||
			*got[0].Primary.WindowDurationMins != 300 || *got[0].Primary.ResetsAt != 1800000000 ||
			got[0].Secondary.UsedPercent != 60 || *got[0].Secondary.WindowDurationMins != 10080 ||
			got[0].Secondary.ResetsAt != nil || *got[1].Primary.WindowDurationMins != 60 {
			t.Fatalf("allowances = %+v", got)
		}
	})
}

func TestAllowanceFromEvent(t *testing.T) {
	now := time.Now().UTC()
	for _, tt := range []struct {
		name string
		raw  string
		ok   bool
	}{
		{"other event", `{"type":"response.completed"}`, false},
		{"invalid JSON", `{`, false},
		{"no windows", `{"type":"codex.rate_limits","rate_limits":{}}`, false},
		{"missing percentage", `{"type":"codex.rate_limits","rate_limits":{"primary":{"window_minutes":300}}}`, false},
		{"zero usage", `{"type":"codex.rate_limits","rate_limits":{"primary":{"used_percent":0}}}`, true},
		{"negative usage", `{"type":"codex.rate_limits","rate_limits":{"primary":{"used_percent":-1}}}`, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			a, ok := AllowanceFromEvent(tt.raw, now)
			if ok != tt.ok {
				t.Fatalf("event = %+v, %v", a, ok)
			}
		})
	}
	a, ok := AllowanceFromEvent(`{"type":"codex.rate_limits","metered_limit_name":"codex-other","rate_limits":{
		"primary":{"used_percent":25,"window_minutes":300,"reset_at":1800000000},
		"secondary":{"used_percent":60,"window_minutes":10080,"reset_at":1800001000}}}`, now)
	if !ok || a.LimitID != "codex_other" || a.ObservedAt != now || *a.Primary.WindowDurationMins != 300 ||
		*a.Primary.ResetsAt != 1800000000 || a.Secondary.UsedPercent != 60 || *a.Secondary.WindowDurationMins != 10080 {
		t.Fatalf("event = %+v, %v", a, ok)
	}
}
