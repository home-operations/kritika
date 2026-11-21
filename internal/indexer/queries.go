package indexer

import (
	"cmp"
	"slices"
	"strings"

	"github.com/home-operations/kritika/internal/chunk"
	"github.com/home-operations/kritika/internal/contextpack"
	"github.com/home-operations/kritika/internal/textcut"
	"github.com/home-operations/kritika/internal/udiff"
)

// queryContext is how many unchanged lines a query keeps on each side of a
// hunk's changed lines.
const queryContext = 3

// HunkQueries is what a run's similar-code stage asks the index: the changed
// regions of the diff's hunks as the head has them, each its changed lines
// and a little context, and the path of each. It leaves out the files that
// the index holds no chunk of, ignored, Unindexed or of no language, and the
// hunks that add nothing, whose text is only context. The files take turns,
// the most changed first, so a query of each of the most changed files comes
// before a second of any. A region over the gateway's bound is asked about by
// the declarations its changes are in, as the index cuts a file, or else in
// windows of its lines, rather than by its start, which in a new file is its
// license and imports.
//
// A query is the code alone. The index leads each chunk with its path, so a
// query led by its own path scores the files beside it higher for their
// path alone, and the definition and caller stages already show those that
// the change uses.
func HunkQueries(diff string, ignore []string) (queries, paths []string) {
	var files []*queryFile
	for _, f := range udiff.Parse(diff) {
		if f.NewPath == "" || chunk.Matches(ignore, f.NewPath) || Unindexed(f.NewPath) {
			continue
		}
		if lang, _ := chunk.Language(f.NewPath); lang == "" {
			continue
		}
		qf := &queryFile{path: f.NewPath}
		for _, h := range f.Hunks {
			if region, n := changedRegion(h); n > 0 {
				qf.regions = append(qf.regions, region)
				qf.added += n
			}
		}
		if len(qf.regions) > 0 {
			files = append(files, qf)
		}
	}
	slices.SortStableFunc(files, func(a, b *queryFile) int { return cmp.Compare(b.added, a.added) })
	var parser chunk.Parser
	for len(files) > 0 && len(queries) < contextpack.SimilarQueries {
		left := files[:0]
		for _, f := range files {
			if len(queries) == contextpack.SimilarQueries {
				break
			}
			if q, ok := f.next(&parser); ok {
				queries, paths = append(queries, q), append(paths, f.path)
				left = append(left, f)
			}
		}
		files = left
	}
	return queries, paths
}

// queryFile is one file's regions still to ask about, and the queries left
// of the region in hand.
type queryFile struct {
	path    string
	added   int
	regions [][]udiff.Line
	pending []string
}

// next is the file's next query, false once it has none.
func (f *queryFile) next(parser *chunk.Parser) (string, bool) {
	for len(f.pending) == 0 {
		if len(f.regions) == 0 {
			return "", false
		}
		f.pending = regionQueries(parser, f.path, f.regions[0])
		f.regions = f.regions[1:]
	}
	q := f.pending[0]
	f.pending = f.pending[1:]
	return q, true
}

// changedRegion is the head-side lines of h from queryContext lines before
// its first added line to queryContext lines after its last, and how many
// lines it adds that are not blank. A hunk that only removes, or adds only
// blank lines, adds none.
func changedRegion(h udiff.Hunk) ([]udiff.Line, int) {
	var head []udiff.Line
	first, last, n := -1, -1, 0
	for _, l := range h.Lines {
		if l.Kind != '+' && l.Kind != ' ' {
			continue
		}
		if l.Kind == '+' && strings.TrimSpace(l.Text) != "" {
			if first < 0 {
				first = len(head)
			}
			last = len(head)
			n++
		}
		head = append(head, l)
	}
	if n == 0 {
		return nil, 0
	}
	return head[max(0, first-queryContext):min(len(head), last+queryContext+1)], n
}

// regionQueries is the queries of one changed region: the region itself when
// it fits the gateway's bound. Otherwise it is the outermost named
// declarations that hold an added line, as the index cuts a file. A
// declaration over the bound gives way to the ones nested in it that hold an
// added line, or without any, to the windows of its lines that hold one:
// its first bound's worth can be all unchanged code. Without such a
// declaration, it is the windows of the region's lines that hold an added
// line.
func regionQueries(parser *chunk.Parser, path string, region []udiff.Line) []string {
	var b strings.Builder
	var added []int
	for i, l := range region {
		b.WriteString(l.Text)
		b.WriteByte('\n')
		if l.Kind == '+' && strings.TrimSpace(l.Text) != "" {
			added = append(added, i+1)
		}
	}
	text := b.String()
	if len(text) <= contextpack.SimilarQueryChars {
		return []string{text}
	}
	f := parser.Parse(path, []byte(text))
	defer f.Close()
	holds := func(d chunk.Decl) bool {
		if d.Symbol == "" || d.Kind == "import" {
			return false
		}
		i, _ := slices.BinarySearch(added, d.StartLine)
		return i < len(added) && added[i] <= d.EndLine
	}
	var out []string
	lastEnd := 0
	for i, d := range f.Decls {
		if d.StartLine <= lastEnd || !holds(d) {
			continue
		}
		body := f.Text(d.StartLine, d.EndLine) + "\n"
		switch {
		case len(body) <= contextpack.SimilarQueryChars:
			out = append(out, body)
		case slices.ContainsFunc(f.Decls[i+1:], func(n chunk.Decl) bool {
			return n.StartLine > d.StartLine && n.EndLine <= d.EndLine && holds(n)
		}):
			continue
		default:
			out = append(out, regionWindows(region[d.StartLine-1:min(d.EndLine, len(region))])...)
		}
		lastEnd = d.EndLine
	}
	if len(out) > 0 {
		return out
	}
	return regionWindows(region)
}

// regionWindows cuts region into runs of whole lines within the gateway's
// bound, and keeps the runs that hold an added line.
func regionWindows(region []udiff.Line) []string {
	var out []string
	var b strings.Builder
	adds := false
	flush := func() {
		if adds {
			q := textcut.Prefix(b.String(), contextpack.SimilarQueryChars)
			// A line indented past the bound would cut to a blank query,
			// which the gateway refuses with the rest of the request.
			if strings.TrimSpace(q) == "" {
				q = textcut.Prefix(strings.TrimSpace(b.String()), contextpack.SimilarQueryChars)
			}
			out = append(out, q)
		}
		b.Reset()
		adds = false
	}
	for _, l := range region {
		if b.Len() > 0 && b.Len()+len(l.Text)+1 > contextpack.SimilarQueryChars {
			flush()
		}
		b.WriteString(l.Text)
		b.WriteByte('\n')
		adds = adds || l.Kind == '+' && strings.TrimSpace(l.Text) != ""
	}
	flush()
	return out
}
