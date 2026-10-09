// Package udiff reads a unified diff, as git writes one, into its files,
// their hunks and their lines, and nothing else: what a caller makes of a
// diff, a prompt's file list, a finding's anchors, the hunks an
// incremental delta leaves out, stays with the caller. The reading is
// lossless, so a file or a hunk can be written back as it was.
package udiff

import (
	"strconv"
	"strings"
)

// File is one file's diff: every line before its first hunk, and its
// hunks.
type File struct {
	// OldPath and NewPath are the base-side and head-side paths the "---"
	// and "+++" lines name, "" for a side the file has not, /dev/null, and
	// where the lines are missing, as in a rename alone.
	OldPath, NewPath string
	// Header is the file's lines before its first hunk, as they were.
	Header string
	Hunks  []Hunk
}

// Hunk is one hunk: its "@@" line and the lines under it.
type Hunk struct {
	// Header is the "@@" line as it was, and the starts and counts are
	// what it says, zero where it says nothing readable.
	Header                                 string
	OldStart, OldCount, NewStart, NewCount int
	Lines                                  []Line
}

// Line is one line of a hunk.
type Line struct {
	// Kind is the line's marker: '+', '-', ' ', or '\' for a "No newline
	// at end of file" note. A line with none, which a well-formed diff has
	// not, is kept under its first byte, or ' ' when it is empty: a
	// context line whose leading space was stripped.
	Kind byte
	// Text is the line without its marker and its newline.
	Text string
	// OldLine and NewLine are where the line is on each side: its own
	// number on the side it is on, and on the side it is not, the number
	// of the line that follows where it was.
	OldLine, NewLine int
	raw              string
}

// Parse reads a unified diff. A hunk's lines are counted from its header,
// so a removed "-- x" or an added "++ x", which read like file headers,
// stay lines of the hunk; once the counts are spent, a "---" or "+++" line
// opens a file, as a "diff --git" line does at any point.
func Parse(diff string) []File {
	var files []File
	var hunk *Hunk
	var oldAt, newAt, oldLeft, newLeft int
	for raw := range strings.Lines(diff) {
		l := strings.TrimSuffix(raw, "\n")
		counting := oldLeft > 0 || newLeft > 0
		switch {
		case hunk != nil && l == "":
			hunk.Lines = append(hunk.Lines, Line{Kind: ' ', OldLine: oldAt, NewLine: newAt, raw: raw})
			oldAt, newAt, oldLeft, newLeft = oldAt+1, newAt+1, oldLeft-1, newLeft-1
			continue
		case hunk != nil && !strings.HasPrefix(l, "@@") && !strings.HasPrefix(l, "diff --git ") &&
			(counting || !strings.HasPrefix(l, "--- ") && !strings.HasPrefix(l, "+++ ")):
			line := Line{Kind: l[0], Text: l[1:], OldLine: oldAt, NewLine: newAt, raw: raw}
			switch line.Kind {
			case '+':
				newAt, newLeft = newAt+1, newLeft-1
			case '-':
				oldAt, oldLeft = oldAt+1, oldLeft-1
			case ' ':
				oldAt, newAt, oldLeft, newLeft = oldAt+1, newAt+1, oldLeft-1, newLeft-1
			}
			hunk.Lines = append(hunk.Lines, line)
			continue
		case strings.HasPrefix(l, "diff --git "), len(files) == 0, hunk != nil && strings.HasPrefix(l, "--- "):
			files = append(files, File{})
			hunk = nil
		}
		f := &files[len(files)-1]
		switch {
		case strings.HasPrefix(l, "@@"):
			h := Hunk{Header: raw}
			h.OldStart, h.OldCount, h.NewStart, h.NewCount = hunkHeader(l)
			f.Hunks = append(f.Hunks, h)
			hunk = &f.Hunks[len(f.Hunks)-1]
			oldAt, newAt, oldLeft, newLeft = h.OldStart, h.NewStart, h.OldCount, h.NewCount
			continue
		case strings.HasPrefix(l, "--- "):
			f.OldPath = sidePath(l[4:], "a/")
		case strings.HasPrefix(l, "+++ "):
			f.NewPath = sidePath(l[4:], "b/")
		}
		f.Header += raw
	}
	return files
}

// Path is the file's head-side path: NewPath, OldPath once deleted, or the
// second name of its "diff --git" line when it has neither, as a rename
// alone has not.
func (f File) Path() string {
	if f.NewPath != "" {
		return f.NewPath
	}
	if f.OldPath != "" {
		return f.OldPath
	}
	first, _, _ := strings.Cut(f.Header, "\n")
	if _, after, ok := strings.Cut(first, "diff --git "); ok {
		if _, name, ok := strings.CutLast(after, " b/"); ok {
			return name
		}
		return after
	}
	return ""
}

// String is the file's diff as it was.
func (f File) String() string {
	var b strings.Builder
	b.WriteString(f.Header)
	for _, h := range f.Hunks {
		b.WriteString(h.String())
	}
	return b.String()
}

// String is the hunk as it was.
func (h Hunk) String() string {
	var b strings.Builder
	b.WriteString(h.Header)
	for _, l := range h.Lines {
		b.WriteString(l.raw)
	}
	return b.String()
}

// String is the line as it was, marker and newline included.
func (l Line) String() string { return l.raw }

// sidePath is the path a "---" or "+++" line names, its prefix off, ""
// for /dev/null.
func sidePath(p, prefix string) string {
	p = strings.TrimSpace(p)
	if p == "/dev/null" {
		return ""
	}
	return strings.TrimPrefix(p, prefix)
}

// hunkHeader reads "@@ -oldStart,oldCount +newStart,newCount @@"; a count
// left out is 1, and a header that does not read is all zeros.
func hunkHeader(l string) (oldStart, oldCount, newStart, newCount int) {
	fields := strings.Fields(l)
	if len(fields) < 3 || !strings.HasPrefix(fields[1], "-") || !strings.HasPrefix(fields[2], "+") {
		return 0, 0, 0, 0
	}
	oldStart, oldCount, ok1 := hunkRange(fields[1][1:])
	newStart, newCount, ok2 := hunkRange(fields[2][1:])
	if !ok1 || !ok2 {
		return 0, 0, 0, 0
	}
	return oldStart, oldCount, newStart, newCount
}

func hunkRange(s string) (start, count int, ok bool) {
	first, rest, hasCount := strings.Cut(s, ",")
	start, err := strconv.Atoi(first)
	if err != nil {
		return 0, 0, false
	}
	count = 1
	if hasCount {
		if count, err = strconv.Atoi(rest); err != nil {
			return 0, 0, false
		}
	}
	return start, count, true
}
