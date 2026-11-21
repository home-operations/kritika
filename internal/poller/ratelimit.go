package poller

import (
	"cmp"
	"errors"
	"net/http"
	"strconv"
	"time"

	gh "github.com/google/go-github/v93/github"

	"github.com/home-operations/kritika/internal/forge/forgejo"
	"github.com/home-operations/kritika/internal/forge/github"
	"github.com/home-operations/kritika/internal/forge/gitlab"
)

// forgeRateLimitWait is how long the looks at an account's default branches
// pause for a forge's 429 that names no wait, or whose client keeps none.
const forgeRateLimitWait = time.Minute

// rateLimited reports how long to wait before asking the forge again after
// err, when err is the forge's rate limit: GitHub's, as github.RetryAfter
// reads it, GitLab's, as gitlab.RetryAfter reads it, or any forge's 429. A
// GitHub 429 that is no limit go-github knows waits for its Retry-After, and
// a 429 that names no wait, or whose client keeps no headers, as Forgejo's,
// waits forgeRateLimitWait.
func rateLimited(err error, now time.Time) (time.Duration, bool) {
	if wait, limited := github.RetryAfter(err, now); limited {
		return wait, true
	}
	if wait, limited := gitlab.RetryAfter(err, now); limited {
		return wait, true
	}
	if forgeStatus(err) != http.StatusTooManyRequests {
		return 0, false
	}
	if e, ok := errors.AsType[*gh.ErrorResponse](err); ok && e.Response != nil {
		if wait, ok := retryAfter(e.Response.Header.Get("Retry-After"), now); ok {
			return wait, true
		}
	}
	return forgeRateLimitWait, true
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

// forgeStatus is the HTTP status of the forge's answer that err carries, 0
// when it carries none.
func forgeStatus(err error) int {
	return cmp.Or(github.Status(err), gitlab.Status(err), forgejo.Status(err))
}
