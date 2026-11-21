package chunk

import (
	"cmp"
	"slices"

	"github.com/odvcencio/gotreesitter"
)

// wrapped returns the index in out of the one outlined top-level
// declaration that d wraps, or -1. d wraps it when it holds no other one and
// ends on its last line, and holds nothing above it but lead lines unless
// bare holds it alone, and none at all when bare governs it, as a
// conditional does. bare is d's node when d is a statement with no name
// of its own, such as a C++ template, and nil otherwise. top indexes the
// top-level declarations of out by StartByte.
func wrapped(out []Decl, top []int, d Decl, lead []bool, bare *gotreesitter.Node, lang *gotreesitter.Language) int {
	k, _ := slices.BinarySearchFunc(top, d.StartByte, func(i, b int) int { return cmp.Compare(out[i].StartByte, b) })
	if k == len(top) || k+1 < len(top) && out[top[k+1]].EndByte <= d.EndByte {
		return -1
	}
	o := out[top[k]]
	if o.EndByte > d.EndByte || o.EndLine != d.EndLine || bare != nil && governs(bare, lang, uint32(o.StartByte), uint32(o.EndByte)) {
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

// governs reports whether n runs the declaration from start to end under a
// condition or as one statement of a body, as a conditional, a loop, a with
// or a try does, rather than wrapping it as an export, a decorator, a
// template or a linkage specification does. Such a statement is a
// declaration of its own however little it holds: its guard is not the
// function's. A wrapper holds the declaration itself as its body, as a C++
// linkage specification does, never a block of statements around it, which
// in Python spans the same bytes as a lone definition in it.
func governs(n *gotreesitter.Node, lang *gotreesitter.Language, start, end uint32) bool {
	// spans reports whether c spans the declaration, and is reports whether
	// c is the declaration: it spans it and no child of it does.
	spans := func(c *gotreesitter.Node) bool { return c.StartByte() == start && c.EndByte() == end }
	is := func(c *gotreesitter.Node) bool {
		if !spans(c) {
			return false
		}
		for i := range c.NamedChildCount() {
			if spans(c.NamedChild(i)) {
				return false
			}
		}
		return true
	}
	for n != nil && !is(n) {
		var next *gotreesitter.Node
		for i := range n.ChildCount() {
			c := n.Child(i)
			switch field := n.FieldNameForChild(i, lang); {
			case field == "condition":
				return true
			case next != nil || !c.IsNamed() || c.StartByte() > start || c.EndByte() < end:
			case field == "consequence" || field == "alternative" || field == "body" && !is(c):
				return true
			default:
				next = c
			}
		}
		n = next
	}
	return false
}
