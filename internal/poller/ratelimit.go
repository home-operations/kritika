package poller

import (
	"cmp"
	"errors"
	"net/http"
	"strconv"
	"time"

	gh "github.com/google/go-github/v93/github"
	gl "gitlab.com/gitlab-org/api/client-go/v3"

	"github.com/home-operations/kritika/internal/forge/forgejo"
	"github.com/home-operations/kritika/internal/forge/github"
	"github.com/home-operations/kritika/internal/forge/gitlab"
)

// forgeRateLimitWait is how long the looks at an account's default branches
// pause for a forge's 429 that names no wait, or whose client keeps none.
const forgeRateLimitWait = time.Minute

// rateLimited reports how long to wait before asking the forge again after
// err, when err is the forge's rate limit: GitHub's, as github.RetryAfter
// reads it, or any forge's 429. A 429 waits for the later of the
// Ratelimit-Reset that GitLab names and the Retry-After that it names, when
// its client keeps the answer's headers, as go-github's and GitLab's do,
// and forgeRateLimitWait when it names neither, as for Forgejo's.
func rateLimited(err error, now time.Time) (time.Duration, bool) {
	if wait, limited := github.RetryAfter(err, now); limited {
		return wait, true
	}
	if cmp.Or(github.Status(err), gitlab.Status(err), forgejo.Status(err)) != http.StatusTooManyRequests {
		return 0, false
	}
	var resp *http.Response
	var reset time.Duration
	if e, ok := errors.AsType[*gl.ErrorResponse](err); ok {
		resp = e.Response
		if resp != nil {
			reset, _ = rateLimitReset(resp.Header.Get("Ratelimit-Reset"), now)
		}
	} else if e, ok := errors.AsType[*gh.ErrorResponse](err); ok {
		resp = e.Response
	}
	var after time.Duration
	if resp != nil {
		after, _ = retryAfter(resp.Header.Get("Retry-After"), now)
	}
	if wait := max(reset, after); wait > 0 {
		return wait, true
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

// rateLimitReset is the wait until the time that GitLab's Ratelimit-Reset
// header of v names, in Unix seconds, and at least a second. A reset that has
// passed names no wait.
func rateLimitReset(v string, now time.Time) (time.Duration, bool) {
	s, err := strconv.ParseInt(v, 10, 64)
	if err != nil || s <= now.Unix() {
		return 0, false
	}
	return max(time.Unix(s, 0).Sub(now), time.Second), true
}
