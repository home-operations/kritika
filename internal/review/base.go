package review

import (
	"fmt"
	"strings"
)

// baseNote says what a base other than the default branch is, and then what
// the pull request is stacked on. The head of an open pull request has not
// merged into the default branch, so what it brings is not known to be
// deployed. It may still be, as a release branch with a back-merge open is,
// so the note claims no more than that.
func baseNote(in Input) string {
	stack := stackNote(in.Below)
	switch {
	case in.DefaultBranch == "" || in.BaseRef == "" || in.BaseRef == in.DefaultBranch:
		return stack
	case in.BaseOf == 0:
		return fmt.Sprintf(" (not the default branch, %s)", in.DefaultBranch) + stack
	}
	return fmt.Sprintf(" (not the default branch, %s, but the head of pull request #%d, which is open). What that pull "+
		"request changes has not merged into %s: do not take state, a migration or an interface that only it brings as "+
		"deployed, and ask for compatibility with an earlier form of it only where something shows that form shipped",
		in.DefaultBranch, in.BaseOf, in.DefaultBranch) + stack
}

// StackEntry is a pull request under the reviewed one in its stack on the
// forge.
type StackEntry struct {
	Number int `json:"number"`
	// Merged says that it merged, and Open that it can still merge. One
	// that is neither was closed without merging.
	Merged bool `json:"merged,omitempty"`
	Open   bool `json:"open,omitempty"`
}

// stackNote names the pull requests under this one in its stack, "" for
// none. The forge settles the order they merge in, which the base branch
// stops showing once the one below merged and the forge moved this one's
// base. A merge is not a deployment, so the note claims the order alone.
func stackNote(below []StackEntry) string {
	if len(below) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(". This pull request is in a stack on the forge. Below it in the stack, lowest first:")
	for i, e := range below {
		state := "closed without merging"
		switch {
		case e.Merged:
			state = "merged"
		case e.Open:
			state = "open"
		}
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, " #%d (%s)", e.Number, state)
	}
	b.WriteString(". The forge merges a stack from the bottom up: this pull request cannot merge before one below it, " +
		"and merging it merges with it each one that is still open. That one of them merged does not show that " +
		"what it changes is deployed")
	return b.String()
}
