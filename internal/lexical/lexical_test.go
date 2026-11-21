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
		{"a part of one non-ASCII letter", "parseÉ", []string{"parseé", "parse"}},
		{"repeated", "lease Lease LEASE", []string{"lease"}},
		{"plural acronym", "IDs", []string{"ids"}},
		{"plural acronym inside", "getURLs URLsByID", []string{"geturls", "get", "urls", "urlsbyid", "id"}},
		{"acronym before a word", "HTTPSConnection", []string{"httpsconnection", "https", "connection"}},
		{"letters around digits", "k8s i18n v2 x", []string{"k8s", "i18n", "v2"}},
		{"a hyphen in a query joins", "rate-limit", []string{"ratelimit", "rate", "limit"}},
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
	want := []Word{{"parsehttprequest", "parseHTTPRequest", true, true}, {"parse", "parse", false, false}, {"http", "HTTP", false, false},
		{"request", "Request", false, false}}
	if !slices.Equal(got, want) {
		t.Errorf("Words = %+v, want %+v", got, want)
	}
}

// TestDocumentHyphens: a hyphen joins the words around it in a path, and in
// the code of a language whose names can hold one, but is a minus in the
// code of one whose names cannot.
func TestDocumentHyphens(t *testing.T) {
	for _, tt := range []struct {
		name, language, path, text string
		want                       []string
	}{
		{"minus in Go", "go", "internal/rate-limit/limit.go", "n := end-start", []string{"internal", "ratelimit", "rate", "limit", "go",
			"end", "start"}},
		{"kebab case in CSS", "css", "web/site.css", ".max-width { }", []string{"web", "site", "css", "maxwidth", "max", "width"}},
		{"no language", "", "notes.txt", "end-start", []string{"notes", "txt", "endstart", "end", "start"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := Document(tt.language, tt.path, "", "", tt.text); !slices.Equal(got, tt.want) {
				t.Errorf("Document = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDocument(t *testing.T) {
	got := Document("go", "internal/store/lease.go", "Store", "AcquireLease", "// AcquireLease takes a slot.\nfunc (s *Store) AcquireLease() {}")
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
	if got := Document("", "", "", "", b.String()); len(got) != maxDocumentLexemes {
		t.Errorf("Document kept %d lexemes, want %d", len(got), maxDocumentLexemes)
	}
	long := strings.Repeat("a", 3*maxLexemeBytes)
	for _, l := range Document("", "", "", "", long+" "+long+"Tail") {
		if len(l) > maxLexemeBytes {
			t.Errorf("lexeme of %d bytes, want at most %d", len(l), maxLexemeBytes)
		}
	}
}

// TestWordsCompound: an identifier written whole is compound when it has
// several parts.
func TestWordsCompound(t *testing.T) {
	for _, tt := range []struct {
		text string
		want bool
	}{{"parseHTTPRequest", true}, {"max_batch", true}, {"lease", false}, {"v2", false}} {
		if w := Words(tt.text); len(w) == 0 || !w[0].Whole || w[0].Compound != tt.want {
			t.Errorf("Words(%q) = %+v, want a whole word first, compound %v", tt.text, w, tt.want)
		}
	}
}
