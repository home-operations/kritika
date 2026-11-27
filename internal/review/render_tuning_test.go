package review

import (
	"math"
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

// TestMinRenderBytesHoldsTheMarkers: tuning's MinRenderBytes, the least
// Validate takes, has room for the longest marker each renderer leads with
// and a body after it.
func TestMinRenderBytesHoldsTheMarkers(t *testing.T) {
	inline := FindingMarker(strings.Repeat("f", 64)) + "\n"
	sticky := Marker(math.MinInt) + "\n"
	if len(inline) != 90 {
		t.Errorf("an inline marker is %d bytes, which MinRenderBytes' doc gives as 90", len(inline))
	}
	for _, m := range []string{inline, sticky} {
		if len(m) >= tuning.MinRenderBytes/2 {
			t.Errorf("marker %q leaves less than half of MinRenderBytes, %d, to the body", m, tuning.MinRenderBytes)
		}
	}
}

// TestRenderAtTheRenderFloor: at the least cap Validate takes, and below
// it as only a test can set, both renderers keep the whole marker in front
// and fall back without a panic when the template is too large.
func TestRenderAtTheRenderFloor(t *testing.T) {
	tmpl := Templates{Summary: `{{ repeat 3000 "x" }}`, Inline: `{{ repeat 3000 "x" }}`}
	f := InlineData{Finding: sampleData().Result.Findings[0]}
	for _, limit := range []tuning.ByteSize{tuning.MinRenderBytes, 16} {
		tuning.Override(t, func(v *tuning.Values) { v.Review.MaxRenderBytes = limit })
		body, notes := RenderSummary(t.Context(), tmpl, sampleData())
		if len(notes) != 1 || !strings.HasPrefix(body, Marker(42)+"\n") {
			t.Errorf("summary at %d bytes: notes %v, body %q; want the fallback after the marker", limit, notes, body)
		}
		inline, notes := RenderInline(t.Context(), tmpl, f)
		if len(notes) != 1 || !strings.HasPrefix(inline, FindingMarker(Fingerprint(f.Finding))+"\n") {
			t.Errorf("inline at %d bytes: notes %v, body %q; want the fallback after the marker", limit, notes, inline)
		}
		if limit == tuning.MinRenderBytes && (len(body) > int(limit) || len(inline) > int(limit)) {
			t.Errorf("at the floor, bodies of %d and %d bytes exceed %d", len(body), len(inline), limit)
		}
	}
}

// TestFallbackNoteNamesTheCap: the note on a template that produced too
// much names the cap as set: in KiB where it is a whole number of them,
// and in bytes where it is not.
func TestFallbackNoteNamesTheCap(t *testing.T) {
	for limit, want := range map[tuning.ByteSize]string{64 << 10: "more than 64 KiB", 1500: "more than 1500 bytes", 2 << 10: "more than 2 KiB"} {
		tuning.Override(t, func(v *tuning.Values) { v.Review.MaxRenderBytes = limit })
		if got := fallbackNote("summary", errTooLarge); !strings.Contains(got, want) {
			t.Errorf("at %d bytes, note = %q, want %q", limit, got, want)
		}
	}
}
