package gitlab

import (
	"errors"
	"net/http"
	"time"

	gl "gitlab.com/gitlab-org/api/client-go/v3"

	"github.com/home-operations/kritika/internal/retryafter"
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
	reset, hasReset := retryafter.ResetEpoch(e.Response.Header.Get(retryafter.GitLabRateLimitReset), now, 0, true)
	after, hasAfter := retryafter.Parse(e.Response.Header.Get(retryafter.Header), now)
	return max(reset, after), hasReset || hasAfter
}
