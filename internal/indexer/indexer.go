// Package indexer cuts a repository tree into the chunks that the embedding
// index stores. It runs in the runner pod, which holds the fetched trees and
// no model key. The worker embeds what it stages. HunkQueries is what a run
// asks that index about a diff.
package indexer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-git/go-git/v6/plumbing/object"

	"github.com/home-operations/kritika/internal/chunk"
	"github.com/home-operations/kritika/internal/textcut"
)

// Chunk is one staged piece of a file.
type Chunk struct {
	Path      string
	Language  string
	Symbol    string
	Kind      string
	Scope     string
	StartLine int
	EndLine   int
	Text      string
	// Signature is the line that names the chunk's declaration when the
	// chunk is a window of it that does not show that line, so that the
	// window's embedding is attributable. Text does not hold it.
	Signature string
}

// maxSignatureBytes bounds a signature, which a minified or generated
// declaration can make one long line.
const maxSignatureBytes = 200

// Options bound the work.
type Options struct {
	// MaxDeclLines is the largest declaration stored whole. Larger ones are
	// represented by their nested declarations, or windowed.
	MaxDeclLines int
	// WindowLines and WindowOverlap cut files that have no declarations.
	WindowLines, WindowOverlap int
	// MaxChunkBytes truncates one chunk's text.
	MaxChunkBytes int
	// MaxFileBytes skips larger files. MaxFiles, MaxBytes and MaxChunks stop
	// the walk.
	MaxFileBytes, MaxFiles, MaxBytes, MaxChunks int
}

// DefaultOptions suit code and configuration repositories alike.
var DefaultOptions = Options{
	MaxDeclLines: 150, WindowLines: 60, WindowOverlap: 10, MaxChunkBytes: 6000,
	MaxFileBytes: 1 << 20, MaxFiles: 50_000, MaxBytes: 500 << 20, MaxChunks: 100_000,
}

// Stats says what Build did.
type Stats struct {
	Files, Parsed, Skipped int
	Bytes                  int
	Truncated              bool
	Elapsed                time.Duration
}

// Build chunks head. With base set, only the paths that differ between base
// and head are chunked. Changed lists every differing path, the deleted ones
// included, so the worker can drop their old chunks. With base nil the whole
// tree is chunked and Changed is nil.
func Build(
	ctx context.Context, head, base *object.Tree, ignore []string, opts Options,
) (chunks []Chunk, changed []string, stats Stats, err error) {
	if opts.MaxDeclLines == 0 {
		opts = DefaultOptions
	}
	start := time.Now()
	b := &builder{opts: opts, ignore: ignore, parser: &chunk.Parser{Limits: chunk.Limits{MaxBytes: opts.MaxFileBytes}}}
	if base == nil {
		err = b.walkAll(ctx, head)
	} else {
		changed, err = b.walkChanged(ctx, head, base)
	}
	b.stats.Elapsed = time.Since(start)
	if err != nil && (!errors.Is(err, ErrBudgetReached) || base != nil) {
		return nil, nil, b.stats, err
	}
	return b.chunks, changed, b.stats, nil
}

// ErrBudgetReached rejects an incomplete incremental build, whose changed
// paths cannot safely replace a generation. A full build keeps its bounded
// output and reports truncation through Stats.
var ErrBudgetReached = errors.New("indexer: budget reached")

type builder struct {
	opts   Options
	ignore []string
	parser *chunk.Parser
	chunks []Chunk
	stats  Stats
}

// unindexed are the paths the index leaves out on top of the ignore rules:
// files that SOPS encrypted, whose text a search has no use for, and code
// that generators write. They are not review ignores, which add to every
// repository's ignore hash: a change to those rebuilds each index at its next
// push, where a change here raises runspec.IndexFormat and so rebuilds at the
// onboarding feeder's pace. A pull request that changes one is still
// reviewed. The ".sops.yaml" that configures SOPS is not encrypted.
var unindexed = []string{
	"**/?*.sops.*",
	"**/*_pb2.py",
	"**/*_pb2.pyi",
	"**/*_pb2_grpc.py",
	"**/*.pb.cc",
	"**/*.pb.h",
	"**/*.g.dart",
	"**/*.freezed.dart",
}

// Unindexed reports whether the index leaves path out whatever the ignore
// rules say: a file that SOPS encrypted, or generated code.
func Unindexed(path string) bool { return chunk.Matches(unindexed, path) }

