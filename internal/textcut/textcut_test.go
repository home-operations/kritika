package textcut

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestPrefix(t *testing.T) {
	tests := []struct {
		name, s string
		n       int
		want    string
	}{
		{"fits", "abc", 3, "abc"},
		{"ascii", "abcdef", 4, "abcd"},
		{"on a boundary", "aé", 3, "aé"},
		{"inside a rune", "aé", 2, "a"},
		{"inside a four-byte rune", "a😀b", 3, "a"},
		{"nothing fits", "😀", 2, ""},
		{"zero", "abc", 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Prefix(tt.s, tt.n)
			if got != tt.want || !utf8.ValidString(got) {
				t.Fatalf("Prefix(%q, %d) = %q, want %q", tt.s, tt.n, got, tt.want)
			}
		})
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		name, s string
		n       int
		want    string
	}{
		{"fits", "abc", 3, "abc"},
		{"cut with the note inside n", strings.Repeat("a", 40), 30, strings.Repeat("a", 9) + "\n[truncated 31 bytes]"},
		{"inside a rune", strings.Repeat("é", 20), 30, strings.Repeat("é", 4) + "\n[truncated 32 bytes]"},
		{"no room for the note", "abcdef", 4, "\n[truncated 6 bytes]"},
		{"disabled", "abc", 0, "abc"},
		{"negative", "abc", -1, "abc"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Truncate(tt.s, tt.n)
			if got != tt.want || !utf8.ValidString(got) {
				t.Fatalf("Truncate(%q, %d) = %q, want %q", tt.s, tt.n, got, tt.want)
			}
			if len(tt.s) > tt.n && tt.n >= 30 && len(got) > tt.n {
				t.Fatalf("Truncate(%q, %d) is %d bytes, over n", tt.s, tt.n, len(got))
			}
		})
	}
}

// TestTruncateTwice: a result cut to n passes a second cut to n as it is,
// so its note still counts what the first cut dropped.
func TestTruncateTwice(t *testing.T) {
	s := strings.Repeat("x", 100_000)
	once := Truncate(s, 4096)
	kept := strings.Count(once, "x")
	if twice := Truncate(once, 4096); twice != once || len(once) > 4096 ||
		!strings.HasSuffix(once, fmt.Sprintf("\n[truncated %d bytes]", len(s)-kept)) {
		t.Fatalf("once is %d bytes ending %q, twice ends %q", len(once), once[len(once)-30:], twice[len(twice)-30:])
	}
}

func TestCut(t *testing.T) {
	tests := []struct {
		name, s    string
		n, dropped int
		want       string
	}{
		{"nothing dropped", "abc", 30, 0, "abc"},
		{"dropped, the rest fits", "abc", 30, 500, "abc\n[truncated 500 bytes]"},
		{"dropped, the rest cut too", strings.Repeat("a", 40), 30, 500, strings.Repeat("a", 8) + "\n[truncated 532 bytes]"},
		{"dropped, no cap", "abc", 0, 7, "abc\n[truncated 7 bytes]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Cut(tt.s, tt.n, tt.dropped); got != tt.want {
				t.Fatalf("Cut(%q, %d, %d) = %q, want %q", tt.s, tt.n, tt.dropped, got, tt.want)
			}
		})
	}
}
