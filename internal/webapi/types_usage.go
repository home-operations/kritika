package webapi

import (
	"time"

	"github.com/home-operations/kritika/internal/store"
)

// The shapes of the usage series, which routes_usage.go serves.

// UsagePoint is one key of a usage series.
type UsagePoint struct {
	Key              string  `json:"key"`
	InputTokens      int64   `json:"inputTokens"`
	CacheReadTokens  int64   `json:"cacheReadTokens"`
	CacheWriteTokens int64   `json:"cacheWriteTokens"`
	OutputTokens     int64   `json:"outputTokens"`
	CostUSD          float64 `json:"costUsd"`
	Calls            int64   `json:"calls"`
}

// UsageSeries is the account's usage in [From, To) grouped by Group.
type UsageSeries struct {
	Group store.UsageGroup `json:"group"`
	From  time.Time        `json:"from"`
	To    time.Time        `json:"to"`
	Rows  []UsagePoint     `json:"rows"`
}
