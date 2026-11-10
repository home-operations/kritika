package worker

import (
	"log/slog"
	"maps"
	"slices"
	"testing"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/review"
	"github.com/home-operations/kritika/internal/runspec"
)

// TestNewReviewSlots: the review's reviewers are pooled by model, each pool
// limited by its provider's concurrency, with the default pass alone in one
// pool when it runs alone.
func TestNewReviewSlots(t *testing.T) {
	file := &configfile.File{Providers: map[string]configfile.Provider{"wide": {Concurrency: 4}, "narrow": {Concurrency: 1}}}
	account := &configfile.Account{Forge: configfile.ForgeGitHub, Name: "acme"}
	settings := configfile.Settings{
		Models: configfile.Models{Review: "wide/big"},
		Reviewers: map[string]configfile.Reviewer{
			"security": {Model: "narrow/small"},
			"style":    {},
			"deps":     {Model: "narrow/small"},
		},
	}
	spec := func(names ...string) reviewers {
		var out reviewers
		for _, name := range names {
			out.specs = append(out.specs, runspec.Reviewer{Name: name})
		}
		return out
	}
	for _, tt := range []struct {
		name      string
		chosen    reviewers
		pools     map[string][]string
		limits    map[string]int
		parts     map[string]int
		reviewers map[string]string
	}{
		{
			name: "the default pass alone", chosen: reviewers{},
			pools: map[string][]string{"wide/big": {review.DefaultReviewer}}, limits: map[string]int{"wide/big": 4},
			parts: map[string]int{"wide/big": 3}, reviewers: map[string]string{review.DefaultReviewer: "wide/big"},
		},
		{
			name: "lenses pooled by model", chosen: spec(review.DefaultReviewer, "security", "style", "deps"),
			pools:  map[string][]string{"wide/big": {review.DefaultReviewer, "style"}, "narrow/small": {"deps", "security"}},
			limits: map[string]int{"wide/big": 4, "narrow/small": 1},
			parts:  map[string]int{"wide/big": 6, "narrow/small": 6},
			reviewers: map[string]string{
				review.DefaultReviewer: "wide/big", "style": "wide/big", "security": "narrow/small", "deps": "narrow/small",
			},
		},
		{
			name: "a lens asked for alone", chosen: spec("security"),
			pools: map[string][]string{"narrow/small": {"security"}}, limits: map[string]int{"narrow/small": 1},
			parts: map[string]int{"narrow/small": 3}, reviewers: map[string]string{"security": "narrow/small"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := newReviewSlots(&Review{}, slog.New(slog.DiscardHandler), file, account, settings, tt.chosen, 3, "run", 1)
			pools := map[string][]string{}
			limits := map[string]int{}
			models := make([]string, 0, len(s.pools))
			for _, pool := range s.pools {
				pools[pool.model], limits[pool.model] = pool.reviewers, pool.limit
				models = append(models, pool.model)
			}
			if !maps.EqualFunc(pools, tt.pools, slices.Equal) || !maps.Equal(limits, tt.limits) {
				t.Fatalf("pools = %v with limits %v, want %v with %v", pools, limits, tt.pools, tt.limits)
			}
			if !slices.IsSorted(models) {
				t.Fatalf("pools not sorted by model: %v", models)
			}
			if got := s.parts(); !maps.Equal(got, tt.parts) {
				t.Fatalf("parts = %v, want %v", got, tt.parts)
			}
			if got := s.poolOf(); !maps.Equal(got, tt.reviewers) {
				t.Fatalf("poolOf = %v, want %v", got, tt.reviewers)
			}
		})
	}
}