func (b *builder) ignored(path string) bool {
	return chunk.Matches(b.ignore, path) || Unindexed(path)
}

func (b *builder) walkAll(ctx context.Context, head *object.Tree) error {
	iter := head.Files()
	defer iter.Close()
	return iter.ForEach(func(f *object.File) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return b.file(f)
	})
}

func (b *builder) walkChanged(ctx context.Context, head, base *object.Tree) ([]string, error) {
	changes, err := object.DiffTreeWithOptions(ctx, base, head, object.DefaultDiffTreeOptions)
	if err != nil {
		return nil, fmt.Errorf("indexer: diff trees: %w", err)
	}
	var changed []string
	seen := map[string]bool{}
	note := func(p string) {
		if p != "" && !seen[p] {
			seen[p] = true
			changed = append(changed, p)
		}
	}
	for _, c := range changes {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		note(c.From.Name)
		note(c.To.Name)
		if c.To.Name == "" {
			continue
		}
		f, err := head.File(c.To.Name)
		if err != nil {
			continue
		}
		if err := b.file(f); err != nil {
			return changed, err
		}
	}
	return changed, nil
}

func (b *builder) file(f *object.File) error {
	if b.stats.Files >= b.opts.MaxFiles || b.stats.Bytes >= b.opts.MaxBytes || len(b.chunks) >= b.opts.MaxChunks {
		b.stats.Truncated = true
		return ErrBudgetReached
	}
	if f.Size > int64(b.opts.MaxFileBytes) || b.ignored(f.Name) {
		b.stats.Skipped++
		return nil
	}
	lang, _ := chunk.Language(f.Name)
	if lang == "" {
		b.stats.Skipped++
		return nil
	}
	// Read once, as bytes: a binary file, which holds a NUL, is skipped. So
	// is one that is not UTF-8, which will otherwise fail the staging insert
	// and so the whole run, since Postgres text holds valid UTF-8 only.
	src, err := readBlob(f)
	if err != nil || len(src) == 0 || bytes.IndexByte(src, 0) >= 0 || !utf8.Valid(src) {
		b.stats.Skipped++
		return nil
	}
	if b.stats.Bytes+len(src) > b.opts.MaxBytes {
		b.stats.Truncated = true
		return ErrBudgetReached
	}
	b.stats.Files++
	b.stats.Bytes += len(src)
	pf := b.parser.Parse(f.Name, src)
	defer pf.Close()
	if len(pf.Decls) > 0 {
		b.stats.Parsed++
	}
	for _, d := range Pieces(pf, b.opts) {
		// A window of a file without declarations can be white space
		// alone, a line too long to share a window with its neighbours, and
		// a piece cut to MaxChunkBytes can keep only its white space.
		text := textcut.Prefix(pf.Text(d.StartLine, d.EndLine), b.opts.MaxChunkBytes)
		if strings.IndexFunc(text, chunk.Worded) < 0 {
			continue
		}
		if len(b.chunks) >= b.opts.MaxChunks {
			b.stats.Truncated = true
			return ErrBudgetReached
		}
		var signature string
		if d.HeadLine > 0 && !d.Contains(d.HeadLine) {
			signature = textcut.Prefix(strings.TrimSpace(pf.Text(d.HeadLine, d.HeadLine)), maxSignatureBytes)
		}
		b.chunks = append(b.chunks, Chunk{
			Path: f.Name, Language: pf.Language, Symbol: d.Symbol, Kind: d.Kind, Scope: d.Scope,
			StartLine: d.StartLine, EndLine: d.EndLine, Text: text, Signature: signature,
		})
	}
	return nil
}

// readBlob reads the file's contents in one pass.
func readBlob(f *object.File) ([]byte, error) {
	r, err := f.Reader()
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()
	return io.ReadAll(r)
}

