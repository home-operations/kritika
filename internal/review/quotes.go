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

// MaxQuoteRefusals is how many times a run's submission is sent back for
// its quotes before one is taken as it is, its misquoted findings
// dropped: a model that does not correct a quote twice over will not,
// and the rest of its review is worth more than the citations.
const MaxQuoteRefusals = 2

// QuoteGate holds a run's submissions to what a finding that cites one
// of its rules must carry: a quote that is a passage of a rule it cites,
// as quotes says. Check refuses a submission with such a finding, naming
// every one so a run told to submit can correct them all in the steps it
// has left, up to MaxQuoteRefusals times; Drop then removes them from
// the submission the run ends with. A finding that enforces a rule the
// model made up, or misread, so goes back to the model rather than to
// the author, and never posts.
type QuoteGate struct {
	rules   []Rule
	refused int
}

// NewQuoteGate is a gate on rules, nil for none, which Check and Drop
// take as nothing to hold to.
func NewQuoteGate(rules []Rule) *QuoteGate {
	if len(rules) == 0 {
		return nil
	}
	return &QuoteGate{rules: rules}
}

// misquoted is the index of each finding of raw that cites one of the
// gate's rules without quoting it, with why.
func (g *QuoteGate) misquoted(raw json.RawMessage) (at []int, why []string) {
	ids := make([]string, len(g.rules))
	for i, r := range g.rules {
		ids[i] = r.ID
	}
	text := func(id string) string {
		i := slices.IndexFunc(g.rules, func(r Rule) bool { return r.ID == id })
		return g.rules[i].Text
	}
	var res Result
	if json.Unmarshal(raw, &res) != nil {
		return nil, nil
	}
	for i, f := range res.Findings {
		cited := citedRules(f.Rules, ids)
		if len(cited) == 0 {
			continue
		}
		where := fmt.Sprintf("%s:%d", strings.TrimSpace(f.Path), f.Line)
		switch {
		case strings.TrimSpace(f.Quote) == "":
			at, why = append(at, i), append(why, fmt.Sprintf("%s cites %s but has no quote", where, nameRules(cited)))
		case !slices.ContainsFunc(cited, func(id string) bool { return quotes(text(id), f.Quote) }):
			at, why = append(at, i), append(why, fmt.Sprintf("%s's quote is not in %s", where, nameRules(cited)))
		}
	}
	return at, why
}

// Check wraps check with the gate: a submission check accepts is refused
// while it has a misquoted finding and the gate has refusals left.
func (g *QuoteGate) Check(check func(json.RawMessage) error) func(json.RawMessage) error {
	if g == nil {
		return check
	}
	return func(raw json.RawMessage) error {
		if err := check(raw); err != nil {
			return err
		}
		_, why := g.misquoted(raw)
		if len(why) == 0 || g.refused >= MaxQuoteRefusals {
			return nil
		}
		g.refused++
		return fmt.Errorf("review: %s. Copy the passage of the rule each finding enforces into its quote, verbatim, as the "+
			"Review rules section has it, at least %d characters of it or the whole rule when it is shorter; leave rules out "+
			"of a finding that enforces none", strings.Join(why, "; "), minQuoteChars)
	}
}

// Drop is raw, the submission a run ended with, without its misquoted
// findings, which are named; raw itself when there are none.
func (g *QuoteGate) Drop(raw json.RawMessage) (json.RawMessage, []string, error) {
	if g == nil {
		return raw, nil, nil
	}
	at, why := g.misquoted(raw)
	if len(at) == 0 {
		return raw, nil, nil
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, nil, fmt.Errorf("review: drop misquoted: %w", err)
	}
	var chosen struct {
		Findings []json.RawMessage `json:"findings"`
	}
	if err := json.Unmarshal(raw, &chosen); err != nil {
		return nil, nil, fmt.Errorf("review: drop misquoted: %w", err)
	}
	kept := make([]json.RawMessage, 0, len(chosen.Findings))
	for i, f := range chosen.Findings {
		if !slices.Contains(at, i) {
			kept = append(kept, f)
		}
	}
	findings, err := json.Marshal(kept)
	if err != nil {
		return nil, nil, fmt.Errorf("review: drop misquoted: %w", err)
	}
	for k := range top {
		if strings.EqualFold(k, keyFindings) {
			delete(top, k)
		}
	}
	top[keyFindings] = findings
	out, err := json.Marshal(top)
	if err != nil {
		return nil, nil, fmt.Errorf("review: drop misquoted: %w", err)
	}
	return out, why, nil
}

// CheckQuotes is a fresh QuoteGate's Check on rules, for a check that
// holds one submission.
func CheckQuotes(check func(json.RawMessage) error, rules []Rule) func(json.RawMessage) error {
	return NewQuoteGate(rules).Check(check)
}

// nameRules names rules for an error: "rule a" or "rules a, b".
func nameRules(ids []string) string {
	if len(ids) == 1 {
		return "rule " + ids[0]
	}
	return "rules " + strings.Join(ids, ", ")
}
