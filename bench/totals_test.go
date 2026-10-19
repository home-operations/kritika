//go:build bench

package bench

import (
	"testing"
	"time"
)

func TestTotalsCountFailedCases(t *testing.T) {
	results := []caseResult{
		{Mode: "diff", MustTotal: 1, MustHit: 1, Input: 100, Output: 50, CostUSD: 0.25, Latency: time.Second},
		{Mode: "diff", Error: "output budget cut off submit_review", Input: 100, Output: 2048, CostUSD: 0.5, Latency: time.Second},
		{Mode: "context", Input: 1, Output: 1},
	}
	got := totals(results, "diff")
	want := modeTotals{Cases: 2, Errors: 1, MustRecall: 1, CostUSD: 0.75, MeanLatency: 1, InputTokens: 200, OutputTokens: 2098}
	if got != want {
		t.Fatalf("totals = %+v, want %+v", got, want)
	}
}
