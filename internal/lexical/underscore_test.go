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

// TestUnderscoreDigitIdentifiers: a name may start with an underscore and a
// digit, as _2faEnabled does in Go, Python or JavaScript, and is a name, not
// a number: it keeps its whole self, with the underscore that tells it from
// one, and its parts. A number gives nothing, with or without underscores,
// and so does an underscore before digits alone.
func TestUnderscoreDigitIdentifiers(t *testing.T) {
	for _, tt := range []struct {
		text string
		want []string
	}{
		{"_2faEnabled", []string{"_2faenabled", "fa", "enabled"}},
		{"__2faEnabled", []string{"_2faenabled", "fa", "enabled"}},
		{"if _2fa {", []string{"_2fa", "fa"}},
		{"42 1_000_000 0x1f 2fa", nil},
		{"_123", nil},
	} {
		t.Run(tt.text, func(t *testing.T) {
			lexemes := func(words []Word) []string {
				out := make([]string, 0, len(words))
				for _, w := range words {
					out = append(out, w.Lexeme)
				}
				return out
			}
			if got := lexemes(Words(tt.text)); !slices.Equal(got, tt.want) {
				t.Errorf("Words(%q) = %q, want %q", tt.text, got, tt.want)
			}
			if got := lexemes(CodeWords("go", tt.text)); !slices.Equal(got, tt.want) {
				t.Errorf("CodeWords(go, %q) = %q, want %q", tt.text, got, tt.want)
			}
			if got := Document("go", "", "", "", tt.text); !slices.Equal(got, tt.want) {
				t.Errorf("Document(%q) = %q, want %q", tt.text, got, tt.want)
			}
		})
	}
}
