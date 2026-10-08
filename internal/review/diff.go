package review

import "strings"

// FitDiff keeps the whole file sections of a unified diff that fit in room
// bytes, in order, and reports the paths of those it left out.
func FitDiff(diff string, room int) (string, []string) {
	if len(diff) <= room {
		return diff, nil
	}
	sections := splitFiles(diff)
	var b strings.Builder
	var omitted []string
	for _, s := range sections {
		if b.Len()+len(s.text) > room {
			omitted = append(omitted, s.path)
			continue
		}
		b.WriteString(s.text)
	}
	return b.String(), omitted
}

type fileSection struct {
	path string
	text string
}

// FileDiffs is a unified diff's file sections, each by the path the
// changed-files list names it by, ending in one newline.
func FileDiffs(diff string) map[string]string {
	out := map[string]string{}
	for _, s := range splitFiles(diff) {
		out[s.path] = strings.TrimRight(s.text, "\n") + "\n"
	}
	return out
}

// fileMarks is, by path, what the changed-files list says after a file the
// diff adds, deletes or renames; a file it only modifies has no mark. A new
// file is worth naming: every line of it is one a finding may anchor to,
// whether or not its section fits in the prompt.
func fileMarks(diff string) map[string]string {
	marks := map[string]string{}
	for _, s := range splitFiles(diff) {
		if m := fileMark(s.text); m != "" {
			marks[s.path] = m
		}
	}
	return marks
}

// fileMark reads one file section's header, which ends where its "---"
// line or its first hunk begins.
func fileMark(section string) string {
	for l := range strings.SplitSeq(section, "\n") {
		switch {
		case strings.HasPrefix(l, "new file mode "):
			return " (new)"
		case strings.HasPrefix(l, "deleted file mode "):
			return " (deleted)"
		case strings.HasPrefix(l, "rename from "):
			return " (renamed from " + strings.TrimPrefix(l, "rename from ") + ")"
		case strings.HasPrefix(l, "--- "), strings.HasPrefix(l, "@@"):
			return ""
		}
	}
	return ""
}

// splitFiles cuts a unified diff at "diff --git" boundaries.
func splitFiles(diff string) []fileSection {
	var out []fileSection
	start, pos, path := 0, 0, "?"
	for l := range strings.SplitSeq(diff, "\n") {
		if strings.HasPrefix(l, "diff --git ") {
			if pos > 0 {
				out = append(out, fileSection{path: path, text: diff[start:pos]})
			}
			start, path = pos, pathFromHeader(l)
		}
		pos += len(l) + 1
	}
	return append(out, fileSection{path: path, text: diff[start:] + "\n"})
}

func pathFromHeader(l string) string {
	// "diff --git a/x/y b/x/y"
	if _, after, ok := strings.CutLast(l, " b/"); ok {
		return after
	}
	return strings.TrimPrefix(l, "diff --git ")
}
