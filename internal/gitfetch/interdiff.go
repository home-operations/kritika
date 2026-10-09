package gitfetch

import (
	"strings"

	"github.com/home-operations/kritika/internal/udiff"
)

// hunkKeys counts the hunks of a unified diff by their path and their added
// and removed lines, with everything positional left out as PatchID leaves
// it out: a hunk the base gained sits at other lines, under other context,
// in the diff since the prior head than in the diff between the merge bases.
func hunkKeys(diff string) map[string]int {
	keys := map[string]int{}
	for _, f := range udiff.Parse(diff) {
		for _, h := range f.Hunks {
			keys[hunkKey(f.Path(), h)]++
		}
	}
	return keys
}

// withoutHunks is one file's diff less as many hunks of each key as drop
// counts, each one dropped taken off drop, "" when none of its hunks
// remain: the change may make an edit the base made too, and that one
// stays. A file diff with no hunks, a rename or a mode change alone, stands.
func withoutHunks(f udiff.File, drop map[string]int) string {
	var b strings.Builder
	b.WriteString(f.Header)
	kept := 0
	for _, h := range f.Hunks {
		if key := hunkKey(f.Path(), h); drop[key] > 0 {
			drop[key]--
			continue
		}
		b.WriteString(h.String())
		kept++
	}
	if len(f.Hunks) > 0 && kept == 0 {
		return ""
	}
	return b.String()
}

// hunkKey is a hunk's path and its added and removed lines, trailing
// whitespace trimmed; context, the hunk header and a no-newline note are
// left out.
func hunkKey(path string, h udiff.Hunk) string {
	var b strings.Builder
	b.WriteString(path)
	b.WriteByte(0)
	for _, l := range h.Lines {
		if l.Kind == '+' || l.Kind == '-' {
			b.WriteByte(l.Kind)
			b.WriteString(strings.TrimRight(l.Text, " \t\r"))
			b.WriteByte('\n')
		}
	}
	return b.String()
}
