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
// the index holds no chunk of, ignored, Unindexed, of no language or, when
// indexable is set, refused by it, as Indexable refuses one too large to
// index, and the hunks that add nothing, whose text is only context. Those
// files go before the ranking, so that they take no turn. The files take
// turns, the most changed first, so a query of each of the most changed
// files comes before a second of any. A region over the gateway's bound is
// asked about by the declarations its changes are in, as the index cuts a
// file, or else in windows of its lines, rather than by its start, which in
// a new file is its license and imports.
//
// A query is the code alone. The index leads each chunk with its path, so a
// query led by its own path scores the files beside it higher for their
// path alone, and the definition and caller stages already show those that
// the change uses.
func HunkQueries(diff string, ignore []string, indexable func(path string) bool) (queries, paths []string) {
	var files []*queryFile
	for _, f := range udiff.Parse(diff) {
		if f.NewPath == "" || chunk.Matches(ignore, f.NewPath) || Unindexed(f.NewPath) || indexable != nil && !indexable(f.NewPath) {
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
		f.pending = regionQueries(parser, f.path, f.regions[0], contextpack.SimilarQueries)
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
// its first bound's worth can be all unchanged code. The windows of the lines
// of a declaration that gave way that hold an added line outside the nested
// ones, such as a field beside a method, come after the declarations, so
// that they do not crowd them out. Without such a declaration, it is the
// windows of the region's lines that hold an added line. Lines outside every
// named declaration, as a license or imports are, are not asked about beside
// one. It is at most limit queries, the first of them, as a request carries
// no more, and a large added file can hold thousands of declarations.
func regionQueries(parser *chunk.Parser, path string, region []udiff.Line, limit int) []string {
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
	// holds reports whether d is named and holds an added line from line from.
	holds := func(d chunk.Decl, from int) bool {
		if d.Symbol == "" || d.Kind == "import" {
			return false
		}
		i, _ := slices.BinarySearch(added, max(d.StartLine, from))
		return i < len(added) && added[i] <= d.EndLine
	}
	// asked marks, by 1-based line, the lines that a query holds. gave counts
	// the declarations that gave way to nested ones on each line, from the
	// difference at their ends: they nest, so marking each one's lines would
	// walk a long body once for each declaration around it.
	asked, gave := make([]bool, len(region)+1), make([]int, len(region)+2)
	var out []string
	// lastEnd and lastEndByte are where the last declaration asked about ends.
	// One nested in it is asked about with it, while one that starts on its
	// last line, as `} function next() {` does, is asked about for the added
	// lines below that line.
	lastEnd, lastEndByte := 0, 0
	for i, d := range f.Decls {
		if len(out) == limit {
			return out
		}
		if d.EndByte <= lastEndByte || !holds(d, lastEnd+1) {
			continue
		}
		body := f.Bytes(d.StartLine, d.EndLine)
		switch {
		case len(body)+1 <= contextpack.SimilarQueryChars:
			out = append(out, string(body)+"\n")
		case slices.ContainsFunc(f.Decls[i+1:], func(n chunk.Decl) bool {
			return n.EndByte <= d.EndByte && (n.StartByte > d.StartByte || n.EndByte < d.EndByte) && holds(n, lastEnd+1)
		}):
			gave[d.StartLine]++
			gave[min(d.EndLine, len(region))+1]--
			continue
		default:
			out = append(out, regionWindows(region[d.StartLine-1:min(d.EndLine, len(region))], limit-len(out))...)
		}
		for l := d.StartLine; l <= min(d.EndLine, len(region)); l++ {
			asked[l] = true
			if marked != nil {
				*marked++
			}
		}
		lastEnd, lastEndByte = d.EndLine, d.EndByte
	}
	if len(out) == 0 {
		return regionWindows(region, limit)
	}
	for l := 1; l <= len(region); l++ {
		gave[l] += gave[l-1]
		if marked != nil {
			*marked++
		}
	}
	for start := 1; start <= len(region) && len(out) < limit; start++ {
		if gave[start] == 0 || asked[start] {
			continue
		}
		end := start
		for end < len(region) && gave[end+1] > 0 && !asked[end+1] {
			end++
		}
		out = append(out, regionWindows(region[start-1:end], limit-len(out))...)
		start = end
	}
	return out
}

// regionWindows cuts region into runs of whole lines within the gateway's
// bound, and keeps the first limit runs that hold an added line.
func regionWindows(region []udiff.Line, limit int) []string {
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
			if flush(); len(out) == limit {
				return out
			}
		}
		b.WriteString(l.Text)
		b.WriteByte('\n')
		adds = adds || l.Kind == '+' && strings.TrimSpace(l.Text) != ""
	}
	flush()
	return out
}

// marked, when a test points it at a count, adds up the lines that
// regionQueries marks. Marking a long body once for each declaration nested
// around it is quadratic in the region's size, and the count shows that on
// any machine. A test that sets it does not run in parallel.
var marked *int
