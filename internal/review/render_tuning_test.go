package review

import (
	"strings"
	"testing"

	"github.com/home-operations/kritika/internal/tuning"
)

// TestRenderSummaryFollowsMaxRenderBytes: a repository's template is held to
// tuning's Review.MaxRenderBytes as the process has it, and the fallback is
// cut to it too.
func TestRenderSummaryFollowsMaxRenderBytes(t *testing.T) {
	tmpl := Templates{Summary: `{{ repeat 3000 "x" }}`}
	body, notes := RenderSummary(t.Context(), tmpl, sampleData())
	if len(notes) != 0 || !strings.Contains(body, strings.Repeat("x", 3000)) {
		t.Fatalf("at the default cap: notes %v, body of %d bytes; want the template's", notes, len(body))
	}
	tuning.Override(t, func(v *tuning.Values) { v.Review.MaxRenderBytes = 2 << 10 })
	body, notes = RenderSummary(t.Context(), tmpl, sampleData())
	if len(notes) != 1 || !strings.Contains(notes[0], "produced more than 2 KiB") {
		t.Fatalf("notes = %v, want the template refused as over 2 KiB", notes)
	}
	if len(body) > 2<<10 || !strings.HasPrefix(body, Marker(42)+"\n") {
		t.Fatalf("fallback body is %d bytes, want at most 2 KiB with the marker", len(body))
	}
}
