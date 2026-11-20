package lexical

import (
	"slices"
	"strings"
	"testing"
)

func TestWords(t *testing.T) {
	tests := []struct {
		name, text string
		want       []string
	}{
		{"camel case", "parseHTTPRequest", []string{"parsehttprequest", "parse", "http", "request"}},
		{"pascal case", "HTTPServer", []string{"httpserver", "http", "server"}},
		{"snake case", "parse_http_request", []string{"parsehttprequest", "parse", "http", "request"}},
		{"kebab case", "max-batch-chars", []string{"maxbatchchars", "max", "batch", "chars"}},
		{"screaming snake", "MAX_EMBED_DIMS", []string{"maxembeddims", "max", "embed", "dims"}},
		{"digits", "sha256Sum", []string{"sha256sum", "sha", "sum"}},
		{"qualified", "store.InsertUsage(ctx, tx)", []string{"store", "insertusage", "insert", "usage", "tx"}},
		{"one part", "lease", []string{"lease"}},
		{"keywords and common names", "func (s *Store) Close() error { return nil }", []string{"store", "close"}},
		{"english", "Release the lease when the run ends", []string{"release", "lease", "run", "ends"}},
		{"minus is not a joiner", "n-1 and a-b", nil},
		{"short and numeric", "x 42 0x1f", nil},
		{"non-ASCII letters", "naïveCache", []string{"naïvecache", "naïve", "cache"}},
		{"repeated", "lease Lease LEASE", []string{"lease"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []string
			for _, w := range Words(tt.text) {
				got = append(got, w.Lexeme)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("Words(%q) = %q, want %q", tt.text, got, tt.want)
			}
		})
	}
}

func TestWordsKeepTheSpelling(t *testing.T) {
	got := Words("parseHTTPRequest")
	want := []Word{{"parsehttprequest", "parseHTTPRequest", true}, {"parse", "parse", false}, {"http", "HTTP", false},
		{"request", "Request", false}}
	if !slices.Equal(got, want) {
		t.Errorf("Words = %+v, want %+v", got, want)
	}
}

func TestDocument(t *testing.T) {
	got := Document("internal/store/lease.go", "Store", "AcquireLease", "// AcquireLease takes a slot.\nfunc (s *Store) AcquireLease() {}")
	for _, want := range []string{"internal", "lease", "go", "store", "acquirelease", "acquire", "takes", "slot"} {
		if !slices.Contains(got, want) {
			t.Errorf("Document lacks %q: %q", want, got)
		}
	}
	if len(got) != len(slices.Compact(slices.Sorted(slices.Values(got)))) {
		t.Errorf("Document repeats a lexeme: %q", got)
	}
}

func TestDocumentBounds(t *testing.T) {
	var b strings.Builder
	for i := range 2 * maxDocumentLexemes {
		b.WriteString("word")
		for n := i; n > 0; n /= 26 {
			b.WriteByte(byte('a' + n%26))
		}
		b.WriteByte(' ')
	}
	if got := Document("", "", "", b.String()); len(got) != maxDocumentLexemes {
		t.Errorf("Document kept %d lexemes, want %d", len(got), maxDocumentLexemes)
	}
	long := strings.Repeat("a", 3*maxLexemeBytes)
	for _, l := range Document("", "", "", long+" "+long+"Tail") {
		if len(l) > maxLexemeBytes {
			t.Errorf("lexeme of %d bytes, want at most %d", len(l), maxLexemeBytes)
		}
	}
}
