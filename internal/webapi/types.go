package webapi

import (
	"time"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/review"
	"github.com/home-operations/kritika/internal/store"
)

// Every JSON shape the dashboard API returns. internal/web/src/lib/types.ts
// mirrors these field for field; testdata/*.golden.json pins the names.

// Page is one page of a keyset-paginated list; NextCursor is null on the
// last page.
type Page[T any] struct {
	Items      []T     `json:"items"`
	NextCursor *string `json:"nextCursor"`
}

// User is the signed-in human.
type User struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	Email       string `json:"email"`
	AvatarURL   string `json:"avatarUrl"`
}

// Me is who the session acts as: an admin, who reads and changes
// everything, or a member of Accounts, the slugs ("<forge>/<name>") of the
// accounts it may read.
type Me struct {
	User     User         `json:"user"`
	Admin    bool         `json:"admin"`
	Accounts []string     `json:"accounts"`
	Settings UserSettings `json:"settings"`
}

// UserSettings is what a user chose for their own dashboard; an empty
// field leaves the choice to the browser. TimeZone is an IANA zone name,
// Clock "12" or "24", Theme "light" or "dark".
type UserSettings struct {
	TimeZone string `json:"timeZone"`
	Clock    string `json:"clock"`
	Theme    string `json:"theme"`
}

// MonthUsage is an account's usage against its caps (a zero cap is unset),
// and the reviews that completed this month: their cost together, and the
// median cost of one, null when none completed.
type MonthUsage struct {
	Tokens              int64    `json:"tokens"`
	CostUSD             float64  `json:"costUsd"`
	TokensPerMonth      int64    `json:"tokensPerMonth"`
	ReviewsToday        int64    `json:"reviewsToday"`
	ReviewsPerDay       int      `json:"reviewsPerDay"`
	Reviews             int64    `json:"reviews"`
	ReviewCostUSD       float64  `json:"reviewCostUsd"`
	MedianReviewCostUSD *float64 `json:"medianReviewCostUsd"`
}

// AccountSummary is one row of the account list. Connection names the
// connection serving it. Attention and the three times are the account's
// health: what wants a look, when the connection's webhook last delivered,
// verified and unsigned, and when the account was last polled, each null
// for never.
type AccountSummary struct {
	Slug                  string     `json:"slug"`
	Connection            string     `json:"connection"`
	Repositories          int        `json:"repositories"`
	Reviews7d             int        `json:"reviews7d"`
	Usage                 MonthUsage `json:"usage"`
	Attention             Attention  `json:"attention"`
	LastWebhookAt         *time.Time `json:"lastWebhookAt"`
	LastUnsignedWebhookAt *time.Time `json:"lastUnsignedWebhookAt"`
	LastPolledAt          *time.Time `json:"lastPolledAt"`
}

// AdminAccount is one account as an admin sees it: Live is false for an
// entry of the instance spec that no connection serves, which Conflict
// explains.
type AdminAccount struct {
	AccountSummary
	Live     bool   `json:"live"`
	Conflict string `json:"conflict,omitempty"`
}

// InstanceSetting is one instance-wide setting as the admin console shows
// it, read-only: its value, and whether it comes from this process's
// environment, the configuration file, the dashboard or a built-in
// default. A secret shows only whether it is set.
type InstanceSetting struct {
	Section string            `json:"section"`
	Key     string            `json:"key"`
	Value   string            `json:"value"`
	Source  configfile.Source `json:"source"`
}

// CredentialsSet says which of a connection's secrets resolve to a
// value; the values themselves are never exposed.
type CredentialsSet struct {
	ClientID      bool `json:"clientId"`
	PrivateKey    bool `json:"privateKey"`
	WebhookSecret bool `json:"webhookSecret"`
}

// Connection is one GitHub App and the accounts it serves. HookPath is
// where its webhook arrives, under the dashboard's URL.
type Connection struct {
	Name        string           `json:"name"`
	Forge       configfile.Forge `json:"forge"`
	Accounts    []string         `json:"accounts"`
	Credentials CredentialsSet   `json:"credentials"`
	HookPath    string           `json:"hookPath"`
	// LastWebhookAt is when a webhook for the connection last passed
	// signature verification, to the minute; null when none ever has, and
	// kritika only polls it.
	LastWebhookAt *time.Time `json:"lastWebhookAt"`
	// LastUnsignedWebhookAt is when one last arrived with no signature, to
	// the minute, which a GitHub App with no webhook secret sends; null when
	// none has.
	LastUnsignedWebhookAt *time.Time `json:"lastUnsignedWebhookAt"`
}

// AccountDetail is one account's configuration and usage, and the
// connection serving it.
type AccountDetail struct {
	Slug       string            `json:"slug"`
	Connection Connection        `json:"connection"`
	Models     configfile.Models `json:"models"`
	Limits     configfile.Limits `json:"limits"`
	// Filters are the conditions that decide which pull requests are
	// reviewed.
	Filters configfile.Filters `json:"filters"`
	Usage   MonthUsage         `json:"usage"`
	// LastPolledAt is when kritika last polled the account for pull
	// requests, null when it never has: what stands in for a webhook that
	// does not arrive.
	LastPolledAt *time.Time `json:"lastPolledAt"`
}

// IndexState is a repository's embedding index: the active generation and
// the newest run. Empty strings and nulls mean none.
type IndexState struct {
	ActiveCommit  string               `json:"activeCommit"`
	ActiveAt      *time.Time           `json:"activeAt"`
	LastRunStatus store.IndexRunStatus `json:"lastRunStatus"`
	LastRunAt     *time.Time           `json:"lastRunAt"`
}

