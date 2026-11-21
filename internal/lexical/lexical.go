// Package lexical turns code, and queries about it, into the words that the
// similar-code index matches by name. Postgres's text-search parsers split
// code where it is not split and stem what must match exactly, so the words
// are made here and stored as they are, each in a posting (index_postings).
//
// An identifier gives its whole self and each of its parts, split at its
// case changes, underscores, hyphens and digits, all lowercased:
// parseHTTPRequest and parse_http_request both give parsehttprequest, parse,
// http and request. So a query finds the code that names what it names,
// whatever the language's convention, and a description's words find the
// identifiers made of them. Nothing is stemmed: stripping -s, -ed and -ing
// from both sides merged names that differ, and found fewer of the
// definitions a change uses in the retrieval evaluation (see the
// development docs).
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
	// one that has several. Compound is true for a whole one of several
	// parts, which few chunks hold by chance.
	Whole, Compound bool
}

// Words are the lexemes of text, each once, in the order they first appear:
// each identifier whole, then its parts. Language keywords, the names every
// file uses, English stop words, numbers, and words of one letter are left
// out. A hyphen between two letters joins them, as a query's text can be
// any language's, or prose, where rate-limit names rateLimit.
func Words(text string) []Word { return words(text, true) }

func words(text string, hyphens bool) []Word {
	var out []Word
	seen := map[string]bool{}
	add := func(lexeme, spelling string, whole, compound bool) {
		if utf8.RuneCountInString(lexeme) < 2 || len(lexeme) > maxLexemeBytes || seen[lexeme] || stop[lexeme] || !startsWithLetter(lexeme) {
			return
		}
		seen[lexeme] = true
		out = append(out, Word{Lexeme: lexeme, Spelling: spelling, Whole: whole, Compound: compound})
	}
	for _, id := range identifiers(text, hyphens) {
		parts := split(id)
		whole := strings.ToLower(separators.Replace(id))
		// a_b is two names, not one, but k8s and v2 are one each.
		if !strings.ContainsAny(id, "_-") || slices.ContainsFunc(parts, func(p string) bool { return utf8.RuneCountInString(p) > 1 }) {
			add(whole, id, true, len(parts) > 1)
		}
		if len(parts) == 1 && strings.ToLower(parts[0]) == whole {
			continue
		}
		for _, p := range parts {
			add(strings.ToLower(p), p, false, false)
		}
	}
	return out
}

var separators = strings.NewReplacer("_", "", "-", "")

// Document is the lexemes of an index chunk of the language (package chunk
// names it): of its path, the scope and symbol of its declaration, and its
// text, each once and at most maxDocumentLexemes. A hyphen joins the words
// of the path, but is a minus in the code of a language whose names hold
// none (minusLanguages).
func Document(language, path, scope, symbol, text string) []string {
	var out []string
	seen := map[string]bool{}
	joins := !minusLanguages[language]
	for i, s := range []string{path, scope, symbol, text} {
		for _, w := range words(s, i == 0 || joins) {
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

// minusLanguages are the languages, as package chunk names them, in whose
// code a hyphen is a minus and never part of a name: end-start is two names
// there, where max-width is one in CSS or YAML.
var minusLanguages = func() map[string]bool {
	m := map[string]bool{}
	for l := range strings.FieldsSeq(`ada apex arduino c c_sharp cairo cpp crystal cuda d dart elixir elm erlang fortran fsharp gdscript
		gleam glsl go groovy hack hare haskell haxe hlsl java javascript julia kotlin lua luau matlab mojo move nim objc ocaml odin pascal
		perl php proto purescript python r rescript ruby rust scala solidity sql squirrel starlark swift teal thrift tsx typescript v
		verilog vhdl wgsl zig`) {
		m[l] = true
	}
	return m
}()

// identifiers are text's runs of letters, digits and underscores. With
// hyphens, a hyphen between two letters is kept inside one: a kebab-case
// name is one identifier, n-1 is two tokens. A run that starts with a digit
// is a number and is left out.
func identifiers(text string, hyphens bool) []string {
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
		case r == '-' && hyphens && start >= 0 && i > 0:
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
// HTTP and Server) unless that is the s of a plural (IDs, URLsByID), and
// where letters meet digits. A part of digits alone is dropped.
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
		case unicode.IsUpper(prev) && unicode.IsUpper(r) && i+1 < len(runes) && unicode.IsLower(runes[i+1]) && !plural(runes[i+1:]):
			cut(i)
		case unicode.IsDigit(r) != unicode.IsDigit(prev):
			cut(i)
		}
	}
	cut(len(runes))
	return parts
}

// plural reports whether rest, what follows an upper-case run, is the s of
// its plural: an s that no lower-case letter follows.
func plural(rest []rune) bool {
	return rest[0] == 's' && (len(rest) == 1 || !unicode.IsLower(rest[1]))
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
