package webapi

import (
	"math"
	"testing"
	"time"

	"github.com/home-operations/kritika/internal/tuning"
)

// TestTokenLifetimeChoices: an error names tuning's WebAPI.TokenLifetimes.
func TestTokenLifetimeChoices(t *testing.T) {
	day := 24 * time.Hour
	for _, tt := range []struct {
		lifetimes []time.Duration
		want      string
	}{
		{[]time.Duration{0, 7 * day, 30 * day, 90 * day, 365 * day}, "7, 30, 90, 365, or 0 for never"},
		{[]time.Duration{14 * day}, "14"},
		{[]time.Duration{0}, "0 for never"},
	} {
		tuning.Override(t, func(v *tuning.Values) { v.WebAPI.TokenLifetimes = tt.lifetimes })
		if got := tokenLifetimeChoices(); got != tt.want {
			t.Errorf("choices of %v = %q, want %q", tt.lifetimes, got, tt.want)
		}
		if !tokenLifetime(int(tt.lifetimes[0]/day)) || tokenLifetime(1) {
			t.Errorf("lifetimes %v: the first offered is refused, or 1 day is taken", tt.lifetimes)
		}
	}
}

// TestTokenLifetimeOutOfRange: a request for days that, multiplied into a
// duration, would overflow into an offered lifetime is refused. Negative
// days would otherwise make a token that never expires.
func TestTokenLifetimeOutOfRange(t *testing.T) {
	day := 24 * time.Hour
	tuning.Override(t, func(v *tuning.Values) { v.WebAPI.TokenLifetimes = []time.Duration{0, 7 * day} })
	// A day is 2^16 times an odd number of nanoseconds, so 2^48 days are a
	// multiple of 2^64 nanoseconds, which wraps to 0.
	wraps := 1 << 48
	for _, days := range []int{-1, -7, wraps, -wraps, math.MaxInt, math.MinInt} {
		if tokenLifetime(days) {
			t.Errorf("tokenLifetime(%d) = true, want it refused", days)
		}
	}
	if !tokenLifetime(0) || !tokenLifetime(7) {
		t.Error("an offered lifetime is refused")
	}
}
