package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strconv"
	"strings"

	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/review"
	"github.com/home-operations/kritika/internal/textcut"
)

var readDiffSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"path": {"type": "string", "description": "A changed file's path, as the changed-files list names it."},
		"page": {"type": "integer", "description": "The page to read, from 1; a long diff comes in pages, and each says how many."}
	},
	"required": ["path"],
	"additionalProperties": false
}`)

// readDiffTool is read_diff: one changed file's part of the pull request's
// diff, for a file the prompt left out to fit its budget, whose head alone
// read_file shows. Each line the head has is led by its number there, the
// one a finding anchors to, so a page that starts inside a hunk still says
// where its lines are.
type readDiffTool struct {
	files    map[string]string
	maxBytes int
}

func newReadDiffTool(diff string, maxBytes int) *readDiffTool {
	return &readDiffTool{files: review.FileDiffs(diff), maxBytes: maxBytes}
}

func (t *readDiffTool) Def() model.ToolDef {
	return model.ToolDef{
		Name: "read_diff",
		Description: "Read one changed file's part of the pull request's diff. Each line the head commit has is led by its " +
			"line number there and a tab, a removed line by a tab alone.",
		InputSchema: readDiffSchema,
	}
}

// pageNoteRoom is kept free on a page for the note that numbers it.
const pageNoteRoom = 96

func (t *readDiffTool) Run(_ context.Context, input json.RawMessage) (string, error) {
	var req struct {
		Path string `json:"path"`
		Page int    `json:"page"`
	}
	if len(input) > 0 {
		if err := json.Unmarshal(input, &req); err != nil {
			return "", fmt.Errorf("agent: read_diff: %w", err)
		}
	}
	section, ok := t.files[path.Clean(strings.TrimSpace(req.Path))]
	if !ok {
		return "", fmt.Errorf("agent: read_diff: %q is not a file the pull request changes", req.Path)
	}
	pages := diffPages(numberDiff(section), t.maxBytes-pageNoteRoom)
	page := max(req.Page, 1)
	if page > len(pages) {
		return "", fmt.Errorf("agent: read_diff: %s's diff ends at page %d", req.Path, len(pages))
	}
	out := pages[page-1]
	switch {
	case page < len(pages):
		out += fmt.Sprintf("[page %d of %d of this file's diff; read_diff with page %d reads on]", page, len(pages), page+1)
	case len(pages) > 1:
		out += fmt.Sprintf("[page %d of %d of this file's diff]", page, len(pages))
	}
	return out, nil
}

// numberDiff leads each line of a file section's hunks with the line's
// number in the head and a tab, a removed line, which the head lacks, with
// a tab alone. The section's header and its hunk headers are kept as they
// are.
func numberDiff(section string) string {
	var b strings.Builder
	inHunk, next := false, 0
	for l := range strings.SplitSeq(strings.TrimSuffix(section, "\n"), "\n") {
		switch {
		case strings.HasPrefix(l, "@@"):
			next, inHunk = hunkStart(l)
		case !inHunk:
		case strings.HasPrefix(l, "-"), strings.HasPrefix(l, `\`):
			l = "\t" + l
		default:
			l = strconv.Itoa(next) + "\t" + l
			next++
		}
		b.WriteString(l + "\n")
	}
	return b.String()
}

// hunkStart is the head's first line in a hunk header, c in
// "@@ -a,b +c,d @@".
func hunkStart(header string) (int, bool) {
	f := strings.Fields(header)
	if len(f) < 3 || !strings.HasPrefix(f[2], "+") {
		return 0, false
	}
	start, _, _ := strings.Cut(f[2][1:], ",")
	n, err := strconv.Atoi(start)
	return n, err == nil
}

// diffPages cuts text into pages of at most room bytes at line ends. A line
// longer than a page is cut to fit one.
func diffPages(text string, room int) []string {
	const cut = " [cut]\n"
	room = max(room, len(cut)+1)
	var pages []string
	var b strings.Builder
	for l := range strings.SplitSeq(strings.TrimSuffix(text, "\n"), "\n") {
		l += "\n"
		if len(l) > room {
			l = textcut.Prefix(l, room-len(cut)) + cut
		}
		if b.Len()+len(l) > room {
			pages = append(pages, b.String())
			b.Reset()
		}
		b.WriteString(l)
	}
	return append(pages, b.String())
}
