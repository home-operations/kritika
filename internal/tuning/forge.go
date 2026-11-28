package tuning

import (
	"errors"
	"fmt"
	"time"
)

// Forge holds the tunable values of package forge.
type Forge struct {
	// MaxFileBytes bounds what a forge client's FileAt reads of one file,
	// the repository configuration's and a rule file's among them. GitHub's
	// contents API inlines a file only up to 1 MiB, so GitHub reads no more
	// than that whatever this says.
	MaxFileBytes ByteSize `env:"KRITIKA_TUNING_FORGE_MAX_FILE_BYTES" envDefault:"1Mi" yaml:"maxFileBytes,omitempty"`
}

// GitHub holds the tunable values of package forge/github.
type GitHub struct {
	// RateLimitMaxWait bounds how long a request waits out a rate limit
	// before it is sent again, and is the wait of a secondary limit that
	// names none. A longer wait is not worth holding a worker for. The
	// response goes back to go-github, which then refuses every request
	// until the reset without asking GitHub. It also goes back to the
	// caller, whose job River retries later.
	RateLimitMaxWait time.Duration `env:"KRITIKA_TUNING_GITHUB_RATE_LIMIT_MAX_WAIT" envDefault:"1m" yaml:"rateLimitMaxWait,omitempty"`
	// ResponseHeaderTimeout bounds how long a request waits for GitHub to
	// start answering. The leader's poll runs on a context with no
	// deadline, and a worker's job on one of hours. Otherwise, a server
	// that accepts a request and never answers holds either for as long.
	// The shared transport reads it once, on first use.
	ResponseHeaderTimeout time.Duration `env:"KRITIKA_TUNING_GITHUB_RESPONSE_HEADER_TIMEOUT" envDefault:"1m" yaml:"responseHeaderTimeout,omitempty"` //nolint:lll // a struct tag does not wrap
	// ErrorBodyWait bounds how long the body of an answer that is no
	// success, or is a 202, is read for once its headers arrive. go-github
	// reads such a body to its end to tell its errors apart, a 202's to
	// build its AcceptedError, and a body that stalls would hold the
	// request until its context ends: a worker's job hours on, the
	// leader's poll never. An error's body is a few hundred bytes. Cut off,
	// it leaves the status and the headers, by which a primary rate limit
	// is still told apart; a secondary limit that only its body names is
	// then a plain refusal.
	ErrorBodyWait time.Duration `env:"KRITIKA_TUNING_GITHUB_ERROR_BODY_WAIT" envDefault:"10s" yaml:"errorBodyWait,omitempty"`
	// MaxRedirects is how many redirects, all on the API's origin, a
	// request follows.
	MaxRedirects int `env:"KRITIKA_TUNING_GITHUB_MAX_REDIRECTS" envDefault:"10" yaml:"maxRedirects,omitempty"`
	// AppJWTLifetime is how long an App JWT lives. At most GitHub's cap of
	// ten minutes, and longer than TokenRefreshLead.
	AppJWTLifetime time.Duration `env:"KRITIKA_TUNING_GITHUB_APP_JWT_LIFETIME" envDefault:"9m" yaml:"appJwtLifetime,omitempty"`
	// AppJWTBackdate is how far before now an App JWT says it was issued,
	// to absorb minor clock skew.
	AppJWTBackdate time.Duration `env:"KRITIKA_TUNING_GITHUB_APP_JWT_BACKDATE" envDefault:"30s" yaml:"appJwtBackdate,omitempty"`
	// TokenRefreshLead is how long before it expires an App JWT or an
	// installation token is replaced, so a request never goes out with one
	// about to lapse.
	TokenRefreshLead time.Duration `env:"KRITIKA_TUNING_GITHUB_TOKEN_REFRESH_LEAD" envDefault:"1m" yaml:"tokenRefreshLead,omitempty"`
	// MaxGraphQLBody bounds a GraphQL answer.
	MaxGraphQLBody ByteSize `env:"KRITIKA_TUNING_GITHUB_MAX_GRAPH_QL_BODY" envDefault:"4Mi" yaml:"maxGraphQlBody,omitempty"`
	// MaxGraphQLErrorBytes bounds how much of a refused GraphQL answer's
	// body the error carries.
	MaxGraphQLErrorBytes ByteSize `env:"KRITIKA_TUNING_GITHUB_MAX_GRAPH_QL_ERROR_BYTES" envDefault:"1Ki" yaml:"maxGraphQlErrorBytes,omitempty"`
	// MaxListedComments is how many comments a listing of a pull request's
	// conversation or inline comments returns, the oldest: a pull request
	// with more is not read to its end into memory.
	MaxListedComments int `env:"KRITIKA_TUNING_GITHUB_MAX_LISTED_COMMENTS" envDefault:"5000" yaml:"maxListedComments,omitempty"`
}

