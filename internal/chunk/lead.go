package chunk

import (
	"cmp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/odvcencio/gotreesitter"
)

// leads reports whether a node of type typ leads into the declaration
// below it rather than standing alone: a comment, a decorator, or an
// attribute or annotation. A Rust inner attribute (inner_attribute_item)
// applies to what encloses it, so it does not lead.
func leads(typ string) bool {
	switch typ {
	case "attribute_item", "attribute_list", "annotation", "marker_annotation":
		return true
	}
	return comment(typ) || strings.Contains(typ, "decorator")
}

// comment reports whether a node of type typ is a comment. A SQL
// comment_statement (COMMENT ON) is a statement.
func comment(typ string) bool { return strings.HasSuffix(typ, "comment") }

// leadLines marks, by 1-based line, the lines that lead nodes fill: every
// character on the line that is not white space belongs to one such node,
// and a node that continues onto the line began on a lead line. continued is
// the 1-based lines, in order, that a lead node which began above continues
// onto, such as the lines of a block comment after its first.
func leadLines(root *gotreesitter.Node, lang *gotreesitter.Language, src []byte, text [][2]int) (lead []bool, continued []int) {
	// reach is, by 0-based row, the end of the row's text that lead nodes
	// fill from its start, with only white space between them, or, once
	// something else stops them, where that thing starts, so that the white
	// space before it is walked once. from is the 1-based line where a lead
	// node that continues onto the row began.
	reach, from := make([]int, len(text)), make([]int, len(text))
	var walk func(n *gotreesitter.Node)
	walk = func(n *gotreesitter.Node) {
		if !leads(n.Type(lang)) {
			for i := range n.ChildCount() {
				walk(n.Child(i))
			}
			return
		}
		first, last := int(n.StartPoint().Row), int(n.EndPoint().Row)
		if last > first && n.EndPoint().Column == 0 {
			last-- // a line comment that holds its newline
		}
		for row := first; row <= last && row < len(text); row++ {
			lo, hi := text[row][0], text[row][1]
			p := skipSpace(src, cmp.Or(reach[row], lo), hi)
			reach[row] = p
			if int(n.StartByte()) <= p {
				reach[row] = max(p, int(n.EndByte()))
			}
			if row > first {
				from[row] = first + 1
			}
		}
	}
	walk(root)
	lead = make([]bool, len(text)+1)
	for row := range text {
		if from[row] != 0 {
			continued = append(continued, row+1)
		}
		// A blank line inside a block comment is the comment's, while one
		// between two comments separates them.
		lo, hi := text[row][0], text[row][1]
		lead[row+1] = (lo < hi || from[row] != 0) && reach[row] > 0 && skipSpace(src, reach[row], hi) >= hi &&
			(from[row] == 0 || lead[from[row]])
	}
	return lead, continued
}

// skipSpace is the offset of the first byte from i to hi that is not white
// space, or hi. White space is what lineTexts trims from a line's ends.
func skipSpace(src []byte, i, hi int) int {
	from := i
	for i < hi {
		r, size := rune(src[i]), 1
		if r >= utf8.RuneSelf {
			r, size = utf8.DecodeRune(src[i:hi])
		}
		if !unicode.IsSpace(r) {
			break
		}
		i += size
	}
	if walked != nil {
		*walked += i - from
	}
	return i
}
