package review

import (
	"errors"
	"math"
	"strconv"
	"strings"
	"testing"
)

func TestPrintfAllocation(t *testing.T) {
	for _, tt := range []struct {
		name, src, want string
		refused         bool
	}{
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
			got, err := execute(t.Context(), tt.src, nil, MaxRenderBytes)
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
