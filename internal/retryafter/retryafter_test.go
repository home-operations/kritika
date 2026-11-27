package retryafter

import (
	"math"
	"net/http"
	"strconv"
	"testing"
	"time"
)

// TestParse: a Retry-After in seconds or as a date, never under a second,
// and nothing for one that names no wait. The cases are those of the forge
// clients that read it.
func TestParse(t *testing.T) {
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		v    string
		wait time.Duration
		ok   bool
	}{
		{name: "seconds", v: "120", wait: 2 * time.Minute, ok: true},
		{name: "a few seconds", v: "5", wait: 5 * time.Second, ok: true},
		{name: "a zero still waits a second", v: "0", wait: time.Second, ok: true},
		{name: "a date", v: now.Add(90 * time.Second).Format(http.TimeFormat), wait: 90 * time.Second, ok: true},
		{name: "a date passed still waits a second", v: now.Add(-time.Hour).Format(http.TimeFormat), wait: time.Second, ok: true},
		{name: "empty", v: ""},
		{name: "no number or date", v: "soon"},
		{name: "a negative number", v: "-5"},
		{name: "past 32 bits of seconds", v: "4294967296"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if wait, ok := Parse(tc.v, now); wait != tc.wait || ok != tc.ok {
				t.Fatalf("Parse(%q) = %s %t, want %s %t", tc.v, wait, ok, tc.wait, tc.ok)
			}
		})
	}
}

// TestResetEpoch: GitHub's reset, with a second's grace and a reset passed
// still a wait, and GitLab's, with no grace and a reset passed none.
func TestResetEpoch(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	epoch := func(d time.Duration) string { return strconv.FormatInt(now.Add(d).Unix(), 10) }
	for _, tc := range []struct {
		name       string
		v          string
		grace      time.Duration
		rejectPast bool
		wait       time.Duration
		ok         bool
	}{
		{name: "GitHub: until the reset and a second", v: epoch(40 * time.Second), grace: time.Second, wait: 41 * time.Second, ok: true},
		{name: "GitHub: a reset passed still waits a second", v: epoch(-time.Hour), grace: time.Second, wait: time.Second, ok: true},
		{name: "GitHub: a reset now waits its grace", v: epoch(0), grace: time.Second, wait: time.Second, ok: true},
		{name: "GitHub: no number", v: "later", grace: time.Second},
		{name: "GitLab: until the reset", v: epoch(2 * time.Hour), rejectPast: true, wait: 2 * time.Hour, ok: true},
		{name: "GitLab: a reset passed is none", v: epoch(-time.Minute), rejectPast: true},
		{name: "GitLab: a reset now is none", v: epoch(0), rejectPast: true},
		{name: "GitLab: empty", v: "", rejectPast: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if wait, ok := ResetEpoch(tc.v, now, tc.grace, tc.rejectPast); wait != tc.wait || ok != tc.ok {
				t.Fatalf("ResetEpoch(%q) = %s %t, want %s %t", tc.v, wait, ok, tc.wait, tc.ok)
			}
		})
	}
}

// TestParseFractional: the model providers' wait, as their SDKs read it.
// The cases are the model package's.
func TestParseFractional(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	with := func(name, v string) http.Header { return http.Header{name: {v}} }
	for _, tt := range []struct {
		name string
		h    http.Header
		want time.Duration
	}{
		{"no header", http.Header{}, 0},
		{"seconds", with(Header, "45"), 45 * time.Second},
		{"a fraction of seconds", with(Header, "2.5"), 2500 * time.Millisecond},
		{"milliseconds", with(HeaderMs, "1500"), 1500 * time.Millisecond},
		{"milliseconds before seconds", http.Header{HeaderMs: {"1500"}, Header: {"45"}}, 1500 * time.Millisecond},
		{"a date ahead", with(Header, now.Add(30*time.Second).Format(http.TimeFormat)), 30 * time.Second},
		{"a date past", with(Header, "Wed, 21 Oct 2015 07:28:00 GMT"), 0},
		{"zero asks nothing", with(Header, "0"), 0},
		{"a negative wait", with(Header, "-3"), 0},
		{"NaN", with(Header, "NaN"), 0},
		{"infinity", with(HeaderMs, "Inf"), 0},
		{"a wait past any Duration", with(Header, "1e30"), math.MaxInt64},
		{"no number or date", with(Header, "soon"), 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := ParseFractional(tt.h, now); got != tt.want {
				t.Fatalf("ParseFractional = %s, want %s", got, tt.want)
			}
		})
	}
}
