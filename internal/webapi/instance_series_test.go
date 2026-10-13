package webapi

import (
	"reflect"
	"testing"

	"github.com/home-operations/kritika/internal/review"
)

func TestAddSeriesSumsBucketsByKey(t *testing.T) {
	ms := int64(1500)
	a := []AnalyticsPoint{{Key: "2026-10-01", Reviews: 1, MedianReviewMs: &ms, Findings: SeverityCounts{P0: 1}}}
	b := []AnalyticsPoint{
		{Key: "2026-09-30", Reviews: 2, Tokens: 5, CostUSD: 0.5},
		{Key: "2026-10-01", Reviews: 3, Failed: 1, Tokens: 7, MedianReviewMs: &ms, Findings: SeverityCounts{P2: 2}, CostUSD: 0.25},
	}
	got := addSeries(addSeries(nil, a), b)
	want := []AnalyticsPoint{
		{Key: "2026-09-30", Reviews: 2, Tokens: 5, CostUSD: 0.5},
		{Key: "2026-10-01", Reviews: 4, Failed: 1, Tokens: 7, Findings: SeverityCounts{P0: 1, P2: 2}, CostUSD: 0.25},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestAddTotalsLeavesMediansOut(t *testing.T) {
	ms := int64(10)
	a := AnalyticsTotals{Reviews: 1, Categories: map[review.Category]int{review.Categories()[0]: 1}, MedianReviewMs: &ms, MedianMergeMs: &ms}
	b := AnalyticsTotals{Reviews: 2, Failed: 1, Categories: map[review.Category]int{review.Categories()[0]: 2}, MedianReviewMs: &ms}
	got := addTotals(a, b)
	if got.Reviews != 3 || got.Failed != 1 || got.MedianReviewMs != nil || got.MedianMergeMs != nil {
		t.Fatalf("got %+v", got)
	}
	if got.Categories[review.Categories()[0]] != 3 || len(got.Categories) != len(review.Categories()) {
		t.Fatalf("categories: %v", got.Categories)
	}
}

func TestAddUsageKeepsTheFirstAccountOfASharedKey(t *testing.T) {
	a := []UsagePoint{{Key: "", Calls: 1}, {Key: "rev-a", Account: "github/a", Calls: 2}}
	b := []UsagePoint{{Key: "", Calls: 3, CostUSD: 1}, {Key: "rev-b", Account: "github/b", Calls: 4}}
	got := addUsage(a, b)
	want := []UsagePoint{{Key: "", Calls: 4, CostUSD: 1}, {Key: "rev-a", Account: "github/a", Calls: 2}, {Key: "rev-b", Account: "github/b", Calls: 4}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestCollapseUsageIsOneRowPerAccountWithUsage(t *testing.T) {
	if got := collapseUsage(nil, "github/idle"); got != nil {
		t.Fatalf("idle account: %+v", got)
	}
	got := collapseUsage([]UsagePoint{{Key: "m1", InputTokens: 10, Calls: 1}, {Key: "m2", InputTokens: 5, CacheReadTokens: 2, Calls: 2, PlanCalls: 1}}, "github/a")
	want := []UsagePoint{{Key: "github/a", Account: "github/a", InputTokens: 15, CacheReadTokens: 2, Calls: 3, PlanCalls: 1}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}
