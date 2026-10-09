package gitfetch

import "strings"

// hunkKeys counts the hunks of a unified diff by their path and their added
// and removed lines, with everything positional left out as PatchID leaves
// it out: a hunk the base gained sits at other lines, under other context,
// in the diff since the prior head than in the diff between the merge bases.
func hunkKeys(diff string) map[string]int {
	keys := map[string]int{}
	for _, file := range splitFileDiffs(diff) {
		path, _, hunks := parseFileDiff(file)
		for _, h := range hunks {
			keys[hunkKey(path, h)]++
		}
	}
	return keys
}

// withoutHunks is one file's unified diff less as many hunks of each key as
// drop counts, each one dropped taken off drop, "" when none of its hunks
// remain: the change may make an edit the base made too, and that one
// stays. A file diff with no hunks, a rename or a mode change alone, stands.
func withoutHunks(fileDiff string, drop map[string]int) string {
	path, header, hunks := parseFileDiff(fileDiff)
	var kept []string
	for _, h := range hunks {
		if key := hunkKey(path, h); drop[key] > 0 {
			drop[key]--
			continue
		}
		kept = append(kept, h)
	}
	if len(hunks) > 0 && len(kept) == 0 {
		return ""
	}
	return header + strings.Join(kept, "")
}

// splitFileDiffs cuts a unified diff at each file's "diff --git" line.
func splitFileDiffs(diff string) []string {
	var files []string
	var cur strings.Builder
	for _, line := range strings.SplitAfter(diff, "\n") {
		if strings.HasPrefix(line, "diff --git ") && cur.Len() > 0 {
			files = append(files, cur.String())
			cur.Reset()
		}
		cur.WriteString(line)
	}
	if cur.Len() > 0 {
		files = append(files, cur.String())
	}
	return files
}

// parseFileDiff splits one file's unified diff into its header, up to the
// first hunk, and its hunks, each from its "@@" line, and names the file by
// its new path, or its old one for a deletion.
func parseFileDiff(fileDiff string) (path, header string, hunks []string) {
	var h, cur strings.Builder
	inHeader := true
	for _, line := range strings.SplitAfter(fileDiff, "\n") {
		if strings.HasPrefix(line, "@@") {
			inHeader = false
			if cur.Len() > 0 {
				hunks = append(hunks, cur.String())
				cur.Reset()
			}
		}
		if !inHeader {
			cur.WriteString(line)
			continue
		}
		h.WriteString(line)
		name := strings.TrimRight(line, "\n")
		if n, ok := strings.CutPrefix(name, "+++ "); ok && n != "/dev/null" {
			path = strings.TrimPrefix(n, "b/")
		} else if n, ok := strings.CutPrefix(name, "--- "); ok && path == "" && n != "/dev/null" {
			path = strings.TrimPrefix(n, "a/")
		}
	}
	if cur.Len() > 0 {
		hunks = append(hunks, cur.String())
	}
	return path, h.String(), hunks
}

// hunkKey is a hunk's path and its added and removed lines, trailing
// whitespace trimmed; context, the hunk header and a no-newline note are
// left out.
func hunkKey(path, hunk string) string {
	var b strings.Builder
	b.WriteString(path)
	b.WriteByte(0)
	for line := range strings.SplitSeq(hunk, "\n") {
		if strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-") {
			b.WriteString(strings.TrimRight(line, " \t\r"))
			b.WriteByte('\n')
		}
	}
	return b.String()
}
