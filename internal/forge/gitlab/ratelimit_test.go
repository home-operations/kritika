package gitlab

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"testing"
	"time"

	gl "gitlab.com/gitlab-org/api/client-go/v3"
)

// TestRetryAfter: GitLab's 429 waits for the later of its Ratelimit-Reset
// and its Retry-After. One that names neither is left to the caller's own
// backoff, as is any other answer.
func TestRetryAfter(t *testing.T) {
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	limited := func(reset int64, retryAfter string) error {
		h := http.Header{}
		if reset != 0 {
			h.Set("Ratelimit-Reset", strconv.FormatInt(reset, 10))
		}
		if retryAfter != "" {
			h.Set("Retry-After", retryAfter)
		}
		return fmt.Errorf("gitlab: branch main of o/r: %w", &gl.ErrorResponse{
			StatusCode: http.StatusTooManyRequests,
			Response:   &http.Response{StatusCode: http.StatusTooManyRequests, Header: h},
		})
	}
	for _, tc := range []struct {
		name    string
		err     error
		wait    time.Duration
		limited bool
	}{
		{name: "until the reset, past the Retry-After", err: limited(now.Add(2*time.Hour).Unix(), "60"), wait: 2 * time.Hour, limited: true},
		{name: "for the Retry-After, past the reset", err: limited(now.Add(30*time.Second).Unix(), "120"), wait: 2 * time.Minute, limited: true},
		{name: "until the reset alone", err: limited(now.Add(time.Hour).Unix(), ""), wait: time.Hour, limited: true},
		{name: "for the Retry-After as a date", err: limited(0, now.Add(90*time.Second).Format(http.TimeFormat)), wait: 90 * time.Second, limited: true},
		{name: "for the Retry-After of a passed reset", err: limited(now.Add(-time.Minute).Unix(), "60"), wait: time.Minute, limited: true},
		{name: "a second at least", err: limited(0, "0"), wait: time.Second, limited: true},
		{name: "a passed reset alone", err: limited(now.Add(-time.Minute).Unix(), "")},
		{name: "a Retry-After that does not parse", err: limited(0, "soon")},
		{name: "no headers", err: limited(0, "")},
		{name: "another status", err: &gl.ErrorResponse{StatusCode: http.StatusBadGateway, Response: &http.Response{Header: http.Header{"Retry-After": {"60"}}}}},
		{name: "no answer", err: errors.New("connection refused")},
		{name: "no error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if wait, ok := RetryAfter(tc.err, now); wait != tc.wait || ok != tc.limited {
				t.Fatalf("RetryAfter = %s %t, want %s %t", wait, ok, tc.wait, tc.limited)
			}
		})
	}
}