// ReviewRef is a review's id, status and start.
type ReviewRef struct {
	ID        string             `json:"id"`
	Status    store.ReviewStatus `json:"status"`
	CreatedAt time.Time          `json:"createdAt"`
}

// Repository is one row of the repository list.
type Repository struct {
	ID            string `json:"id"`
	FullName      string `json:"fullName"`
	Enabled       bool   `json:"enabled"`
	ManagedBy     string `json:"managedBy"`
	DefaultBranch string `json:"defaultBranch"`
	Archived      bool   `json:"archived"`
	Fork          bool   `json:"fork"`
	// TurnedOn is the choice an admin made in the dashboard, null while
	// the configuration decides whether the repository runs.
	TurnedOn   *bool      `json:"turnedOn"`
	Index      IndexState `json:"index"`
	LastReview *ReviewRef `json:"lastReview"`
}

// RepoSettings are a repository's settings as they resolve: the
// admin's, or with the in-repo .kritika.yaml applied (RepoConfig). The
// blocks are the configuration's own resolved types; durations are served
// in whole seconds.
type RepoSettings struct {
	Enabled        bool                     `json:"enabled"`
	Models         configfile.Models        `json:"models"`
	Filters        configfile.Filters       `json:"filters"`
	Ignore         []string                 `json:"ignore"`
	SettleSeconds  int64                    `json:"settleSeconds"`
	MaxAutoReviews int                      `json:"maxAutoReviews"`
	MaxDeltaFiles  int                      `json:"maxDeltaFiles"`
	Review         configfile.Review        `json:"review"`
	Confidence     configfile.Confidence    `json:"confidence"`
	Skills         configfile.Skills        `json:"skills"`
	Agent          configfile.AgentSettings `json:"agent"`
	Limits         configfile.Limits        `json:"limits"`
}

// RepoConfig is the repository's .kritika.yaml as the last review that ran
// read it, applied to the admin's settings as they are now.
type RepoConfig struct {
	ReviewID string `json:"reviewId"`
	// Commit is the merge base the review read the file at.
	Commit string `json:"commit"`
	// Found is false when there was no file there.
	Found bool `json:"found"`
	// Settings are the repository's settings with the file applied.
	Settings RepoSettings `json:"settings"`
	// Filters are the file's own conditions, passed beside the admin's.
	Filters configfile.Filters `json:"filters"`
	// Dropped are the file's values outside the admin's bounds; the
	// admin's value applies for each.
	Dropped []string `json:"dropped"`
	// Ignored is why the file was ignored as a whole, when it was.
	Ignored string `json:"ignored,omitempty"`
}

// IndexRun is one index generation or step.
type IndexRun struct {
	ID         string               `json:"id"`
	Repository string               `json:"repository"`
	CommitSHA  string               `json:"commitSha"`
	BaseSHA    string               `json:"baseSha"`
	EmbedModel string               `json:"embedModel"`
	Mode       string               `json:"mode"`
	Status     store.IndexRunStatus `json:"status"`
	Trigger    string               `json:"trigger"`
	ChunkCount int                  `json:"chunkCount"`
	Error      string               `json:"error"`
	CreatedAt  time.Time            `json:"createdAt"`
	FinishedAt *time.Time           `json:"finishedAt"`
}

// RepoDetail is one repository, its settings and recent index runs.
// Sources says, by the policy table's keys, which layer each of the
// admin's settings comes from: the defaults, the account (its entry or the
// repository's entry in it) or kritika's default. RepoConfig is null until a
// review has read the repository's .kritika.yaml.
type RepoDetail struct {
	Repository
	Settings   RepoSettings                 `json:"settings"`
	Sources    map[string]configfile.Source `json:"sources"`
	RepoConfig *RepoConfig                  `json:"repoConfig"`
	IndexRuns  []IndexRun                   `json:"indexRuns"`
}

// Label is a pull request label.
type Label struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}

// Review is one review pass as lists show it. DurationMs is null while
// the review runs.
type Review struct {
	ID         string             `json:"id"`
	Status     store.ReviewStatus `json:"status"`
	Trigger    string             `json:"trigger"`
	Scope      review.Scope       `json:"scope"`
	Model      string             `json:"model"`
	HeadSHA    string             `json:"headSha"`
	CostUSD    float64            `json:"costUsd"`
	Tokens     TokenCounts        `json:"tokens"`
	DurationMs *int64             `json:"durationMs"`
	CreatedAt  time.Time          `json:"createdAt"`
	FinishedAt *time.Time         `json:"finishedAt"`
	// SkipReason is why a skipped review was: disabled, filtered or
	// only_skipped_paths from the repository's configuration, or the
	// runner's unchanged_patch or too_large.
	SkipReason string `json:"skipReason"`
	Error      string `json:"error"`
	// Confidence is null for a review that was not scored.
	Confidence *Confidence `json:"confidence"`
}

// Usage counts one or more model calls' tokens.
type Usage struct {
	Input      int64 `json:"input"`
	CacheRead  int64 `json:"cacheRead"`
	CacheWrite int64 `json:"cacheWrite"`
	Output     int64 `json:"output"`
}

// Event is one server-sent event's data: a row of Kind changed in the
// account with this slug.
type Event struct {
	Kind     store.EventKind `json:"kind"`
	Account  string          `json:"account"`
	ID       string          `json:"id"`
	ReviewID *string         `json:"reviewId"`
}

// Resync is the data of the resync event, which opens every stream and
// stands for events the client missed: it refetches everything it shows.
type Resync struct {
	// Entry is the path of the dashboard's entry script, as the build
	// manifest names it, "" when the server serves a UI built without one.
	// A tab that booted from another script runs another build of the
	// dashboard, and reloads instead.
	Entry string `json:"entry"`
}

// ErrorBody is every API error.
type ErrorBody struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
}
