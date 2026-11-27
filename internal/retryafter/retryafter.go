// Package retryafter reads the headers in which an HTTP service says how long
// to wait before asking again: Retry-After, the SDKs' Retry-After-Ms, and the
// rate-limit resets that GitHub and GitLab send as Unix seconds. Each caller
// keeps its own policy, which answers count as a limit and how long a wait
// it honors; this package only reads the waits.
//
// It imports only the standard library, so that the runner's model package
// can share it with the forges without importing them.
package retryafter

import (
	"math"
	"net/http"
	"strconv"
	"time"
)

// The headers that name a wait.
const (
	// Header is the standard Retry-After: whole seconds or an HTTP date.
	Header = "Retry-After"
	// HeaderMs is Retry-After-Ms, in milliseconds, which the OpenAI and
	// Anthropic SDKs read before Retry-After.
	HeaderMs = "Retry-After-Ms"
	// RateLimitRemaining and RateLimitReset are GitHub's primary limit:
	// what is left of it, and when it resets, in Unix seconds.
	RateLimitRemaining = "X-RateLimit-Remaining"
	RateLimitReset     = "X-RateLimit-Reset"
	// GitLabRateLimitReset is GitLab's reset, in Unix seconds.
	GitLabRateLimitReset = "Ratelimit-Reset"
)

// Floor is the shortest wait that Parse and ResetEpoch name: a wait of
// zero, or one already over, still waits a second, so that a caller does
// not ask again at once.
const Floor = time.Second

// Parse is the wait that a Retry-After header of v names, in whole seconds
// or as an HTTP date, and at least Floor. It reports false when v names no
// wait.
func Parse(v string, now time.Time) (time.Duration, bool) {
	if v == "" {
		return 0, false
	}
	if s, err := strconv.ParseInt(v, 10, 32); err == nil && s >= 0 {
		return max(time.Duration(s)*time.Second, Floor), true
	}
	if t, err := http.ParseTime(v); err == nil {
		return max(t.Sub(now), Floor), true
	}
	return 0, false
}

// ResetEpoch is the wait until the time that a reset header of v names, in
// Unix seconds, plus grace for clock skew, and at least Floor. It reports
// false when v is no number, or, with rejectPast, when the reset is no
// later than now.
func ResetEpoch(v string, now time.Time, grace time.Duration, rejectPast bool) (time.Duration, bool) {
	s, err := strconv.ParseInt(v, 10, 64)
	if err != nil || rejectPast && s <= now.Unix() {
		return 0, false
	}
	return max(time.Unix(s, 0).Sub(now)+grace, Floor), true
}

// ParseFractional is the wait that h names as openai-go and
// anthropic-sdk-go read it: Retry-After-Ms in milliseconds, else
// Retry-After in seconds, fractions allowed, or as an HTTP date. It has no
// floor, and is zero when h names nothing usable: no number, or one that is
// not positive and finite. A number of seconds too large for a Duration is
// the longest one.
func ParseFractional(h http.Header, now time.Time) time.Duration {
	if ms, err := strconv.ParseFloat(h.Get(HeaderMs), 64); err == nil {
		return seconds(ms / 1000)
	}
	v := h.Get(Header)
	if secs, err := strconv.ParseFloat(v, 64); err == nil {
		return seconds(secs)
	}
	if at, err := http.ParseTime(v); err == nil {
		return max(at.Sub(now), 0)
	}
	return 0
}

// seconds is secs as a Duration: zero unless it is a positive, finite
// number, and at most the longest Duration.
func seconds(secs float64) time.Duration {
	if !(secs > 0) || math.IsInf(secs, 1) {
		return 0
	}
	if secs >= float64(math.MaxInt64)/float64(time.Second) {
		return math.MaxInt64
	}
	return time.Duration(secs * float64(time.Second))
}
