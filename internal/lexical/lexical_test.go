package lexical

import (
	"slices"
	"strings"
	"testing"

	"github.com/home-operations/kritika/internal/tuning"
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
		{"a letter and digits", "S3 X509", []string{"s3", "x509"}},
		{"a hyphen in a query joins", "rate-limit", []string{"ratelimit", "rate", "limit"}},
		{"a combining mark", "cafe\u0301Cache", []string{"cafe\u0301cache", "cafe\u0301", "cache"}},
		{"a combining mark before a case change", "getE\u0301tat", []string{"gete\u0301tat", "get", "e\u0301tat"}},
		{"a combining mark before a hyphen", "cafe\u0301-bar", []string{"cafe\u0301bar", "cafe\u0301", "bar"}},
		{"an acronym before a marked capital", "HTTPA\u0301gent", []string{"httpa\u0301gent", "http", "a\u0301gent"}},
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

// TestWordsKeepTheStrongestOccurrence: a lexeme that a text holds more than
// once keeps its first place, and the spelling and kind of its strongest
// occurrence: a whole identifier over a part, and a compound one over one of
// a single part. So the order of a query's words does not decide whether a
// chunk counts as holding a compound name.
func TestWordsKeepTheStrongestOccurrence(t *testing.T) {
	for _, tt := range []struct {
		text string
		want []Word
	}{
		{"ratelimit rateLimit", []Word{{"ratelimit", "rateLimit", true, true}, {"rate", "rate", false, false}, {"limit", "Limit", false, false}}},
		{"rateLimit ratelimit", []Word{{"ratelimit", "rateLimit", true, true}, {"rate", "rate", false, false}, {"limit", "Limit", false, false}}},
		{"parseRequest parse", []Word{{"parserequest", "parseRequest", true, true}, {"parse", "parse", true, false}, {"request", "Request", false, false}}},
		{"parse parseRequest", []Word{{"parse", "parse", true, false}, {"parserequest", "parseRequest", true, true}, {"request", "Request", false, false}}},
	} {
		t.Run(tt.text, func(t *testing.T) {
			if got := Words(tt.text); !slices.Equal(got, tt.want) {
				t.Errorf("Words(%q) = %+v, want %+v", tt.text, got, tt.want)
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
		{"names of one-letter words in CSS", "css", "web/site.css", ".x-y { --x-y: 0 }", []string{"web", "site", "css", "xy"}},
		{"numeric suffixes in CSS", "css", "web/site.css", ".bg-blue-500 { } .bg-blue-600 { }", []string{"web", "site", "css",
			"bgblue500", "bg", "blue", "bgblue600"}},
		{"numeric suffix in SCSS", "scss", "web/site.scss", "$gap-2: 4px;", []string{"web", "site", "scss", "gap2", "gap"}},
		{"numeric suffix in YAML", "yaml", "deploy.yaml", "bg-blue-500: x-y", []string{"deploy", "yaml", "bgblue", "bg", "blue"}},
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
	limits := tuning.Get().Lexical
	var b strings.Builder
	for i := range 2 * limits.MaxDocumentLexemes {
		b.WriteString("word")
		for n := i; n > 0; n /= 26 {
			b.WriteByte(byte('a' + n%26))
		}
		b.WriteByte(' ')
	}
	if got := Document("", "", "", "", b.String()); len(got) != limits.MaxDocumentLexemes {
		t.Errorf("Document kept %d lexemes, want %d", len(got), limits.MaxDocumentLexemes)
	}
	long := strings.Repeat("a", 3*limits.MaxLexemeBytes)
	for _, l := range Document("", "", "", "", long+" "+long+"Tail") {
		if len(l) > limits.MaxLexemeBytes {
			t.Errorf("lexeme of %d bytes, want at most %d", len(l), limits.MaxLexemeBytes)
		}
	}
	tuning.Override(t, func(v *tuning.Values) { v.Lexical.MaxDocumentLexemes, v.Lexical.MaxLexemeBytes = 3, 4 })
	if got := Document("", "", "", "", "alpha beta gamma delta epsilon zeta"); !slices.Equal(got, []string{"beta", "zeta"}) {
		t.Errorf("Document at a configured 3 lexemes of at most 4 bytes = %q, want beta and zeta", got)
	}
	if got := Document("", "", "", "", "cat dog owl fox"); len(got) != 3 {
		t.Errorf("Document at a configured 3 lexemes kept %q", got)
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

// TestDocumentCombiningMarks: a combining mark, which JavaScript, Python and
// Java let an identifier hold, stays inside it, so a chunk that declares
// one holds what a query that names it gives.
func TestDocumentCombiningMarks(t *testing.T) {
	got := Document("javascript", "src/cache.js", "", "caféCache", "const caféCache = new Map();")
	for _, want := range []string{"cafécache", "café", "cache"} {
		if !slices.Contains(got, want) {
			t.Errorf("Document lacks %q: %q", want, got)
		}
	}
}

// TestHyphenAfterADigit: a hyphen after a digit joins a name that starts
// with a letter to the letters after it, so http2-server gives the
// http2server that http2Server and http2_server give. A run that starts with
// a digit is a number and does not join, a digit after a hyphen does not
// either, and a minus between names of a letter and digits keeps the names.
func TestHyphenAfterADigit(t *testing.T) {
	for _, tt := range []struct {
		text string
		want []string
	}{
		{"http2-server", []string{"http2server", "http", "server"}},
		{"http2Server", []string{"http2server", "http", "server"}},
		{"top-10-results", []string{"top", "results"}},
		{"n-1", nil},
		{"x2-y2", []string{"x2", "y2"}},
		{"x86-64", []string{"x86"}},
	} {
		t.Run(tt.text, func(t *testing.T) {
			ws := Words(tt.text)
			got := make([]string, 0, len(ws))
			for _, w := range ws {
				got = append(got, w.Lexeme)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("Words(%q) = %q, want %q", tt.text, got, tt.want)
			}
		})
	}
	if got := Document("yaml", "", "", "", "name: http2-server"); !slices.Contains(got, "http2server") {
		t.Errorf("Document of YAML that names http2-server = %q, want http2server in it", got)
	}
	if got := Document("go", "", "", "", "d := h2-server"); slices.Contains(got, "h2server") {
		t.Errorf("Document of Go = %q, want no h2server: a hyphen is a minus there", got)
	}
}

// TestCodeWordsHyphens: a diff query's hyphen is what it is in its file's
// language, as Document has it: a minus between two names in Go or
// JavaScript, which joins no compound the code's chunks lack, and part of a
// name in CSS or YAML, or when the language is unknown, as Words has it.
// Only in CSS is it always part of a name, so a name of one-letter words is
// one there, and two words left out elsewhere, and a name's numeric suffix
// stays in it there, where elsewhere it is a number after a minus.
func TestCodeWordsHyphens(t *testing.T) {
	for _, tt := range []struct {
		name, language, text string
		want                 []string
	}{
		{"minus in Go", "go", "d := end-start", []string{"end", "start"}},
		{"minus between digit names in Go", "go", "old2-new2", []string{"old2", "old", "new2"}},
		{"minus in JavaScript", "javascript", "const d = end-start;", []string{"end", "start"}},
		{"kebab case in CSS", "css", ".max-width { }", []string{"maxwidth", "max", "width"}},
		{"a name of one-letter words in CSS", "css", ".x-y { }", []string{"xy"}},
		{"a custom property of one-letter words in CSS", "css", "--x-y: 0;", []string{"xy"}},
		{"a numeric suffix in CSS", "css", ".bg-blue-500 { }", []string{"bgblue500", "bg", "blue"}},
		{"another numeric suffix in CSS", "css", ".bg-blue-600 { }", []string{"bgblue600", "bg", "blue"}},
		{"a numeric suffix in Less", "less", "@gap-2: 4px;", []string{"gap2", "gap"}},
		{"a number after a CSS name", "css", "margin: 0 -1px", []string{"margin"}},
		{"a number in a CSS name that starts with one", "css", ".nth-child(2n-1)", []string{"nthchild", "nth", "child"}},
		{"kebab case in YAML", "yaml", "end-start: 1", []string{"endstart", "end", "start"}},
		{"one-letter words in YAML", "yaml", "x-y: bg-blue-500", []string{"bgblue", "bg", "blue"}},
		{"minus between one-letter names in Go", "go", "d := x-y", nil},
		{"minus before a number in Go", "go", "d := gap-2", []string{"gap"}},
		{"no language", "", "end-start", []string{"endstart", "end", "start"}},
		{"one-letter words with no language", "", "x-y bg-blue-500", []string{"bgblue", "bg", "blue"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			words := CodeWords(tt.language, tt.text)
			got := make([]string, 0, len(words))
			for _, w := range words {
				got = append(got, w.Lexeme)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("CodeWords(%q, %q) = %q, want %q", tt.language, tt.text, got, tt.want)
			}
		})
	}
}

// TestDocumentWithinTsvectorBytes: distinct lexemes each within the
// per-lexeme limit, more than a tsvector holds together, are kept only
// until their bytes reach PostgreSQL's total.
func TestDocumentWithinTsvectorBytes(t *testing.T) {
	tuning.Override(t, func(v *tuning.Values) { v.Lexical.MaxLexemeBytes, v.Lexical.MaxDocumentLexemes = 2047, 100000 })
	var text strings.Builder
	for i := range 700 {
		text.WriteString(string(rune('a'+i/26%26)) + string(rune('a'+i%26)) + string(rune('a'+i/676)) + strings.Repeat("x", 1997) + " ")
	}
	got := Document("go", "", "", "", text.String())
	total := 0
	for _, l := range got {
		total += len(l)
	}
	if total > postgresMaxLexemesBytes || len(got) < 500 {
		t.Fatalf("%d lexemes of %d bytes, want as many as fit %d", len(got), total, postgresMaxLexemesBytes)
	}
}
