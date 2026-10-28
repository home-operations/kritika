package review

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// describeQuote is the contract's description of a finding's quote.
const describeQuote = "The passage of a cited rule this finding enforces, copied verbatim from the Review rules section, " +
	"at least 12 characters of it or the whole rule when it is shorter; required when rules is set, omitted otherwise."

// minQuoteChars is the least a quote may have once normalized, unless it
// is a rule's whole text: a shorter one could be in a rule by chance.
const minQuoteChars = 12

// Markdown a quote may carry or leave out and still be the rule's text:
// links, which keep their text, heading, quote and list markers at the
// start of a line, nested or not, emphasis marks in pairs around a span,
// and backticks, whose content stays as it is: a mark in code, such as
// the star of a pointer type or the underscores of a dunder, is part of
// the text, and so is a mark on its own.
var (
	quoteLinks    = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	quoteMarkers  = regexp.MustCompile(`(?m)^[ \t]{0,3}(?:(?:#{1,6}|>|[-*+]|\d+[.)])[ \t]+)+`)
	quoteEmphasis = emphasisPatterns("**", "__", "~~", "*", "_")
	quoteMarks    = strings.NewReplacer("`", "", "“", `"`, "”", `"`, "‘", "'", "’", "'")
)

// codeStandIn stands for a code span while the link, marker and emphasis
// passes run, so its content is none of theirs, and an emphasized span may
// hold it. A literal one in the text is dropped first, so every one left
// stands for a span.
const codeStandIn = "\uE000"

// setAsideCode is s with each code span, a run of backticks to the next
// run of the same length as CommonMark reads them, replaced by
// codeStandIn, and the spans' contents in order. A run with no closing
// run is text.
func setAsideCode(s string) (string, []string) {
	var b strings.Builder
	var code []string
	s = strings.ReplaceAll(s, codeStandIn, "")
	for {
		open := strings.IndexByte(s, '`')
		if open < 0 {
			break
		}
		n := len(s[open:]) - len(strings.TrimLeft(s[open:], "`"))
		rest := s[open+n:]
		closing := -1
		for at := 0; at < len(rest); {
			i := strings.Index(rest[at:], strings.Repeat("`", n))
			if i < 0 {
				break
			}
			if run := len(rest[at+i:]) - len(strings.TrimLeft(rest[at+i:], "`")); run == n {
				closing = at + i
				break
			} else {
				at += i + run
			}
		}
		if closing < 0 {
			b.WriteString(s[:open+n])
			s = rest
			continue
		}
		b.WriteString(s[:open])
		b.WriteString(codeStandIn)
		code = append(code, rest[:closing])
		s = rest[closing+n:]
	}
	b.WriteString(s)
	return b.String(), code
}

// restoreCode is s with each codeStandIn replaced by the next of code.
func restoreCode(s string, code []string) string {
	var b strings.Builder
	for i, part := range strings.Split(s, codeStandIn) {
		if i > 0 {
			b.WriteString(code[i-1])
		}
		b.WriteString(part)
	}
	return b.String()
}

// emphasisPatterns matches each mark in pairs around a span that starts
// and ends on something other than space or the mark, holds no mark, and
// is bounded by something other than a letter, a digit or an underscore,
// so the underscores of an identifier are never a pair; longest marks
// first. The bounds are the first and third groups, the span the second.
func emphasisPatterns(marks ...string) []*regexp.Regexp {
	out := make([]*regexp.Regexp, len(marks))
	for i, m := range marks {
		q, c := regexp.QuoteMeta(m), regexp.QuoteMeta(m[:1])
		out[i] = regexp.MustCompile(`(^|[^\pL\pN_])` + q + `([^\s` + c + `](?:[^` + c + `]*?[^\s` + c + `])?)` + q + `($|[^\pL\pN_])`)
	}
	return out
}

