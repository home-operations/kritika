// Package lexical turns code, and queries about it, into the words that the
// similar-code index matches by name. Postgres's text-search parsers split
// code where it is not split and stem what must match exactly, so the words
// are made here and stored as they are (array_to_tsvector).
//
// An identifier gives its whole self and each of its parts, split at its
// case changes, underscores, hyphens and digits, all lowercased:
// parseHTTPRequest and parse_http_request both give parsehttprequest, parse,
// http and request. So a query finds the code that names what it names,
// whatever the language's convention, and a description's words find the
// identifiers made of them.
package lexical

import (
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Bounds on what a document stores: lexemes, and bytes of one. A longer
// run of letters is a key, a hash or encoded data, which no query names.
const (
	maxDocumentLexemes = 2048
	maxLexemeBytes     = 64
)

// Word is one lexeme of a text and how the text spells it.
type Word struct {
	Lexeme   string
	Spelling string
	// Whole is true for an identifier as written, and false for a part of
	// one that has several.
	Whole bool
}

// Words are the lexemes of text, each once, in the order they first appear:
// each identifier whole, then its parts. Language keywords, the names every
// file uses, English stop words, numbers, and words under two bytes are
// left out.
func Words(text string) []Word {
	var out []Word
	seen := map[string]bool{}
	add := func(lexeme, spelling string, whole bool) {
		if len(lexeme) < 2 || len(lexeme) > maxLexemeBytes || seen[lexeme] || stop[lexeme] || !startsWithLetter(lexeme) {
			return
		}
		seen[lexeme] = true
		out = append(out, Word{Lexeme: lexeme, Spelling: spelling, Whole: whole})
	}
	for _, id := range identifiers(text) {
		parts := split(id)
		whole := strings.ToLower(separators.Replace(id))
		// a-b is two names and a minus, not one name.
		if slices.ContainsFunc(parts, func(p string) bool { return utf8.RuneCountInString(p) > 1 }) {
			add(whole, id, true)
		}
		if len(parts) == 1 && strings.ToLower(parts[0]) == whole {
			continue
		}
		for _, p := range parts {
			add(strings.ToLower(p), p, false)
		}
	}
	return out
}

var separators = strings.NewReplacer("_", "", "-", "")

// Document is the lexemes of an index chunk: of its path, the scope and
// symbol of its declaration, and its text, each once and at most
// maxDocumentLexemes.
func Document(path, scope, symbol, text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, s := range []string{path, scope, symbol, text} {
		for _, w := range Words(s) {
			if len(out) == maxDocumentLexemes {
				return out
			}
			if !seen[w.Lexeme] {
				seen[w.Lexeme] = true
				out = append(out, w.Lexeme)
			}
		}
	}
	return out
}

// identifiers are text's runs of letters, digits and underscores, with a
// hyphen between two letters kept inside one: a kebab-case name is one
// identifier, n-1 is two tokens. A run that starts with a digit is a number
// and is left out.
func identifiers(text string) []string {
	var out []string
	start := -1
	flush := func(end int) {
		if start >= 0 {
			if id := strings.Trim(text[start:end], "_-"); id != "" && startsWithLetter(id) {
				out = append(out, id)
			}
			start = -1
		}
	}
	for i, r := range text {
		switch {
		case r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r):
			if start < 0 {
				start = i
			}
		case r == '-' && start >= 0 && i > 0:
			prev, _ := utf8.DecodeLastRuneInString(text[:i])
			next, _ := utf8.DecodeRuneInString(text[i+1:])
			if !unicode.IsLetter(prev) || !unicode.IsLetter(next) {
				flush(i)
			}
		default:
			flush(i)
		}
	}
	flush(len(text))
	return out
}

// split cuts an identifier into its parts: at underscores and hyphens, where
// a lower-case letter or a digit meets an upper-case one, before the last
// upper-case letter of a run that a lower-case one follows (HTTPServer is
// HTTP and Server), and where letters meet digits. A part of digits alone is
// dropped.
func split(id string) []string {
	runes := []rune(id)
	var parts []string
	start := 0
	cut := func(end int) {
		if end > start {
			p := string(runes[start:end])
			if !allDigits(p) {
				parts = append(parts, p)
			}
		}
		start = end
	}
	for i, r := range runes {
		if r == '_' || r == '-' {
			cut(i)
			start = i + 1
			continue
		}
		if i == start {
			continue
		}
		prev := runes[i-1]
		switch {
		case unicode.IsUpper(r) && (unicode.IsLower(prev) || unicode.IsDigit(prev)):
			cut(i)
		case unicode.IsUpper(prev) && unicode.IsUpper(r) && i+1 < len(runes) && unicode.IsLower(runes[i+1]):
			cut(i)
		case unicode.IsDigit(r) != unicode.IsDigit(prev):
			cut(i)
		}
	}
	cut(len(runes))
	return parts
}

func startsWithLetter(s string) bool {
	r, _ := utf8.DecodeRuneInString(s)
	return unicode.IsLetter(r)
}

func allDigits(s string) bool {
	return !strings.ContainsFunc(s, func(r rune) bool { return !unicode.IsDigit(r) })
}

// stop are the words too common to tell code apart: the keywords and
// builtins of the common languages, the names nearly every function uses,
// and English stop words, which a search_code query and comments are full
// of. A word that is merely common in one repository is weighed down by how
// many of its chunks hold it instead.
var stop = func() map[string]bool {
	words := slices.Concat(
		// Go.
		strings.Fields(`break case chan const continue default defer else fallthrough for func goto if import interface map
			package range return select struct switch type var nil true false error string int int64 int32 uint uint64 bool
			byte rune float64 len cap make new append copy delete panic recover any err ctx`),
		// Python, JavaScript, TypeScript, Rust, Java, C and shell.
		strings.Fields(`def class self cls none null undefined let fn pub mut impl use mod crate async await try catch throw
			throws final static public private protected void elif except lambda yield pass raise this super extends
			implements export function var println printf sprintf echo local then fi do done esac while unsafe where
			enum trait match loop ref char long short double float unsigned signed include define ifdef endif`),
		// English.
		strings.Fields(`the an and or of to in is it its for on with as be by this that these those are was were from at
			not no but if can will should would may might has have had does did than so such into out up over only also
			more most other some any each all which who what when where how why there their them they we you your our
			ok`),
	)
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}()
