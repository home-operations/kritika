// Package textcut shortens text without splitting a UTF-8 sequence, so a
// cut string stays valid wherever valid UTF-8 is required, as it is in a
// Postgres text column.
package textcut

import (
	"fmt"
	"unicode/utf8"
)

// Prefix is the longest prefix of s at most n bytes long that ends on a
// rune boundary: s itself when it fits.
func Prefix(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// Truncate is Prefix with a note of how many bytes were cut appended, the
// two together at most n bytes long when n has room for the note; a
// non-positive n disables the cut. Fitting the note matters: a result over
// n that is cut again would lose its note to one counting only the note's
// own bytes.
func Truncate(s string, n int) string { return Cut(s, n, 0) }

// Cut is Truncate for s that has already lost dropped bytes: the note
// counts them too, and is appended even when s itself fits.
func Cut(s string, n, dropped int) string {
	if dropped == 0 && (n <= 0 || len(s) <= n) {
		return s
	}
	if tail := note(dropped); n <= 0 || len(s)+len(tail) <= n {
		return s + tail
	}
	// The note counts at most dropped+len(s) bytes, so room for that note
	// is room for the one appended.
	kept := Prefix(s, max(n-len(note(dropped+len(s))), 0))
	return kept + note(dropped+len(s)-len(kept))
}

func note(cut int) string { return fmt.Sprintf("\n[truncated %d bytes]", cut) }
