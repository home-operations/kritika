package webapi

import (
	"time"

	"github.com/home-operations/kritika/internal/review"
	"github.com/home-operations/kritika/internal/store"
)

// The shapes of the analytics page, which routes_analytics.go serves.

// SeverityCounts counts findings by severity.
type SeverityCounts struct {
	P0 int `json:"p0"`
	P1 int `json:"p1"`
	P2 int `json:"p2"`
}

// AnalyticsTotals is what the account's reviews came to over a window:
// completed reviews and the pull requests they reviewed, failed reviews, and
// the findings first reported in it and how many were addressed since. It
// also holds the reactions to those posted inline, spend, the median time a
// completed review took, and the median time from opening to merging.
type AnalyticsTotals struct {
	PullRequests int            `json:"pullRequests"`
	Reviews      int            `json:"reviews"`
	Failed       int            `json:"failed"`
	Findings     SeverityCounts `json:"findings"`
	// Categories counts the same findings by category, every category
	// present.
	Categories     map[review.Category]int `json:"categories"`
	Addressed      int                     `json:"addressed"`
	ReactionsUp    int                     `json:"reactionsUp"`
	ReactionsDown  int                     `json:"reactionsDown"`
	Tokens         int64                   `json:"tokens"`
	CostUSD        float64                 `json:"costUsd"`
	UnpricedCalls  int64                   `json:"unpricedCalls,omitzero"`
	MedianReviewMs *int64                  `json:"medianReviewMs"`
	MedianMergeMs  *int64                  `json:"medianMergeMs"`
}

// AnalyticsPoint is one bucket of the series, keyed by its first date.
type AnalyticsPoint struct {
	Key            string         `json:"key"`
	Reviews        int            `json:"reviews"`
	Failed         int            `json:"failed"`
	Tokens         int64          `json:"tokens"`
	MedianReviewMs *int64         `json:"medianReviewMs"`
	Findings       SeverityCounts `json:"findings"`
	CostUSD        float64        `json:"costUsd"`
}

// RepoActivity is what one repository's reviews came to over the window.
type RepoActivity struct {
	Repository string `json:"repository"`
	// Account is set in the instance's list, where a row can be any readable
	// account's.
	Account   string         `json:"account,omitzero"`
	Reviews   int            `json:"reviews"`
	Findings  SeverityCounts `json:"findings"`
	Addressed int            `json:"addressed"`
}

// EgressRefusal is how often the gateway refused one host over the
// window, to the runs of one repository, by reason. The host is what the
// agent asked for, a name steered by the content under review: text, not
// a link. A run belongs to one repository, so Runs add up across a
// host's rows.
type EgressRefusal struct {
	// Repository is the full name, "" for runs without one.
	Repository string `json:"repository"`
	// Account is set in the instance's list, where a row can be any readable
	// account's.
	Account string `json:"account,omitzero"`
	Host    string `json:"host"`
	// Count is the refusals and Runs the distinct runs they came from.
	Count int64 `json:"count"`
	Runs  int64 `json:"runs"`
	// Reasons counts the refusals by reason.
	Reasons    map[string]int64 `json:"reasons"`
	LastSeenAt time.Time        `json:"lastSeenAt"`
}

// Analytics is the account's reviews in [From, To), against the window of the
// same length before it, bucketed by Group. It also holds the repositories
// with the most reviews and the hosts that the gateway refused its runs, the
// most refused first.
type Analytics struct {
	Group        store.AnalyticsGroup `json:"group"`
	From         time.Time            `json:"from"`
	To           time.Time            `json:"to"`
	Current      AnalyticsTotals      `json:"current"`
	Previous     AnalyticsTotals      `json:"previous"`
	Series       []AnalyticsPoint     `json:"series"`
	Repositories []RepoActivity       `json:"repositories"`
	Refusals     []EgressRefusal      `json:"refusals"`
}

// DependencyDrift is how one dependency's updates were rated over the
// window, from the newest scored review of each pull request that moved
// it: the risk the judge gave each update before any hold and the reach it
// gave the dependency, counted, with the last update's rating. A
// dependency rated at more levels than one drifted.
type DependencyDrift struct {
	Repository string `json:"repository"`
	// Path is the file that moves the dependency and Dependency the line,
	// with its version blanked, that names it there.
	Path       string `json:"path"`
	Dependency string `json:"dependency"`
	Bumps      int    `json:"bumps"`
	// Levels is how many risk levels the updates were rated at.
	Levels    int                  `json:"levels"`
	Risks     map[review.Risk]int  `json:"risks"`
	Reaches   map[review.Reach]int `json:"reaches"`
	LastAt    time.Time            `json:"lastAt"`
	LastRisk  review.Risk          `json:"lastRisk"`
	LastReach review.Reach         `json:"lastReach"`
	LastStep  review.Step          `json:"lastStep"`
}
