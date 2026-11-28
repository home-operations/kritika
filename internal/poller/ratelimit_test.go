package poller

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"testing"
	"time"

	gh "github.com/google/go-github/v93/github"
	gl "gitlab.com/gitlab-org/api/client-go/v3"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/configfile/configfiletest"
	"github.com/home-operations/kritika/internal/tuning"
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
	gitlabReset := func(reset int64, retryAfter string) error {
		err := gitlab429(retryAfter)
		if e, ok := errors.AsType[*gl.ErrorResponse](err); ok {
			e.Response.Header.Set("Ratelimit-Reset", strconv.FormatInt(reset, 10))
		}
		return err
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
		{name: "GitLab's 429 without a Retry-After", err: gitlab429(""), wait: tuning.Get().Poller.ForgeRateLimitWait, limited: true},
		{name: "GitLab's 429 with a Retry-After that does not parse", err: gitlab429("soon"), wait: tuning.Get().Poller.ForgeRateLimitWait, limited: true},
		{name: "GitLab's 429 until its Ratelimit-Reset, past its Retry-After", err: gitlabReset(now.Add(2*time.Hour).Unix(), "60"),
			wait: 2 * time.Hour, limited: true},
		{name: "GitLab's 429 for its Retry-After, past its Ratelimit-Reset", err: gitlabReset(now.Add(30*time.Second).Unix(), "120"),
			wait: 2 * time.Minute, limited: true},
		{name: "GitLab's 429 with a Ratelimit-Reset that has passed", err: gitlabReset(now.Add(-time.Minute).Unix(), "60"),
			wait: time.Minute, limited: true},
		// go-github v93 reads an HTTP date as no wait at all.
		{name: "GitHub's secondary limit until its Retry-After date", err: &gh.AbuseRateLimitError{
			RetryAfter: new(time.Duration),
			Response:   &http.Response{StatusCode: http.StatusForbidden, Header: header(now.Add(10 * time.Minute).Format(http.TimeFormat))},
		}, wait: 10 * time.Minute, limited: true},
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

// TestTipsOrderStartsAScopeAtItsRefusedAccount: among the accounts that one
// credential's rate limit counts, a look starts at the one whose lookup the
// limit refused, and goes round from it, while every other account keeps
// its place. Once that account's look goes ahead, the order is as before.
func TestTipsOrderStartsAScopeAtItsRefusedAccount(t *testing.T) {
	t.Setenv("TEST_PEM", "pem")
	t.Setenv("TEST_SECRET", "s")
	t.Setenv("TEST_GL_HOOK", "whsec_c2lnbmluZy1rZXk=")
	file := configfiletest.Load(t, `
apps:
  bot-ross:
    accounts: [onedr0p]
    clientId: Iv1.x
    privateKey: { env: TEST_PEM }
    webhookSecret: { env: TEST_SECRET }
gitlab:
  lab-a:
    host: gitlab.example.com
    accounts: [acme, globex, hooli]
    token: { env: TEST_SECRET }
    webhookSecret: { env: TEST_GL_HOOK }
  lab-b:
    host: gitlab.other.example
    accounts: [initech]
    token: { env: TEST_SECRET }
    webhookSecret: { env: TEST_GL_HOOK }
`)
	p := &Poller{}
	ids := func(order []*configfile.Account) []string {
		out := make([]string, len(order))
		for i, a := range order {
			out[i] = a.ID()
		}
		return out
	}
	order := p.tipsOrder(file)
	before := ids(order)
	var lab []int
	for i, a := range order {
		if file.ConnectionFor(a).Name == "lab-a" {
			lab = append(lab, i)
		}
	}
	if len(lab) != 3 || len(order) != 5 {
		t.Fatalf("accounts %q with lab-a's at %v, want five with three of lab-a's", before, lab)
	}
	refused := order[lab[1]]
	scope := limitScope(file.ConnectionFor(refused), refused)
	p.tipsLimit(scope, refused.ID(), time.Now())
	want := slices.Clone(before)
	want[lab[0]], want[lab[1]], want[lab[2]] = before[lab[1]], before[lab[2]], before[lab[0]]
	if got := ids(p.tipsOrder(file)); !slices.Equal(got, want) {
		t.Fatalf("order after lab-a refused %s = %q, want %q", refused.ID(), got, want)
	}
	if _, due := p.tipsDue(refused.ID(), scope, nil, time.Hour); !due {
		t.Fatal("the refused account's look is not due past the limit")
	}
	if got := ids(p.tipsOrder(file)); !slices.Equal(got, before) {
		t.Fatalf("order after the refused account's look = %q, want %q", got, before)
	}
}

// TestRateLimitedFallbackIsTunable: a 429 that names no wait waits tuning's
// Poller.ForgeRateLimitWait as it is when the poll reads it.
func TestRateLimitedFallbackIsTunable(t *testing.T) {
	tuning.Override(t, func(v *tuning.Values) { v.Poller.ForgeRateLimitWait = 7 * time.Minute })
	err := &gl.ErrorResponse{StatusCode: http.StatusTooManyRequests, Response: &http.Response{StatusCode: http.StatusTooManyRequests, Header: http.Header{}}}
	if wait, limited := rateLimited(err, time.Now()); wait != 7*time.Minute || !limited {
		t.Fatalf("rateLimited = %s %t, want the overridden 7m", wait, limited)
	}
}
