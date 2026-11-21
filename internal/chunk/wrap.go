package chunk

import (
	"cmp"
	"slices"

	"github.com/odvcencio/gotreesitter"
)

// wrapped returns the index in out of the one outlined top-level
// declaration that d wraps, or -1. d wraps it when it holds no other one and
// ends on its last line, and holds nothing above it but lead lines unless
// bare holds it alone. bare is d's node when d is a statement with no name
// of its own, such as a C++ template, and nil otherwise. top indexes the
// top-level declarations of out by StartByte.
func wrapped(out []Decl, top []int, d Decl, lead []bool, bare *gotreesitter.Node, lang *gotreesitter.Language) int {
	k, _ := slices.BinarySearchFunc(top, d.StartByte, func(i, b int) int { return cmp.Compare(out[i].StartByte, b) })
	if k == len(top) || k+1 < len(top) && out[top[k+1]].EndByte <= d.EndByte {
		return -1
	}
	o := out[top[k]]
	if o.EndByte > d.EndByte || o.EndLine != d.EndLine {
		return -1
	}
	if bare != nil && alone(bare, lang, uint32(o.StartByte), uint32(o.EndByte)) {
		return top[k]
	}
	for l := d.StartLine; l < o.StartLine; l++ {
		if !lead[l] {
			return -1
		}
	}
	return top[k]
}

// alone reports whether the node from start to end is a child of n, or
// descends from one through nodes that hold nothing else but lead nodes.
// An export, a decorator or a template holds the declaration so, and so
// does a TypeScript export of an ambient declaration. A conditional whose
// body runs a statement beside the declaration does not: the statement is
// the conditional's, not the declaration's.
func alone(n *gotreesitter.Node, lang *gotreesitter.Language, start, end uint32) bool {
	for depth := 0; n != nil; depth++ {
		var next *gotreesitter.Node
		for i := range n.NamedChildCount() {
			switch c := n.NamedChild(i); {
			case next == nil && c.StartByte() <= start && c.EndByte() >= end:
				next = c
			case depth > 0 && !leads(c.Type(lang)):
				return false
			}
		}
		if next != nil && next.StartByte() == start && next.EndByte() == end {
			return true
		}
		n = next
	}
	return false
}
