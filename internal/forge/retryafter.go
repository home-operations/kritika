package forge

import (
	"net/http"
	"strconv"
	"time"
)

// RetryAfter is the wait that a Retry-After header of v names, in seconds or
// as an HTTP date, and at least a second. It reports false when v names no
// wait.
func RetryAfter(v string, now time.Time) (time.Duration, bool) {
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
