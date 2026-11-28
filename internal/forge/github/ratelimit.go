package github

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"time"

	gh "github.com/google/go-github/v93/github"

	"github.com/home-operations/kritika/internal/retryafter"
	"github.com/home-operations/kritika/internal/tuning"
)

// RetryAfter reports how long to wait before trying again after err, when err
// is GitHub's rate limit from REST or GraphQL. The wait lasts until the
// primary limit resets, with the second of grace that rateLimitWait gives. Or
// it lasts for the secondary limit's Retry-After, or tuning's
// GitHub.RateLimitMaxWait when that limit names none. A 403 or 429 that go-github does not know for a
// limit, as one whose body names no secondary limit, is one still when it
// names a Retry-After, and lasts for that. A job retried sooner only spends
// an attempt. The REST client also refuses requests locally until its limit
// resets.
func RetryAfter(err error, now time.Time) (time.Duration, bool) {
	if e, ok := errors.AsType[*gh.RateLimitError](err); ok {
		return max(e.Rate.Reset.Sub(now)+time.Second, retryafter.Floor), true
	}
	if e, ok := errors.AsType[*gh.AbuseRateLimitError](err); ok {
		// go-github reads only seconds, and an HTTP date as 0, so GitHub's
		// own header goes first. One that names no wait gets the fallback,
		// not go-github's reading of it. A refusal that go-github makes
		// itself, before a request, carries no header.
		maxWait := tuning.Get().GitHub.RateLimitMaxWait
		var v string
		if e.Response != nil {
			v = e.Response.Header.Get(retryafter.Header)
		}
		if v == "" {
			return max(cmp.Or(e.GetRetryAfter(), maxWait), retryafter.Floor), true
		}
		if wait, ok := retryafter.Parse(v, now); ok {
			return wait, true
		}
		return max(maxWait, retryafter.Floor), true
	}
	if e, ok := errors.AsType[*gh.ErrorResponse](err); ok && e.Response != nil && e.Response.Header.Get(retryafter.Header) != "" {
		return rateLimitWait(e.Response, now)
	}
	return 0, false
}

// rateLimitTransport sends a request that GitHub refused for a rate limit
// once more when the limit resets soon. GitHub's secondary limits, on content
// creation above all, answer with a Retry-After of seconds to tens of
// seconds. Without the wait, every such refusal fails a review or a poll
// outright. River's retry, which knows nothing of Retry-After, can come back
// inside the same window.
type rateLimitTransport struct {
	base    http.RoundTripper
	maxWait time.Duration
	// observe, when set, is told of every rate-limited response: the wait
	// it asked for, and whether the request waited it out and was resent.
	observe func(wait time.Duration, waited bool)
	// sleep overrides the wait in tests.
	sleep func(ctx context.Context, d time.Duration) error
}

func (t *rateLimitTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	wait, limited := rateLimitWait(resp, time.Now())
	if !limited {
		return resp, nil
	}
	// A body that cannot be read again cannot be sent again.
	retry := wait <= t.maxWait && (req.Body == nil || req.Body == http.NoBody || req.GetBody != nil)
	observe := func(resent bool) {
		if t.observe != nil {
			t.observe(wait, resent)
		}
	}
	if !retry {
		observe(false)
		return resp, nil
	}
	_ = resp.Body.Close()
	if err := t.wait(req.Context(), wait); err != nil {
		observe(false)
		return nil, err
	}
	observe(true)
	again := req.Clone(req.Context())
	if req.GetBody != nil {
		body, err := req.GetBody()
		if err != nil {
			return nil, err
		}
		again.Body = body
	}
	return t.base.RoundTrip(again)
}

func (t *rateLimitTransport) wait(ctx context.Context, d time.Duration) error {
	if t.sleep != nil {
		return t.sleep(ctx, d)
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// rateLimitWait reads how long a response says to wait. GitHub answers a
// rate-limited request with 403 or 429. For a secondary limit, the answer has
// Retry-After, in seconds or as a date. For the primary limit, it has
// x-ratelimit-remaining 0 with the reset as an epoch. The wait is never under
// a second, and a reset gets a second's grace for clock skew. Any other
// response is not a limit.
func rateLimitWait(resp *http.Response, now time.Time) (time.Duration, bool) {
	if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusTooManyRequests {
		return 0, false
	}
	if wait, ok := retryafter.Parse(resp.Header.Get(retryafter.Header), now); ok {
		return wait, true
	}
	if resp.Header.Get(retryafter.RateLimitRemaining) == "0" {
		return retryafter.ResetEpoch(resp.Header.Get(retryafter.RateLimitReset), now, time.Second, false)
	}
	return 0, false
}

// Status returns the HTTP status that go-github's generic ErrorResponse in
// err carries, or 0: for no answer, and for GitHub's rate limits that
// go-github returns as errors of their own. RetryAfter recognizes those, and
// a refusal of this kind that names a Retry-After.
func Status(err error) int {
	if e, ok := errors.AsType[*gh.ErrorResponse](err); ok && e.Response != nil {
		return e.Response.StatusCode
	}
	return 0
}