// Pieces picks the line ranges to store, which cover every line of the file
// that holds a word, but for comments that lead into no declaration. A
// declaration over MaxDeclLines, and one that holds named ones and is unnamed
// (a C++ namespace, a Python if, an HCL body) or over MaxChunkBytes, is
// covered by its nested declarations, each cut the same way, and by windows
// of the lines between them, under its own symbol. Any other declaration is
// one piece. Imports and unnamed statements at the top level (a package
// clause, assignments) are merged into runs of up to WindowLines. Other named
// declarations are not merged, so each keeps the symbol that a search result
// reports. A file with no declarations is cut into windows. No piece's text
// is over MaxChunkBytes unless it is one line.
//
// A piece is whole lines, and declarations can share one, so a line is
// stored once where that loses nothing. Of the pieces that start on one
// line, which a search, telling its hits apart by path and start line, would
// see as one, only the longest is kept, as it holds the others' lines, and a
// named one before an unnamed one of the same lines. An unnamed piece whose
// lines a piece that starts on an earlier line holds is dropped, as a
// statement after a declaration's closing bracket is. A named one is kept for
// its symbol, as a function that starts on another's last line is, so that
// line is stored twice. So is the line that two pieces meet on when each
// holds lines of its own. The windows of a range overlap by WindowOverlap.
func Pieces(f *chunk.File, opts Options) []chunk.Decl {
	if len(f.Decls) == 0 {
		return chunk.Windows(f.Source, opts.WindowLines, opts.WindowOverlap, opts.MaxChunkBytes)
	}
	c := cutter{f: f, opts: opts, kids: nesting(f.Decls)}
	var out, run []chunk.Decl
	flush := func() {
		if len(run) > 0 {
			out = append(out, merge(run))
			run = run[:0]
		}
	}
	for _, i := range c.kids[0] {
		d := f.Decls[i]
		if d.Symbol != "" && d.Kind != importKind || d.Lines() > opts.WindowLines || c.split(i) {
			flush()
			out = append(out, c.cover(i)...)
			continue
		}
		if len(run) > 0 && (d.EndLine-run[0].StartLine >= opts.WindowLines ||
			len(f.Text(run[0].StartLine, d.EndLine)) > opts.MaxChunkBytes) {
			flush()
		}
		run = append(run, d)
	}
	flush()
	var fit []chunk.Decl
	at := map[int]int{}
	for _, p := range out {
		for _, w := range c.windows(p, p.StartLine, p.EndLine, opts.MaxDeclLines) {
			j, ok := at[w.StartLine]
			switch {
			case !ok:
				at[w.StartLine] = len(fit)
				fit = append(fit, w)
			case w.EndLine > fit[j].EndLine || w.EndLine == fit[j].EndLine && fit[j].Symbol == "" && w.Symbol != "":
				fit[j] = w
			}
		}
	}
	// The pieces come in line order, so one that ends by the furthest end
	// before it lies in an earlier piece.
	kept, end := fit[:0], 0
	for _, p := range fit {
		if p.Symbol == "" && p.EndLine <= end {
			continue
		}
		kept = append(kept, p)
		end = max(end, p.EndLine)
	}
	return kept
}

// importKind is the kind that chunk gives a package clause or an import.
const importKind = "import"

// merge is one piece for a run of top-level statements.
func merge(run []chunk.Decl) chunk.Decl {
	if len(run) == 1 {
		return run[0]
	}
	kind := importKind
	if slices.ContainsFunc(run, func(d chunk.Decl) bool { return d.Kind != importKind }) {
		kind = "other"
	}
	return chunk.Decl{Kind: kind, StartLine: run[0].StartLine, EndLine: run[len(run)-1].EndLine}
}

// nesting lists, for each declaration of ds (sorted outermost first, as
// chunk.File.Decls is), the indexes of the declarations directly inside it:
// kids[i+1] for ds[i], and kids[0] for the top level.
func nesting(ds []chunk.Decl) [][]int {
	kids := make([][]int, len(ds)+1)
	var stack []int
	for i, d := range ds {
		for len(stack) > 0 {
			p := ds[stack[len(stack)-1]]
			if p.StartByte <= d.StartByte && d.EndByte <= p.EndByte {
				break
			}
			stack = stack[:len(stack)-1]
		}
		parent := 0
		if len(stack) > 0 {
			parent = stack[len(stack)-1] + 1
		}
		kids[parent] = append(kids[parent], i)
		stack = append(stack, i)
	}
	return kids
}

// minGapLines is the fewest worded lines that a gap between an oversized
// declaration's nested ones needs to be stored by itself. A smaller gap, such
// as a field or a class's opening line, joins the nested declaration next to
// it when the two fit one piece.
const minGapLines = 3

type cutter struct {
	f    *chunk.File
	opts Options
	kids [][]int
}

// segment is a piece of an oversized declaration: a nested declaration
// stored whole, or a part of it, or the lines between nested ones.
type segment struct {
	chunk.Decl
	whole, gap bool
}

