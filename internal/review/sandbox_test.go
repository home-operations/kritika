package review

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/home-operations/kritika/internal/tuning"
)

func TestPrintfAllocation(t *testing.T) {
	// items is a list whose bytes fit the call twice over but not three
	// times. So a width charged against its leaves instead of the scalar that
	// it pads refuses the call. deep is a small value nested past
	// tuning's Review.MaxMeasureDepth, which cannot be measured and so is
	// refused.
	items := make([]int, 12000)
	for i := range items {
		items[i] = i
	}
	var deep any = "leaf"
	for range tuning.Get().Review.MaxMeasureDepth + 8 {
		deep = []any{deep}
	}
	for _, tt := range []struct {
		name, src, want string
		data            any
		refused         bool
	}{
		{name: "width on a scalar beside a composite", src: `{{ printf "%5d items: %v" 3 .Items }}`,
			data: map[string]any{"Items": items}, want: fmt.Sprintf("%5d items: %v", 3, items)},
		{name: "width on a composite", src: `{{ printf "%25v" .Items }}`, data: map[string]any{"Items": items}, refused: true},
		{name: "width on a deeply nested value", src: `{{ printf "%3v" . }}`, data: deep, refused: true},
		{name: "index out of range", src: `{{ printf "%[3]s|%s" "a" "b" }}`, want: "%!s(BADINDEX)|a"},
		{name: "indexed width", src: `{{ printf "%[1]*[2]s" 10 "x" }}`, refused: true},
		{name: "indexed precision", src: `{{ printf "%.[1]*[2]f" 2 1.25 }}`, refused: true},
		{name: "reordered width", src: `{{ printf "%[2]*[1]s" "x" 10 }}`, refused: true},
		{name: "width after escaped percent", src: `{{ printf "%%%[1]*[2]s" 10 "x" }}`, refused: true},
		{name: "overflowing widths", src: `{{ printf "%1s%` + strconv.Itoa(math.MaxInt) + `s" "a" "b" }}`, refused: true},
		{name: "reused argument", src: `{{ printf (repeat 4 "%[1]s") (repeat 70000 "x") }}`, refused: true},
		{name: "padding each slice element", src: `{{ printf "%100000s" (list "a" "b" "c") }}`, refused: true},
		{name: "padding nested elements", src: `{{ printf "%90000v" (list (list "a" "b") (dict "k" "v")) }}`, refused: true},
		{name: "escaped indexed width", src: `{{ printf "%%[1]*[2]s" }}`, want: "%[1]*[2]s"},
		{name: "escaped width", src: `{{ printf "%%*s" }}`, want: "%*s"},
		{name: "escaped large width", src: `{{ printf "%%1000000s" }}`, want: "%1000000s"},
		{name: "positional arguments", src: `{{ printf "%[2]s %[1]s %[2]s" "a" "b" }}`, want: "b a b"},
		{name: "positional literal width", src: `{{ printf "%3[2]s %.2[1]f" 1.25 "x" }}`, want: "  x 1.25"},
		{name: "positional after escaped percent", src: `{{ printf "%%%[2]s" "a" "b" }}`, want: "%b"},
		{name: "ordinary composite", src: `{{ printf "%3s" (list "a" "b") }}`, want: "[  a   b]"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := execute(t.Context(), tt.src, tt.data, int(tuning.Get().Review.MaxRenderBytes))
			if tt.refused {
				if !errors.Is(err, errSandbox) || !strings.Contains(err.Error(), "printf:") ||
					!strings.Contains(err.Error(), "input and estimated allocation") {
					t.Fatalf("render = %q, %v; want printf refused before allocation", got, err)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("render = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

// TestIterationBudgetAccounting: the iteration guard charges a loop against
// what is left of the budget before it adds the charge, so that neither a
// budget as large as an int goes nor a charge as large as a uint64 goes
// wraps, and a run of loops is refused at the first that would pass it.
func TestIterationBudgetAccounting(t *testing.T) {
	tuning.Override(t, func(v *tuning.Values) { v.Review.MaxIterations = math.MaxInt })
	g := &guard{ctx: t.Context()}
	if _, err := g.iter(5); err != nil || g.iterations != 5 {
		t.Fatalf("a range of 5 under the largest budget: %v, %d charged", err, g.iterations)
	}
	if _, err := g.iter(uint64(math.MaxUint64)); !errors.Is(err, errSandbox) || g.iterations != 5 {
		t.Fatalf("a range past the largest budget: %v, %d charged; want it refused uncharged", err, g.iterations)
	}
	if _, err := g.iter(math.MaxInt - 5); err != nil || g.iterations != math.MaxInt {
		t.Fatalf("a range of what is left: %v, %d charged", err, g.iterations)
	}
	if _, err := g.iter(1); !errors.Is(err, errSandbox) {
		t.Fatalf("a range past a spent budget: %v", err)
	}

	tuning.Override(t, func(v *tuning.Values) { v.Review.MaxIterations = 10 })
	g = &guard{ctx: t.Context()}
	for i, tt := range []struct {
		n       any
		refused bool
	}{{4, false}, {-3, false}, {[]int{1, 2, 3}, false}, {"abcd", true}, {3, false}, {1, true}} {
		if _, err := g.iter(tt.n); errors.Is(err, errSandbox) != tt.refused {
			t.Fatalf("charge %d (%v): %v, want refused %v", i, tt.n, err, tt.refused)
		}
	}
	if g.iterations != 10 {
		t.Fatalf("charged %d, want the budget of 10 spent exactly", g.iterations)
	}
}

// TestMeasureAtTheDepthCutoff: at tuning's Review.MaxMeasureDepth, a string
// still counts its length and a composite with contents counts past the
// limit, so a low depth refuses what it cannot see rather than letting a
// large value through as small.
func TestMeasureAtTheDepthCutoff(t *testing.T) {
	tuning.Override(t, func(v *tuning.Values) { v.Review.MaxMeasureDepth = 2 })
	limit := tuning.Get().Review.MaxCallBytes()
	big := strings.Repeat("x", limit+1)
	for _, tt := range []struct {
		name string
		v    any
		want func(int) bool
	}{
		{"a string at the cutoff", []any{[]any{"abc"}}, func(n int) bool { return n == 2*nodeBytes+3 }},
		{"a large string at the cutoff", []any{[]any{big}}, func(n int) bool { return n > limit }},
		{"a list past the cutoff", []any{[]any{[]any{big}}}, func(n int) bool { return n > limit }},
		{"an empty list at the cutoff", []any{[]any{[]any{}}}, func(n int) bool { return n <= 3*nodeBytes }},
	} {
		if n := measure(reflect.ValueOf(tt.v), 0, limit); !tt.want(n) {
			t.Errorf("%s: measured %d bytes against a limit of %d", tt.name, n, limit)
		}
	}
	tmpl := Templates{Summary: `{{ printf "%v" .Notes }}`}
	d := sampleData()
	d.Notes = []string{"short"}
	tuning.Override(t, func(v *tuning.Values) { v.Review.MaxMeasureDepth = 0 })
	if _, notes := RenderSummary(t.Context(), tmpl, d); len(notes) != 1 {
		t.Fatalf("notes = %v; want a call on a value the depth cannot measure refused", notes)
	}
}
