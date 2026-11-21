package lexical

import (
	"slices"
	"testing"
)

// TestUnderscoreIdentifiersStayWhole: an underscore is part of a name, never
// an operator, so an identifier of underscores keeps its whole self even
// when its parts are a letter and digits each, as x86_64's are. A hyphen
// between single letters can be a minus, so a-b gives nothing.
func TestUnderscoreIdentifiersStayWhole(t *testing.T) {
	for _, tt := range []struct {
		text string
		want []string
	}{
		{"x86_64", []string{"x8664"}},
		{"arch_x86_64", []string{"archx8664", "arch"}},
		{"a_b", []string{"ab"}},
		{"a-b", nil},
	} {
		t.Run(tt.text, func(t *testing.T) {
			var got []string
			for _, w := range Words(tt.text) {
				got = append(got, w.Lexeme)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("Words(%q) = %q, want %q", tt.text, got, tt.want)
			}
			if got := Document("python", "", "", "", tt.text); !slices.Equal(got, tt.want) {
				t.Errorf("Document(%q) = %q, want %q", tt.text, got, tt.want)
			}
		})
	}
	// A query for the name finds the chunk that holds it.
	doc := Document("c", "arch.c", "", "", "#ifdef __x86_64__\n")
	if !slices.Contains(doc, "x8664") {
		t.Errorf("Document of a chunk that names __x86_64__ = %q, want x8664 in it", doc)
	}
}
