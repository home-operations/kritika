package contextpack

import (
	"strings"

	"github.com/home-operations/kritika/internal/chunk"
	"github.com/home-operations/kritika/internal/udiff"
)

// diffLines is what a unified diff says per path: the head-side lines it
// added, the head-side lines it shows at all (added plus context) with
// their text, and the base-side lines it removed.
type diffLines struct {
	added   map[string][]int
	shown   map[string]map[int]string
	removed map[string][]int
}

// diffLine is one line of a hunk: its kind ('+', '-' or ' '), its text
// without the marker, the paths of the file's two sides ("" for a side
// that does not exist) and the line's number on each side.
type diffLine struct {
	kind             byte
	text             string
	oldPath, newPath string
	oldLine, newLine int
	// hunk numbers the diff's hunks from 0.
	hunk int
}

// walkDiff calls fn for each added, removed and context line of each hunk
// of a unified diff.
func walkDiff(diff string, fn func(diffLine)) {
	hunk := -1
	for _, f := range udiff.Parse(diff) {
		for _, h := range f.Hunks {
			hunk++
			for _, l := range h.Lines {
				switch l.Kind {
				case '+', '-', ' ':
					fn(diffLine{kind: l.Kind, text: l.Text, oldPath: f.OldPath, newPath: f.NewPath, oldLine: l.OldLine, newLine: l.NewLine, hunk: hunk})
				}
			}
		}
	}
}

// parseDiff walks a unified diff once. Paths are head-side for added and
// shown, base-side for removed; a rename therefore keys the two sides
// differently, which is what the two trees need.
func parseDiff(diff string) diffLines {
	d := diffLines{added: map[string][]int{}, shown: map[string]map[int]string{}, removed: map[string][]int{}}
	walkDiff(diff, func(l diffLine) {
		switch {
		case l.kind == '-' && l.oldPath != "":
			d.removed[l.oldPath] = append(d.removed[l.oldPath], l.oldLine)
		case l.kind == '+' && l.newPath != "":
			d.added[l.newPath] = append(d.added[l.newPath], l.newLine)
			d.show(l.newPath, l.newLine, l.text)
		case l.kind == ' ' && l.newPath != "":
			d.show(l.newPath, l.newLine, l.text)
		}
	})
	return d
}

// ChangedLines counts the lines a unified diff adds and removes in the
// files no ignore glob matches. A file is matched by its head-side path,
// or its base-side path once deleted.
func ChangedLines(diff string, ignore []string) int {
	n := 0
	walkDiff(diff, func(l diffLine) {
		if l.kind == ' ' {
			return
		}
		path := l.newPath
		if path == "" {
			path = l.oldPath
		}
		if !chunk.Matches(ignore, path) {
			n++
		}
	})
	return n
}

// ShownLines returns, per head-side path, the head-side lines a unified
// diff shows, its added and context lines, each with its text.
func ShownLines(diff string) map[string]map[int]string {
	return parseDiff(diff).shown
}

// TouchedLines returns, per head-side path, the head-side lines a unified
// diff changed: each line it added, and for lines it removed the line that
// now follows where they were.
func TouchedLines(diff string) map[string][]int {
	touched := map[string][]int{}
	walkDiff(diff, func(l diffLine) {
		if l.kind != ' ' && l.newPath != "" {
			touched[l.newPath] = append(touched[l.newPath], l.newLine)
		}
	})
	return touched
}

// Renames maps the base-side path of each file a unified diff renames and
// changes to its head-side path. A rename alone has no hunk to say so, and
// is left out.
func Renames(diff string) map[string]string {
	renames := map[string]string{}
	walkDiff(diff, func(l diffLine) {
		if l.oldPath != "" && l.newPath != "" && l.oldPath != l.newPath {
			renames[l.oldPath] = l.newPath
		}
	})
	return renames
}

func (d *diffLines) show(path string, line int, text string) {
	if d.shown[path] == nil {
		d.shown[path] = map[int]string{}
	}
	d.shown[path][line] = text
}

// runs groups sorted line numbers into ranges, merging neighbours closer
// than gap lines.
func runs(lines []int, gap int) [][2]int {
	var out [][2]int
	for _, l := range lines {
		if n := len(out); n > 0 && l-out[n-1][1] <= gap {
			out[n-1][1] = l
			continue
		}
		out = append(out, [2]int{l, l})
	}
	return out
}

// Hunks returns the head-side text of each hunk in a unified diff: added
// and context lines, without the removed ones, so each string reads like
// the region as it now is. Hunks are keyed by their head path; a deleted
// file's have none and are left out.
func Hunks(diff string) []Hunk {
	var out []Hunk
	last := -1
	var cur Hunk
	var text strings.Builder
	flush := func() {
		if cur.Path != "" && strings.TrimSpace(text.String()) != "" {
			cur.Text = text.String()
			out = append(out, cur)
		}
		text.Reset()
	}
	walkDiff(diff, func(l diffLine) {
		if l.hunk != last {
			flush()
			last, cur = l.hunk, Hunk{Path: l.newPath}
		}
		if l.kind != '-' {
			text.WriteString(l.text)
			text.WriteByte('\n')
		}
	})
	flush()
	return out
}

// Hunk is one hunk's head-side text.
type Hunk struct {
	Path string
	Text string
}
