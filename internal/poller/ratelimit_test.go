package poller

import (
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	gh "github.com/google/go-github/v93/github"
	gl "gitlab.com/gitlab-org/api/client-go/v3"
)

func TestRateLimited(t *testing.T) {
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	header := func(retryAfter string) http.Header {
		h := http.Header{}
		if retryAfter != "" {
			h.Set("Retry-After", retryAfter)
		}
		return h
	}
	gitlab429 := func(retryAfter string) error {
		return fmt.Errorf("gitlab: branch main of o/r: %w", &gl.ErrorResponse{
			StatusCode: http.StatusTooManyRequests,
			Response:   &http.Response{StatusCode: http.StatusTooManyRequests, Header: header(retryAfter)},
		})
	}
	for _, tc := range []struct {
		name    string
		err     error
		wait    time.Duration
		limited bool
	}{
		{name: "GitHub's primary limit", err: &gh.RateLimitError{
			Rate: gh.Rate{Reset: gh.Timestamp{Time: now.Add(time.Hour)}}, Response: &http.Response{StatusCode: http.StatusForbidden},
		}, wait: time.Hour + time.Second, limited: true},
		{name: "GitLab's 429 for its Retry-After in seconds", err: gitlab429("120"), wait: 2 * time.Minute, limited: true},
		{name: "GitLab's 429 for its Retry-After as a date", err: gitlab429(now.Add(90 * time.Second).Format(http.TimeFormat)),
			wait: 90 * time.Second, limited: true},
		{name: "GitLab's 429 without a Retry-After", err: gitlab429(""), wait: forgeRateLimitWait, limited: true},
		{name: "GitLab's 429 with a Retry-After that does not parse", err: gitlab429("soon"), wait: forgeRateLimitWait, limited: true},
		{name: "GitHub's 429 that is no limit it knows", err: &gh.ErrorResponse{
			Response: &http.Response{StatusCode: http.StatusTooManyRequests, Header: header("30")},
		}, wait: 30 * time.Second, limited: true},
		{name: "another status", err: &gl.ErrorResponse{StatusCode: http.StatusBadGateway}},
		{name: "no answer", err: errors.New("connection refused")},
		{name: "no error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wait, limited := rateLimited(tc.err, now)
			if wait != tc.wait || limited != tc.limited {
				t.Fatalf("rateLimited = %s %t, want %s %t", wait, limited, tc.wait, tc.limited)
			}
		})
	}
}
