package poller

import (
	"cmp"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"time"

	gh "github.com/google/go-github/v93/github"

	"github.com/home-operations/kritika/internal/configfile"
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

// tipsOrder is the order that a look takes the file's accounts in. It starts
// at the account that the last look ran out of time before reaching, or after
// the one it ran out of time in. So an account that takes a look's whole
// interval, every time, does not keep the accounts after it from their turn.
// Among the accounts whose lookups one credential's rate limit counts, as
// limitScope names them, it starts at the one whose lookup the limit refused
// last, until that one's look goes ahead. So an account earlier in the file,
// due at every look, does not spend the quota at every reset before a later
// one is asked for again. The other accounts keep their places.
func (p *Poller) tipsOrder(file *configfile.File) []*configfile.Account {
	n := len(file.Accounts)
	p.mu.Lock()
	defer p.mu.Unlock()
	first := max(slices.IndexFunc(file.Accounts, func(a configfile.Account) bool { return a.ID() == p.tipsFirst }), 0)
	order := make([]*configfile.Account, n)
	places := map[string][]int{}
	for i := range n {
		order[i] = &file.Accounts[(first+i)%n]
		if in := file.ConnectionFor(order[i]); in != nil {
			scope := limitScope(in, order[i])
			places[scope] = append(places[scope], i)
		}
	}
	for scope, at := range places {
		k := slices.IndexFunc(at, func(i int) bool { return order[i].ID() == p.tipsRefused[scope] })
		if k <= 0 {
			continue
		}
		turned := make([]*configfile.Account, len(at))
		for j := range at {
			turned[j] = order[at[(k+j)%len(at)]]
		}
		for j, i := range at {
			order[i] = turned[j]
		}
	}
	return order
}
