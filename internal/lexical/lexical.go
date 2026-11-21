// Package lexical turns code, and queries about it, into the words that the
// similar-code index matches by name. Postgres's text-search parsers split
// code where it is not split and stem what must match exactly, so the words
// are made here and stored as they are, with the chunks that hold each
// (index_postings).
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
// each identifier whole, then its parts. A lexeme that text holds more than
// once takes the spelling and kind of its strongest occurrence: a whole
// identifier over a part, and a compound one over one of a single part. So
// "ratelimit rateLimit" holds a compound ratelimit, as "rateLimit ratelimit"
// does. Language keywords, the names every file uses, English stop words,
// numbers, and words of one letter are left out. A hyphen between two
// letters joins them, and so does one between a name's digit and a letter,
// as a query's text can be any language's, or prose, where rate-limit names
// rateLimit and http2-server names http2Server.
func Words(text string) []Word { return words(text, true) }

// CodeWords are the Words of text that is code of the language, as package
// chunk names it, such as a diff's lines: a hyphen is a minus in the code of
// minusLanguages, as Document has it, so end-start there gives end and
// start, and no endstart that the language's chunks never hold. Of another
// language, or none (""), they are Words.
func CodeWords(language, text string) []Word { return words(text, !minusLanguages[language]) }

func words(text string, hyphens bool) []Word {
	var out []Word
	at := map[string]int{}
	strength := func(w Word) int {
		switch {
		case w.Compound:
			return 2
		case w.Whole:
			return 1
		}
		return 0
	}
	add := func(lexeme, spelling string, whole, compound bool) {
		if utf8.RuneCountInString(lexeme) < 2 || len(lexeme) > maxLexemeBytes || stop[lexeme] || !startsWithName(lexeme) {
			return
		}
		w := Word{Lexeme: lexeme, Spelling: spelling, Whole: whole, Compound: compound}
		if i, ok := at[lexeme]; ok {
			if strength(w) > strength(out[i]) {
				out[i] = w
			}
			return
		}
		at[lexeme] = len(out)
		out = append(out, w)
	}
	var identifier func(id string)
	identifier = func(id string) {
		parts := split(id)
		// a-b is two names, as a hyphen can be a minus, but an underscore is
		// part of a name: x86_64 and a_b are one each, as k8s and v2 are. So
		// a hyphenated id whose parts are all of one letter is the names
		// between its hyphens: x2-y2 is x2 and y2.
		if strings.Contains(id, "-") && !slices.ContainsFunc(parts, func(p string) bool { return utf8.RuneCountInString(p) > 1 }) {
			for name := range strings.SplitSeq(id, "-") {
				identifier(name)
			}
			return
		}
		whole := strings.ToLower(separators.Replace(id))
		if strings.HasPrefix(id, "_") {
			whole = "_" + whole
		}
		add(whole, id, true, len(parts) > 1)
		if len(parts) == 1 && strings.ToLower(parts[0]) == whole {
			return
		}
		for _, p := range parts {
			add(strings.ToLower(p), p, false, false)
		}
	}
	for _, id := range identifiers(text, hyphens) {
		identifier(id)
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

// identifiers are text's runs of letters, digits and underscores, and the
// combining marks on them. With hyphens, a hyphen before a letter is kept
// inside a run when a letter is before it, or a digit in a run that starts
// with a letter: a kebab-case name is one identifier, as http2-server is,
// while n-1 is two tokens and so is 10-items. A run that starts with a digit
// is a number and is left out. Underscores around a run are trimmed, but for
// one before a digit, which makes a name of what follows when a letter does:
// _2faEnabled is a name, and _2fa the same one as __2fa, where _123 is not.
func identifiers(text string, hyphens bool) []string {
	var out []string
	start := -1
	flush := func(end int) {
		if start >= 0 {
			id := strings.Trim(text[start:end], "_-")
			if id != "" && !startsWithLetter(id) && text[start] == '_' && strings.ContainsFunc(id, unicode.IsLetter) {
				id = "_" + id
			}
			if startsWithName(id) {
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
		case unicode.IsMark(r) && start >= 0:
			// A combining mark continues an identifier, as most languages'
			// rules for one let it.
		case r == '-' && hyphens && start >= 0 && i > 0:
			prev, _ := utf8.DecodeLastRuneInString(text[:i])
			next, _ := utf8.DecodeRuneInString(text[i+1:])
			after := unicode.IsLetter(prev) || unicode.IsMark(prev) ||
				unicode.IsDigit(prev) && startsWithLetter(strings.TrimLeft(text[start:i], "_"))
			if !after || !unicode.IsLetter(next) {
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
		// A combining mark stays with its letter, and the letter it marks
		// is what the next rune's case is compared with.
		if i == start || unicode.IsMark(r) {
			continue
		}
		j := i - 1
		for j > start && unicode.IsMark(runes[j]) {
			j--
		}
		prev := runes[j]
		// next is the letter after r's marks, whose case tells whether r
		// starts a word after an acronym.
		next := i + 1
		for next < len(runes) && unicode.IsMark(runes[next]) {
			next++
		}
		switch {
		case unicode.IsUpper(r) && (unicode.IsLower(prev) || unicode.IsDigit(prev)):
			cut(i)
		case unicode.IsUpper(prev) && unicode.IsUpper(r) && next < len(runes) && unicode.IsLower(runes[next]) && !plural(runes[next:]):
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

// startsWithName reports whether s starts as a name does: with a letter, or
// with the underscore that identifiers keeps before a digit.
func startsWithName(s string) bool {
	return startsWithLetter(s) || strings.HasPrefix(s, "_")
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