// citedRules is the ids of cited that given lists, once each, in the
// order cited; nil for none.
func citedRules(cited, given []string) []string {
	var out []string
	for _, id := range cited {
		if id = strings.TrimSpace(id); slices.Contains(given, id) && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// BlockQuote is Quote as a Markdown block quote, each of its lines led by
// "> ", so it renders as one quotation whatever lines it has.
func (f Finding) BlockQuote() string {
	lines := strings.Split(strings.TrimSpace(f.Quote), "\n")
	for i, l := range lines {
		lines[i] = "> " + l
	}
	return strings.Join(lines, "\n")
}

// normalizeQuote is s as a quote is compared: its markdown stripped,
// lowercased, its whitespace collapsed and the punctuation at its ends
// dropped, so a passage copied without the rule's formatting, or its
// final stop, still matches it.
func normalizeQuote(s string) string {
	s, code := setAsideCode(s)
	s = quoteLinks.ReplaceAllString(s, "$1")
	s = quoteMarkers.ReplaceAllString(s, "")
	for _, re := range quoteEmphasis {
		// A match takes the bound after its span, which the next span may
		// need before its own: the pass goes again until nothing changes.
		for prev := ""; prev != s; {
			prev, s = s, re.ReplaceAllString(s, "$1$2$3")
		}
	}
	s = quoteMarks.Replace(restoreCode(s, code))
	return strings.Trim(strings.ToLower(strings.Join(strings.Fields(s), " ")), " .,;:!?")
}

// quotes reports whether quote is a passage of text: at least
// minQuoteChars of it once both are normalized, or the whole of it.
func quotes(text, quote string) bool {
	t, q := normalizeQuote(text), normalizeQuote(quote)
	return q != "" && (utf8.RuneCountInString(q) >= minQuoteChars || q == t) && strings.Contains(t, q)
}

// CheckQuotes wraps check with what a finding that cites one of rules
// must carry: a quote that is a passage of a rule it cites, as quotes
// says. It is checked where the model can still correct it, so a finding
// that enforces a rule the model made up, or misread, goes back to it
// rather than to the author. One refusal names every such finding, so a
// run told to submit can correct them all in the steps it has left.
func CheckQuotes(check func(json.RawMessage) error, rules []Rule) func(json.RawMessage) error {
	if len(rules) == 0 {
		return check
	}
	ids := make([]string, len(rules))
	for i, r := range rules {
		ids[i] = r.ID
	}
	text := func(id string) string {
		i := slices.IndexFunc(rules, func(r Rule) bool { return r.ID == id })
		return rules[i].Text
	}
	return func(raw json.RawMessage) error {
		if err := check(raw); err != nil {
			return err
		}
		var res Result
		// check has decoded the same input.
		_ = json.Unmarshal(raw, &res)
		var refused []string
		for _, f := range res.Findings {
			cited := citedRules(f.Rules, ids)
			if len(cited) == 0 {
				continue
			}
			where := fmt.Sprintf("%s:%d", strings.TrimSpace(f.Path), f.Line)
			switch {
			case strings.TrimSpace(f.Quote) == "":
				refused = append(refused, fmt.Sprintf("%s cites %s but has no quote", where, nameRules(cited)))
			case !slices.ContainsFunc(cited, func(id string) bool { return quotes(text(id), f.Quote) }):
				refused = append(refused, fmt.Sprintf("%s's quote is not in %s", where, nameRules(cited)))
			}
		}
		if len(refused) == 0 {
			return nil
		}
		return fmt.Errorf("review: %s. Copy the passage of the rule each finding enforces into its quote, verbatim, as the "+
			"Review rules section has it, at least %d characters of it or the whole rule when it is shorter; leave rules out "+
			"of a finding that enforces none", strings.Join(refused, "; "), minQuoteChars)
	}
}

// nameRules names rules for an error: "rule a" or "rules a, b".
func nameRules(ids []string) string {
	if len(ids) == 1 {
		return "rule " + ids[0]
	}
	return "rules " + strings.Join(ids, ", ")
}
