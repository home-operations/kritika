package review

import (
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/home-operations/kritika/internal/tuning"
)

// TestNormalizeSummaryUnderAHugePraiseCap: a praise cap as large as an int
// goes sizes the praise from the answer, not from the cap, and keeps every
// item under it.
func TestNormalizeSummaryUnderAHugePraiseCap(t *testing.T) {
	tuning.Override(t, func(v *tuning.Values) { v.Review.MaxPraise = math.MaxInt })
	got := normalizeSummary(Summary{Take: "t", Praise: []string{"clear tests", "", "small diff"}}, ParseOptions{})
	if !slices.Equal(got.Praise, []string{"clear tests", "small diff"}) {
		t.Fatalf("praise = %q, want both items", got.Praise)
	}
	if got := normalizeSummary(Summary{Take: "t"}, ParseOptions{}); got.Praise == nil || len(got.Praise) != 0 {
		t.Fatalf("praise of none = %#v, want an empty list", got.Praise)
	}
}

// TestPraiseCountAgrees: the review's and the merge call's instructions ask
// for the praise count that their schemas allow, at the default and under
// an override.
func TestPraiseCountAgrees(t *testing.T) {
	for _, tt := range []struct {
		max  int
		word string
	}{{3, "three"}, {5, "five"}, {12, "12"}} {
		tuning.Override(t, func(v *tuning.Values) { v.Review.MaxPraise = tt.max })
		for name, prompt := range map[string]string{
			"review": SystemPrompt(nil, nil, nil, false, false, false, false),
			"merge":  MergeSystemPrompt(false),
		} {
			if !strings.Contains(prompt, "Praise lists\nat most "+tt.word+" specific things") || strings.Contains(prompt, praiseCap) {
				t.Errorf("%s instructions at %d do not ask for at most %s", name, tt.max, tt.word)
			}
		}
		for name, praise := range map[string]*jsonSchema{
			"review": contractSchema(false, false).Properties[keySummary].Properties[keyPraise],
			"merge":  summaryProperties(false)[keyPraise],
		} {
			if !strings.HasPrefix(praise.Description, "Up to "+tt.word+" specific things") || praise.MaxItems != tt.max {
				t.Errorf("%s schema at %d = %q, at most %d; want up to %s", name, tt.max, praise.Description, praise.MaxItems, tt.word)
			}
		}
	}
}
