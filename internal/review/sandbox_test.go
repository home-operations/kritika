package review

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"testing"
)

func TestPrintfAllocation(t *testing.T) {
	// items is a list whose bytes fit the call twice over but not three
	// times. So a width charged against its leaves instead of the scalar that
	// it pads refuses the call. deep is a small value nested past
	// maxMeasureDepth.
	items := make([]int, 12000)
	for i := range items {
		items[i] = i
	}
	var deep any = "leaf"
	for range maxMeasureDepth + 8 {
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
		{name: "width on a deeply nested value", src: `{{ printf "%3v" . }}`, data: deep, want: fmt.Sprintf("%3v", deep)},
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
			got, err := execute(t.Context(), tt.src, tt.data, MaxRenderBytes)
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
