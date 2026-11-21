package gitlab

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	gl "gitlab.com/gitlab-org/api/client-go/v3"
)

// RetryAfter reports how long to wait before asking GitLab again after err,
// when err is GitLab's 429 that names when to: the later of the time that
// its Ratelimit-Reset names and the wait that its Retry-After names, and at
// least a second. A 429 that names neither, or only a reset that has passed,
// is not reported.
func RetryAfter(err error, now time.Time) (time.Duration, bool) {
	e, ok := errors.AsType[*gl.ErrorResponse](err)
	if !ok || e.StatusCode != http.StatusTooManyRequests || e.Response == nil {
		return 0, false
	}
	reset, hasReset := rateLimitReset(e.Response.Header.Get("Ratelimit-Reset"), now)
	after, hasAfter := retryAfter(e.Response.Header.Get("Retry-After"), now)
	return max(reset, after), hasReset || hasAfter
}

// retryAfter is the wait that a Retry-After header of v names, in seconds or
// as a date, and at least a second.
func retryAfter(v string, now time.Time) (time.Duration, bool) {
	if v == "" {
		return 0, false
	}
	if s, err := strconv.ParseInt(v, 10, 32); err == nil && s >= 0 {
		return max(time.Duration(s)*time.Second, time.Second), true
	}
	if t, err := http.ParseTime(v); err == nil {
		return max(t.Sub(now), time.Second), true
	}
	return 0, false
}

// rateLimitReset is the wait until the time that a Ratelimit-Reset header of
// v names, in Unix seconds, and at least a second. A reset that has passed
// names no wait.
func rateLimitReset(v string, now time.Time) (time.Duration, bool) {
	s, err := strconv.ParseInt(v, 10, 64)
	if err != nil || s <= now.Unix() {
		return 0, false
	}
	return max(time.Unix(s, 0).Sub(now), time.Second), true
}
