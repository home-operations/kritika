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
// completed reviews and the pull requests they reviewed, failed reviews,
// the findings first reported in it and how many were addressed since,
// the reactions to those posted inline, spend, the median time a
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
	CostUSD        float64                 `json:"costUsd"`
	UnpricedCalls  int64                   `json:"unpricedCalls,omitzero"`
	MedianReviewMs *int64                  `json:"medianReviewMs"`
	MedianMergeMs  *int64                  `json:"medianMergeMs"`
}

// AnalyticsPoint is one bucket of the series, keyed by its first date.
type AnalyticsPoint struct {
	Key      string         `json:"key"`
	Reviews  int            `json:"reviews"`
	Findings SeverityCounts `json:"findings"`
	CostUSD  float64        `json:"costUsd"`
}

// RepoActivity is what one repository's reviews came to over the window.
type RepoActivity struct {
	Repository string         `json:"repository"`
	Reviews    int            `json:"reviews"`
	Findings   SeverityCounts `json:"findings"`
	Addressed  int            `json:"addressed"`
}

// Analytics is the account's reviews in [From, To), against the window of
// the same length before it, bucketed by Group, and the repositories with
// the most reviews.
type Analytics struct {
	Group        store.AnalyticsGroup `json:"group"`
	From         time.Time            `json:"from"`
	To           time.Time            `json:"to"`
	Current      AnalyticsTotals      `json:"current"`
	Previous     AnalyticsTotals      `json:"previous"`
	Series       []AnalyticsPoint     `json:"series"`
	Repositories []RepoActivity       `json:"repositories"`
}
