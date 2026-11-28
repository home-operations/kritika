package review

import (
	"fmt"
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

// TestIncompleteWarningSurvivesTheRenderFloor: at the least cap Validate
// takes, an incomplete review whose notes overflow the default template
// still warns that it is incomplete, with its reason cut and the notice on
// earlier heads' findings kept, within the cap.
func TestIncompleteWarningSurvivesTheRenderFloor(t *testing.T) {
	tuning.Override(t, func(v *tuning.Values) { v.Review.MaxRenderBytes = tuning.MinRenderBytes })
	d := RenderData{Number: 42, HeadSHA: "0123456789abcdef", Incomplete: "the run timed out " + strings.Repeat("waiting ", 200)}
	for range 40 {
		d.Notes = append(d.Notes, "a command setup failed with a long diagnostic")
	}
	if _, err := render(t.Context(), defaultSummary, d, bodyLimit(Marker(42)+"\n")); err == nil {
		t.Fatal("the notes no longer overflow the default template")
	}
	body, _ := RenderSummary(t.Context(), Templates{}, d)
	if len(body) > tuning.MinRenderBytes {
		t.Fatalf("summary of %d bytes, want at most %d", len(body), tuning.MinRenderBytes)
	}
	for _, want := range []string{Marker(42) + "\n", "**Review incomplete for `0123456`:** the run timed out",
		"Findings posted for earlier heads may no longer apply."} {
		if !strings.Contains(body, want) {
			t.Errorf("summary lost %q:\n%s", want, body)
		}
	}
}

// TestUnanchoredFindingsSurviveTheRenderFloor: at the least cap Validate
// takes, a review whose findings outside the diff overflow the default
// template still lists them, which no inline comment carries, ahead of the
// take, and counts those that do not fit, within the cap.
func TestUnanchoredFindingsSurviveTheRenderFloor(t *testing.T) {
	tuning.Override(t, func(v *tuning.Values) { v.Review.MaxRenderBytes = tuning.MinRenderBytes })
	d := RenderData{Number: 42, HeadSHA: "0123456789abcdef", Result: Result{Summary: Summary{Take: "- Adds the cache."}},
		PullURL: "https://kritika.example/#/pulls/42"}
	d.Unanchored = append(d.Unanchored, Finding{Path: "cache.go", Line: 7, Severity: SeverityP1, Title: "Stale entries are served",
		Explanation: strings.Repeat("why ", 500)})
	for i := range 30 {
		d.Unanchored = append(d.Unanchored, Finding{Path: "other.go", Line: i + 1, Severity: SeverityP2, Title: "A smaller point"})
	}
	if _, err := render(t.Context(), defaultSummary, d, bodyLimit(Marker(42)+"\n")); err == nil {
		t.Fatal("the findings no longer overflow the default template")
	}
	body, _ := RenderSummary(t.Context(), Templates{}, d)
	if len(body) > tuning.MinRenderBytes {
		t.Fatalf("summary of %d bytes, want at most %d", len(body), tuning.MinRenderBytes)
	}
	for _, want := range []string{"- **[P1]** `cache.go:7` Stale entries are served\n", "more, on [the dashboard](https://kritika.example/#/pulls/42)"} {
		if !strings.Contains(body, want) {
			t.Errorf("summary lost %q:\n%s", want, body)
		}
	}
	listed := strings.Count(body, "`other.go:")
	if want := fmt.Sprintf("- and %d more", 31-1-listed); !strings.Contains(body, want) {
		t.Errorf("summary lists %d of the 30 smaller findings but does not say %q:\n%s", listed, want, body)
	}
	d.Unanchored = d.Unanchored[:1]
	if body, _ := RenderSummary(t.Context(), Templates{}, d); !strings.Contains(body, "- Adds the cache.") || strings.Contains(body, "more") {
		t.Errorf("with one finding outside the diff, the take is lost or a count added:\n%s", body)
	}
}
