package webapi

import (
	"time"

	"github.com/home-operations/kritika/internal/store"
)

// The shapes of the usage series, which routes_usage.go serves.

// UsagePoint is one key of a usage series.
type UsagePoint struct {
	Key string `json:"key"`
	// Account is set in the instance's series for keys of one account:
	// a review run, or the account itself when grouped by account.
	Account          string  `json:"account,omitzero"`
	InputTokens      int64   `json:"inputTokens"`
	CacheReadTokens  int64   `json:"cacheReadTokens"`
	CacheWriteTokens int64   `json:"cacheWriteTokens"`
	OutputTokens     int64   `json:"outputTokens"`
	CostUSD          float64 `json:"costUsd"`
	UnpricedCalls    int64   `json:"unpricedCalls,omitzero"`
	Calls            int64   `json:"calls"`
	PlanCalls        int64   `json:"planCalls,omitzero"`
}

// UsageSeries is the account's usage in [From, To) grouped by Group.
type UsageSeries struct {
	Group store.UsageGroup `json:"group"`
	From  time.Time        `json:"from"`
	To    time.Time        `json:"to"`
	Rows  []UsagePoint     `json:"rows"`
	// Previous uses the same filters over the preceding window of equal
	// length. Time and review groups return one total. Named groups return
	// per-key comparisons. Cache detail is omitted.
	Previous []UsagePoint `json:"previous"`
}