// GitLab holds the tunable values of package forge/gitlab.
type GitLab struct {
	// PageSize is how many items a paginated listing asks for per page.
	// GitLab serves at most 100, the default.
	PageSize int `env:"KRITIKA_TUNING_GITLAB_PAGE_SIZE" envDefault:"100" yaml:"pageSize,omitempty"`
	// MaxDiffBytes bounds a merge request's diff read whole.
	MaxDiffBytes ByteSize `env:"KRITIKA_TUNING_GITLAB_MAX_DIFF_BYTES" envDefault:"8Mi" yaml:"maxDiffBytes,omitempty"`
	// DiscardTimeout bounds the discarding of a failed review's drafts,
	// which runs on even after the review's context ends.
	DiscardTimeout time.Duration `env:"KRITIKA_TUNING_GITLAB_DISCARD_TIMEOUT" envDefault:"30s" yaml:"discardTimeout,omitempty"`
}

// Forgejo holds the tunable values of package forge/forgejo.
type Forgejo struct {
	// TokenTTL is how long a token that kritika calls the API with lasts.
	// Longer than TokenReuseMargin.
	TokenTTL time.Duration `env:"KRITIKA_TUNING_FORGEJO_TOKEN_TTL" envDefault:"10m" yaml:"tokenTtl,omitempty"`
	// RunnerTokenTTL is how long a token that a runner fetches with lasts.
	// That token is read once a run starts, but it can wait in a queue
	// first.
	RunnerTokenTTL time.Duration `env:"KRITIKA_TUNING_FORGEJO_RUNNER_TOKEN_TTL" envDefault:"1h" yaml:"runnerTokenTtl,omitempty"`
	// TokenSkew is how far a token's issue and not-before times are set
	// back. Forgejo refuses a token issued in its future and allows no
	// leeway.
	TokenSkew time.Duration `env:"KRITIKA_TUNING_FORGEJO_TOKEN_SKEW" envDefault:"1m" yaml:"tokenSkew,omitempty"`
	// TokenReuseMargin is how long a token must have left to be used
	// again rather than replaced.
	TokenReuseMargin time.Duration `env:"KRITIKA_TUNING_FORGEJO_TOKEN_REUSE_MARGIN" envDefault:"1m" yaml:"tokenReuseMargin,omitempty"`
	// MaxErrorBody bounds how much of an error response an API error
	// quotes.
	MaxErrorBody ByteSize `env:"KRITIKA_TUNING_FORGEJO_MAX_ERROR_BODY" envDefault:"512" yaml:"maxErrorBody,omitempty"`
	// MaxDiffBytes bounds a pull request's diff read whole.
	MaxDiffBytes ByteSize `env:"KRITIKA_TUNING_FORGEJO_MAX_DIFF_BYTES" envDefault:"8Mi" yaml:"maxDiffBytes,omitempty"`
	// PageSize is how many items a paginated listing asks for per page. The
	// instance may cap it, so a short page does not end a listing: its
	// Link or X-Total-Count headers do, or else an empty page.
	PageSize int `env:"KRITIKA_TUNING_FORGEJO_PAGE_SIZE" envDefault:"50" yaml:"pageSize,omitempty"`
	// MaxRedirects is how many redirects a request to the instance follows
	// before it fails as a redirect loop.
	MaxRedirects int `env:"KRITIKA_TUNING_FORGEJO_MAX_REDIRECTS" envDefault:"10" yaml:"maxRedirects,omitempty"`
}

// Webhook holds the tunable values of package webhook.
type Webhook struct {
	// MaxBody bounds a delivery's payload, read and parsed. GitHub caps
	// deliveries at 25 MB. A pull request event is a few hundred kilobytes
	// at most.
	MaxBody ByteSize `env:"KRITIKA_TUNING_WEBHOOK_MAX_BODY" envDefault:"4Mi" yaml:"maxBody,omitempty"`
	// GitLabTolerance is how far a GitLab delivery's timestamp can be from
	// now: the signature covers it, so an older one is a replay.
	GitLabTolerance time.Duration `env:"KRITIKA_TUNING_WEBHOOK_GIT_LAB_TOLERANCE" envDefault:"5m" yaml:"gitLabTolerance,omitempty"`
}

func init() {
	crossField = append(crossField,
		func(v Values) error {
			if v.GitHub.AppJWTLifetime > GitHubMaxJWTLifetime {
				return fmt.Errorf("%s %w GitHub's cap, %s", fieldLabel("gitHub.appJwtLifetime"), errMustNotExceed, GitHubMaxJWTLifetime)
			}
			return nil
		},
		func(v Values) error {
			return exceeds("gitHub.appJwtLifetime", v.GitHub.AppJWTLifetime, "gitHub.tokenRefreshLead", v.GitHub.TokenRefreshLead)
		},
		func(v Values) error {
			return exceeds("forgejo.tokenTtl", v.Forgejo.TokenTTL, "forgejo.tokenReuseMargin", v.Forgejo.TokenReuseMargin)
		},
		func(v Values) error {
			// A token's expiry is whole seconds, rounded down.
			return errors.Join(
				atLeastSecond("gitHub.appJwtLifetime", v.GitHub.AppJWTLifetime),
				atLeastSecond("forgejo.tokenTtl", v.Forgejo.TokenTTL),
				atLeastSecond("forgejo.runnerTokenTtl", v.Forgejo.RunnerTokenTTL),
			)
		},
	)
}