// split reports whether declaration i is stored as its nested declarations
// and the lines between them rather than whole: it is over MaxDeclLines, or
// it holds a declaration that has a symbol and either has none itself or is
// over MaxChunkBytes. Stored whole, one over MaxChunkBytes is cut into
// windows under its own symbol, which the declarations it holds lose.
func (c *cutter) split(i int) bool {
	d := c.f.Decls[i]
	return d.Lines() > c.opts.MaxDeclLines ||
		(d.Symbol == "" || len(c.f.Bytes(d.StartLine, d.EndLine)) > c.opts.MaxChunkBytes) && c.named(i)
}

// named reports whether a declaration nested in declaration i has a symbol.
func (c *cutter) named(i int) bool {
	for _, k := range c.kids[i+1] {
		if c.f.Decls[k].Symbol != "" || c.named(k) {
			return true
		}
	}
	return false
}

// cover returns the pieces of declaration i.
func (c *cutter) cover(i int) []chunk.Decl {
	d := c.f.Decls[i]
	if !c.split(i) {
		return []chunk.Decl{d}
	}
	var segs []segment
	gap := func(start, end int) {
		for start <= end && !c.worded(start) {
			start++
		}
		for end >= start && !c.worded(end) {
			end--
		}
		if start <= end {
			segs = append(segs, segment{
				Symbol: d.Symbol, Kind: d.Kind, Scope: d.Scope, StartLine: start, EndLine: end, HeadLine: d.HeadLine, gap: true,
			})
		}
	}
	next := d.StartLine
	for _, k := range c.kids[i+1] {
		n := c.f.Decls[k]
		gap(next, n.StartLine-1)
		if !c.split(k) {
			segs = append(segs, segment{Decl: n, whole: true})
		} else {
			for _, p := range c.cover(k) {
				segs = append(segs, segment{Decl: p})
			}
		}
		next = max(next, n.EndLine+1)
	}
	gap(next, d.EndLine)
	// fits reports whether lines start to end can be one piece, so that a gap
	// joined to a whole declaration does not have it cut into windows.
	fits := func(start, end int) bool {
		return end-start < c.opts.MaxDeclLines && len(c.f.Bytes(start, end)) <= c.opts.MaxChunkBytes
	}
	var out []chunk.Decl
	for j, s := range segs {
		if s.gap && c.words(s.StartLine, s.EndLine) < minGapLines {
			if j+1 < len(segs) && segs[j+1].whole && fits(s.StartLine, segs[j+1].EndLine) {
				segs[j+1].StartLine = s.StartLine
				continue
			}
			if last := len(out) - 1; j > 0 && segs[j-1].whole && last >= 0 && fits(out[last].StartLine, s.EndLine) {
				out[last].EndLine = s.EndLine
				continue
			}
		}
		if s.gap {
			out = append(out, c.windows(s.Decl, s.StartLine, s.EndLine, c.opts.WindowLines)...)
		} else {
			out = append(out, s.Decl)
		}
	}
	return out
}

// windows returns lines start to end as one piece attributed to d when they
// are at most lines lines and MaxChunkBytes, or else cuts them into windows
// of at most WindowLines and MaxChunkBytes, leaving out a window that holds
// no word.
func (c *cutter) windows(d chunk.Decl, start, end, lines int) []chunk.Decl {
	text := c.f.Text(start, end)
	if end-start < lines && len(text) <= c.opts.MaxChunkBytes || start == end {
		d.StartLine, d.EndLine = start, end
		return []chunk.Decl{d}
	}
	var out []chunk.Decl
	for _, w := range chunk.Windows([]byte(text), c.opts.WindowLines, c.opts.WindowOverlap, c.opts.MaxChunkBytes) {
		d.StartLine, d.EndLine = start+w.StartLine-1, start+w.EndLine-1
		if c.words(d.StartLine, d.EndLine) > 0 {
			out = append(out, d)
		}
	}
	return out
}

// worded reports whether line holds a word, which a line of brackets and
// punctuation alone does not.
func (c *cutter) worded(line int) bool {
	return strings.IndexFunc(c.f.Text(line, line), chunk.Worded) >= 0
}

// words counts the worded lines from start to end.
func (c *cutter) words(start, end int) int {
	n := 0
	for l := start; l <= end; l++ {
		if c.worded(l) {
			n++
		}
	}
	return n
}
