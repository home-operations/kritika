// Package chunk cuts source files into declarations. It is the one place
// that speaks tree-sitter: the runner uses it for the PR-head overlay,
// definitions and callers, and the indexer for the embedding index, so
// both see the same chunks.
//
// Parsing is pure Go through gotreesitter with every grammar embedded, so
// any file a repository holds parses without a rebuild. A
// language whose grammar carries no tags query (configuration formats,
// mostly) has no declarations and falls back to fixed windows.
package chunk

import (
	"bytes"
	"cmp"
	"maps"
	"math"
	"regexp"
	"slices"
	"strings"
	"sync"
	"unicode"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

// Decl is one declaration in a file: a function, method, type, constant,
// or whatever the language calls a top-level thing. Lines are 1-based and
// inclusive.
type Decl struct {
	Symbol    string
	Kind      string
	Scope     string
	StartLine int
	EndLine   int
	StartByte int
	EndByte   int
	// HeadLine is the first line of the declaration that is not blank and
	// not a comment, decorator or attribute leading into it: the line that
	// names it. It is zero for a window.
	HeadLine int
}

// Lines is how many lines the declaration spans.
func (d Decl) Lines() int { return d.EndLine - d.StartLine + 1 }

// Contains reports whether line falls inside the declaration.
func (d Decl) Contains(line int) bool { return line >= d.StartLine && line <= d.EndLine }

// Limits bound one parse: a file over MaxBytes, defaultMaxBytes when zero,
// is not parsed at all.
type Limits struct {
	MaxBytes int
}

const defaultMaxBytes = 1 << 20

// A parse has no timeout. Every limit on it counts the parser's work, not
// time, so that a file is cut the same on a loaded machine as on an idle
// one, which the index's chunks and the vectors kept by their text rely on.
// A deadline that cuts error recovery short returns a tree other than an
// unbounded parse's, with no error and an accepted stop reason
// (odvcencio/gotreesitter#1600), so a timeout added here would also have to
// drop a tree whose RetryStopReason, added by odvcencio/gotreesitter#1619,
// is not ParseStopNone. The limits below and the library's own per-byte
// ones stop pathological inputs in about a second.

// parseMemoryBudget bounds one parse's arena and scratch memory. The
// library's default is the larger of 512 MiB and 512 bytes per input byte.
// That default is past a runner pod's memory limit for a file of the size
// that defaultMaxBytes lets through. A parse that it stops falls back to
// fixed windows.
const parseMemoryBudget = 256 << 20

// parseStackDepthLimit stops a parse whose stack runs deep, as a long run of
// open brackets drives it: the library recovers at the end of such a file
// for minutes otherwise.
const parseStackDepthLimit = 4096

// Parser cuts files. It caches one tree-sitter parser per language and
// serialises every parse, since parsers are not safe for concurrent use.
type Parser struct {
	Limits Limits

	mu      sync.Mutex
	parsers map[string]*languageParser
}

type languageParser struct {
	entry    grammars.LangEntry
	lang     *gotreesitter.Language
	parser   *gotreesitter.Parser
	outliner *gotreesitter.Outliner
}

// Language names the grammar a path parses with, and whether that
// grammar can outline declarations. Detection is by extension and
// filename, the same table the indexer uses.
func Language(path string) (name string, declarations bool) {
	entry := grammars.DetectLanguage(path)
	if entry == nil {
		return "", false
	}
	return entry.Name, strings.TrimSpace(grammars.ResolveTagsQuery(*entry)) != ""
}

// File is a parsed file.
type File struct {
	Path        string
	Language    string
	Source      []byte
	Decls       []Decl
	lineOffsets []int
	// continued is the lines, in order, that a lead node which began on a
	// line above continues onto.
	continued []int

	lang *gotreesitter.Language
	tree *gotreesitter.Tree
}

// Parse cuts src into declarations. Some cases return a File with no
// declarations and a nil error. They are a path with no grammar, a grammar
// with no tags query, a file over the byte limit, and a failed parse. Those
// are the fixed-window cases, not failures.
func (p *Parser) Parse(path string, src []byte) *File {
	f := &File{Path: path, Source: src}
	if len(src) == 0 || len(src) > cmp.Or(p.Limits.MaxBytes, defaultMaxBytes) || bytes.IndexByte(src, 0) >= 0 {
		return f
	}
	lp := p.language(path)
	if lp == nil {
		return f
	}
	f.Language = lp.entry.Name
	if lp.outliner == nil {
		return f
	}
	p.mu.Lock()
	tree, err := lp.parser.ParseStrict(src)
	p.mu.Unlock()
	// A parse stopped early returns its partial tree with the error. One
	// that recovers from errors can also return a tree that ends before the
	// source does, with none: odvcencio/gotreesitter#1632 fixed some C++
	// cases, and others are not fixed upstream.
	if err != nil || tree == nil || tree.RootNode() == nil ||
		int(tree.RootNode().EndByte()) < len(bytes.TrimRightFunc(src, unicode.IsSpace)) {
		tree.Release()
		return f
	}
	f.lang, f.tree = lp.lang, tree
	f.Decls, f.continued = declarations(lp, tree, src)
	return f
}

// Close gives the parse tree's memory back to the library's pool, which a
// parse takes a fresh arena from otherwise. The declarations and the source
// stay. Identifiers, which walks the tree, finds none afterward.
func (f *File) Close() {
	if f.tree != nil {
		f.tree.Release()
		f.tree = nil
	}
}

func (p *Parser) language(path string) *languageParser {
	entry := grammars.DetectLanguage(path)
	if entry == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if lp, ok := p.parsers[entry.Name]; ok {
		return lp
	}
	lang := entry.Language()
	parser := gotreesitter.NewParser(lang)
	parser.SetMemoryBudgetBytes(parseMemoryBudget)
	parser.SetParseWorkLimits(gotreesitter.ParseWorkLimits{StackDepthLimit: parseStackDepthLimit})
	lp := &languageParser{entry: *entry, lang: lang, parser: parser}
	if q := strings.TrimSpace(grammars.ResolveTagsQuery(*entry)); q != "" {
		// A tags query that fails to compile leaves the language without
		// declarations rather than without parsing.
		lp.outliner, _ = gotreesitter.NewOutliner(lang, q)
	}
	if p.parsers == nil {
		p.parsers = map[string]*languageParser{}
	}
	p.parsers[entry.Name] = lp
	return lp
}

// declarations merges two views of the tree. One is the outline: named
// symbols from the grammar's tags query, nested where the language nests
// them. The other is the root's children: every top-level statement, so a
// type or constant block that the tags query does not cover still becomes a
// chunk. It also returns the lines that a lead node continues onto, as
// leadLines does.
func declarations(lp *languageParser, tree *gotreesitter.Tree, src []byte) (decls []Decl, continued []int) {
	root := tree.RootNode()
	starts := lineStarts(src)
	text := lineTexts(src, starts)
	lead, continued := leadLines(root, lp.lang, src, text)
	var out []Decl
	seen := map[[2]int]bool{}
	// A statement holding one outlined symbol over the same lines (a Go
	// type_spec in its type_declaration) is one declaration, not two.
	type span struct {
		symbol     string
		start, end int
	}
	named := map[span]bool{}
	add := func(d Decl) bool {
		key, lines := [2]int{d.StartByte, d.EndByte}, span{d.Symbol, d.StartLine, d.EndLine}
		if seen[key] || named[lines] || d.EndByte <= d.StartByte {
			return false
		}
		seen[key], named[lines] = true, true
		d.HeadLine = headLine(d, lead, text)
		out = append(out, d)
		return true
	}
	symbols, _ := lp.outliner.OutlineTree(tree)
	var top []int
	var walk func(ss []gotreesitter.OutlineSymbol, owner string)
	walk = func(ss []gotreesitter.OutlineSymbol, owner string) {
		for _, s := range ss {
			scope := cmp.Or(s.Owner, owner)
			if add(Decl{
				Symbol: s.Name, Kind: s.Kind, Scope: scope,
				StartLine: int(s.Range.StartPoint.Row) + 1, EndLine: endLine(s.Range.StartPoint, s.Range.EndPoint),
				StartByte: int(s.Range.StartByte), EndByte: int(s.Range.EndByte),
			}) && owner == "" {
				top = append(top, len(out)-1)
			}
			walk(s.Children, s.Name)
		}
	}
	walk(symbols, "")
	// Methods carry their receiver as scope when the outline gave none.
	for i := range out {
		if out[i].Scope == "" && out[i].Kind == "method" {
			if n := nodeAt(root, uint32(out[i].StartByte), uint32(out[i].EndByte)); n != nil {
				_, out[i].Scope = nameOf(n, lp.lang, src)
			}
		}
	}
	slices.SortFunc(top, func(a, b int) int { return cmp.Compare(out[a].StartByte, out[b].StartByte) })
	// loose holds the attributes and decorators at the top level, which the
	// declaration below them usually takes in.
	var loose []Decl
	for i := range root.ChildCount() {
		c := root.Child(i)
		typ := c.Type(lp.lang)
		// Error recovery can leave a grammar's hidden nodes (a
		// translation_unit_repeat1) at the top level in place of the
		// statements they hold, so only an anonymous token is passed over.
		// odvcencio/gotreesitter#1632 fixed some C++ cases, and others are
		// not fixed upstream.
		if !c.IsNamed() && c.ChildCount() == 0 || comment(typ) || bytes.IndexFunc(src[c.StartByte():c.EndByte()], Worded) < 0 {
			continue
		}
		d := Decl{
			Kind:      kindFromType(typ),
			StartLine: int(c.StartPoint().Row) + 1, EndLine: endLine(c.StartPoint(), c.EndPoint()),
			StartByte: int(c.StartByte()), EndByte: int(c.EndByte()),
		}
		if leads(typ) {
			loose = append(loose, d)
			continue
		}
		d.Symbol, d.Scope = nameOf(c, lp.lang, src)
		// An export, a decorated definition or a C++ template around one
		// outlined declaration widens that declaration, which keeps its
		// symbol. A parse error is no such statement.
		var bare *gotreesitter.Node
		if d.Symbol == "" && !c.IsError() {
			bare = c
		}
		if j := wrapped(out, top, d, lead, bare, lp.lang); j >= 0 {
			out[j].StartLine, out[j].StartByte = d.StartLine, d.StartByte
			continue
		}
		if dn := c.ChildByFieldName("declaration", lp.lang); dn != nil && d.Kind == "other" {
			d.Kind = kindFromType(dn.Type(lp.lang))
		}
		add(d)
	}
	takeLeads(out, lead, starts)
	for _, d := range unheld(out, loose, len(starts)) {
		add(d)
	}
	slices.SortFunc(out, func(a, b Decl) int {
		return cmp.Or(cmp.Compare(a.StartByte, b.StartByte), cmp.Compare(b.EndByte, a.EndByte))
	})
	return out, continued
}

// takeLeads widens each declaration of out over the lead lines directly
// above it, starting at lines starts. A lead line that ends another
// declaration is that declaration's. The lead lines above a line are the
// first declaration's on it: another that starts later on the line would
// otherwise start at the same byte and, being longer, read as the first
// one's parent.
func takeLeads(out []Decl, lead []bool, starts []int) {
	ends, first := make([]bool, len(lead)), slices.Repeat([]int{math.MaxInt}, len(lead))
	for _, d := range out {
		ends[d.EndLine] = true
		first[d.StartLine] = min(first[d.StartLine], d.StartByte)
	}
	// above is, by line, the first of the lead lines that run up to it, in
	// one pass rather than a walk up from each declaration, as one line can
	// start many.
	above := make([]int, len(lead))
	for l := 1; l < len(above); l++ {
		above[l] = l
		if l > 1 && lead[l-1] && !ends[l-1] {
			above[l] = above[l-1]
		}
	}
	if walked != nil {
		*walked += len(above)
	}
	for i, d := range out {
		if line := above[d.StartLine]; line < d.StartLine && d.StartByte == first[d.StartLine] {
			out[i].StartLine, out[i].StartByte = line, starts[line-1]
		}
	}
}

// unheld returns the declarations of loose that hold a line which no
// declaration of out holds, in a file of lines lines.
func unheld(out, loose []Decl, lines int) []Decl {
	if len(loose) == 0 {
		return nil
	}
	// held counts, by line, the declarations that hold it.
	held := make([]int, lines+2)
	for _, d := range out {
		held[d.StartLine]++
		held[min(d.EndLine+1, len(held)-1)]--
	}
	for l := 1; l < len(held); l++ {
		held[l] += held[l-1]
	}
	var alone []Decl
	for _, d := range loose {
		if slices.Contains(held[d.StartLine:min(d.EndLine+1, len(held))], 0) {
			alone = append(alone, d)
		}
	}
	return alone
}

// endLine is the 1-based last line of a node from start to end. A node that
// ends at the start of a line, as a C preprocessor directive that holds its
// newline does, ends on the line before.
func endLine(start, end gotreesitter.Point) int {
	if end.Column == 0 && end.Row > start.Row {
		return int(end.Row)
	}
	return int(end.Row) + 1
}

// Worded reports whether r can be part of a word: a letter, a digit, or the
// $ and _ that identifiers in many languages are made of. Text with none of
// these is punctuation alone, such as an empty statement or a lone bracket,
// which the chunks leave out.
func Worded(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '$' }

// headLine is the first line of d that holds more than lead nodes and
// white space, or d's first line when none does.
func headLine(d Decl, lead []bool, text [][2]int) int {
	for l := d.StartLine; l <= d.EndLine && l <= len(text); l++ {
		if !lead[l] && text[l-1][0] < text[l-1][1] {
			return l
		}
	}
	return d.StartLine
}

// lineTexts is, by 0-based row, the byte range of the row's text, without the
// white space around it or the byte order mark that starts a file. It is
// computed once for the file, as a row that white space pads can hold many
// declarations.
func lineTexts(src []byte, starts []int) [][2]int {
	text := make([][2]int, len(starts))
	for row, lo := range starts {
		hi := len(src)
		if row+1 < len(starts) {
			hi = starts[row+1]
		}
		if row == 0 && bytes.HasPrefix(src, byteOrderMark) {
			lo += len(byteOrderMark)
		}
		line := src[lo:hi]
		trimmed := bytes.TrimLeftFunc(line, unicode.IsSpace)
		lo += len(line) - len(trimmed)
		text[row] = [2]int{lo, lo + len(bytes.TrimRightFunc(trimmed, unicode.IsSpace))}
		if walked != nil {
			*walked += len(line) - (text[row][1] - text[row][0])
		}
	}
	return text
}

var byteOrderMark = []byte("\ufeff")

// walked, when a test points it at a count, adds up the white space that
// lineTexts and skipSpace walk and the lines that takeLeads does. Walking
// them again for each declaration or comment on a row is quadratic in the
// file's size, and the count shows that on any machine, where a bound on
// the parse's time holds only on a known one. A test that sets it does not
// run in parallel, as declarations runs outside the parser's lock.
var walked *int

// nodeAt finds the node with exactly the given byte range. It descends from
// root, and returns nil when no node has it.
func nodeAt(root *gotreesitter.Node, start, end uint32) *gotreesitter.Node {
	n := root
	for n != nil {
		if n.StartByte() == start && n.EndByte() == end {
			return n
		}
		var next *gotreesitter.Node
		for i := range n.NamedChildCount() {
			c := n.NamedChild(i)
			if c.StartByte() <= start && c.EndByte() >= end {
				next = c
				break
			}
		}
		n = next
	}
	return nil
}

// nameOf returns a node's declared name and scope from the grammar's "name"
// and "receiver" fields. It falls back to the first identifier inside a
// one-declaration block (Go's type and const groups).
func nameOf(n *gotreesitter.Node, lang *gotreesitter.Language, src []byte) (name, scope string) {
	if nn := n.ChildByFieldName("name", lang); nn != nil {
		name = strings.TrimSpace(nn.Text(src))
	}
	if r := n.ChildByFieldName("receiver", lang); r != nil {
		text := strings.Trim(strings.TrimSpace(r.Text(src)), "()")
		// "(w *Review)" -> "Review"; "(Review)" -> "Review"
		fields := strings.Fields(text)
		if len(fields) > 0 {
			scope = strings.TrimLeft(fields[len(fields)-1], "*&")
		}
	}
	if dn := n.ChildByFieldName("declaration", lang); name == "" && dn != nil {
		// An export statement names what it exports.
		name, _ = nameOf(dn, lang, src)
	}
	if name == "" {
		// A block of specs (Go's const and type groups) takes its first
		// member's name. A bare identifier child is the name itself.
		for i := range n.NamedChildCount() {
			c := n.NamedChild(i)
			if strings.Contains(c.Type(lang), "identifier") && c.ChildCount() == 0 {
				name = c.Text(src)
			} else if strings.Contains(c.Type(lang), "spec") || strings.Contains(c.Type(lang), "declarator") {
				name, _ = nameOf(c, lang, src)
			}
			if name != "" {
				break
			}
		}
	}
	return name, scope
}

func kindFromType(typ string) string {
	switch {
	case strings.Contains(typ, "method"):
		return "method"
	case strings.Contains(typ, "function"), strings.Contains(typ, "func"):
		return "function"
	case strings.Contains(typ, "type"), strings.Contains(typ, "struct"), strings.Contains(typ, "enum"),
		strings.Contains(typ, "interface"), strings.Contains(typ, "class"):
		return "type"
	case strings.Contains(typ, "const"):
		return "constant"
	case strings.Contains(typ, "var"), strings.Contains(typ, "let"):
		return "variable"
	case strings.Contains(typ, "import"), strings.Contains(typ, "use_"), strings.Contains(typ, "package"):
		return "import"
	default:
		return "other"
	}
}

// Enclosing returns the smallest declaration that contains every line in
// lines and spans at most maxLines from its head line, or nil. Declarations
// are nested, so "smallest" prefers a method over its class. A declaration
// that the comments, decorators or attributes above it put over maxLines is
// returned from the first of lines, when that is above its head line and
// leaves it within maxLines, or else from its head line, and holds only the
// lines from there. A first of lines inside a comment, decorator or
// attribute that began above it counts from the line where that began, so
// the declaration does not start inside one, such as at an argument of a
// decorator that spans lines.
func (f *File) Enclosing(lines []int, maxLines int) *Decl {
	first := math.MaxInt
	for _, l := range lines {
		first = min(first, l)
	}
	// best is the one copy that leaves the loop, so the others stay off the
	// heap.
	var best Decl
	found := false
	for _, d := range f.Decls {
		head := max(d.HeadLine, d.StartLine)
		if d.EndLine-head+1 > maxLines {
			continue
		}
		if d.Lines() > maxLines {
			start := head
			if first >= d.StartLine && first < head {
				open := first
				for open > d.StartLine && f.LeadContinues(open) {
					open--
				}
				if d.EndLine-open+1 <= maxLines {
					start = open
				}
			}
			d.StartLine, d.StartByte = start, f.offsets()[start-1]
		}
		ok := true
		for _, l := range lines {
			if !d.Contains(l) {
				ok = false
				break
			}
		}
		if ok && (!found || d.EndLine-head < best.EndLine-max(best.HeadLine, best.StartLine)) {
			best, found = d, true
		}
	}
	if !found {
		return nil
	}
	return &best
}

var identifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Identifiers returns the identifiers that appear on the given lines, most
// frequent first. Only named leaf nodes whose type mentions "identifier"
// count, which excludes keywords (anonymous), strings, numbers and
// comments in every grammar that follows tree-sitter conventions.
func (f *File) Identifiers(lines []int) []string {
	counts := map[string]int{}
	f.identifiers(lines, func(_ int, id string) { counts[id]++ })
	out := slices.Collect(maps.Keys(counts))
	slices.SortFunc(out, func(a, b string) int { return cmp.Or(cmp.Compare(counts[b], counts[a]), cmp.Compare(a, b)) })
	return out
}

// LineIdentifiers returns, by line, the identifiers that appear on each of
// the given lines, as Identifiers counts them, from one walk of the tree.
func (f *File) LineIdentifiers(lines []int) map[int][]string {
	by := map[int][]string{}
	f.identifiers(lines, func(line int, id string) { by[line] = append(by[line], id) })
	return by
}

// identifiers calls yield with each identifier on the given lines and its
// line. It descends only into the nodes that span one of the lines.
func (f *File) identifiers(lines []int, yield func(line int, id string)) {
	if f.tree == nil || len(lines) == 0 {
		return
	}
	want := slices.Compact(slices.Sorted(slices.Values(lines)))
	var walk func(n *gotreesitter.Node)
	walk = func(n *gotreesitter.Node) {
		row := int(n.StartPoint().Row) + 1
		k, _ := slices.BinarySearch(want, row)
		if k == len(want) || want[k] > int(n.EndPoint().Row)+1 {
			return
		}
		if n.ChildCount() == 0 {
			if n.IsNamed() && want[k] == row && strings.Contains(n.Type(f.lang), "identifier") {
				if t := n.Text(f.Source); identifier.MatchString(t) {
					yield(row, t)
				}
			}
			return
		}
		for i := range n.ChildCount() {
			walk(n.Child(i))
		}
	}
	walk(f.tree.RootNode())
}

// lineCount is the number of lines in src, a final newline ending the last
// line rather than starting an empty one.
func lineCount(src []byte) int {
	n := bytes.Count(src, []byte{'\n'})
	if len(src) > 0 && src[len(src)-1] != '\n' {
		n++
	}
	return n
}

// lineStarts is the byte offset of each line of src.
func lineStarts(src []byte) []int {
	starts := make([]int, 1, lineCount(src))
	for i, c := range src {
		if c == '\n' && i+1 < len(src) {
			starts = append(starts, i+1)
		}
	}
	return starts
}

// Window returns the fixed-size fallback: the lines from first-radius to
// last+radius, clamped to the file. Lines are 1-based, inclusive. Repeated
// calls reuse the file's line offsets, including after Close.
func (f *File) Window(first, last, radius int) (startLine, endLine int, text string) {
	startLine, endLine = max(first-radius, 1), min(last+radius, len(f.offsets()))
	if startLine > endLine {
		return 0, 0, ""
	}
	return startLine, endLine, string(f.Bytes(startLine, endLine))
}

// Bytes is Text without the copy: a slice of Source, which the caller must
// not change. Lines are clamped to the file.
func (f *File) Bytes(startLine, endLine int) []byte {
	startLine, endLine = max(startLine, 1), min(endLine, len(f.offsets()))
	if startLine > endLine {
		return nil
	}
	start, end := f.lineOffsets[startLine-1], len(f.Source)
	if endLine < len(f.lineOffsets) {
		end = f.lineOffsets[endLine] - 1
	} else if end > 0 && f.Source[end-1] == '\n' {
		end--
	}
	return f.Source[start:end]
}

// offsets is the byte offset of each line of the source, computed once.
func (f *File) offsets() []int {
	if f.lineOffsets == nil && len(f.Source) > 0 {
		f.lineOffsets = lineStarts(f.Source)
	}
	return f.lineOffsets
}

// Text returns the source of a line range, 1-based and inclusive.
func (f *File) Text(startLine, endLine int) string {
	_, _, text := f.Window(startLine, endLine, 0)
	return text
}

// LeadContinues reports whether a comment, decorator or attribute that began
// on a line above line continues onto it, so that text from line would start
// inside one. It holds after Close.
func (f *File) LeadContinues(line int) bool {
	_, ok := slices.BinarySearch(f.continued, line)
	return ok
}

// Windows cuts a file with no declarations into pieces for the index. Each
// piece is at most size lines and, unless maxBytes is 0, at most maxBytes
// bytes of text. A line longer than maxBytes is a piece of its own. The
// pieces overlap by overlap lines, fewer for a piece that the byte bound cut
// short, so a boundary does not hide a match. Lines are 1-based, inclusive.
func Windows(src []byte, size, overlap, maxBytes int) []Decl {
	lines := lineCount(src)
	if lines == 0 || size <= 0 {
		return nil
	}
	if overlap >= size {
		overlap = size / 4
	}
	overlap = max(overlap, 0)
	starts := lineStarts(src)
	// length is line l's text without its newline.
	length := func(l int) int {
		if l < len(starts) {
			return starts[l] - starts[l-1] - 1
		}
		return len(bytes.TrimSuffix(src[starts[l-1]:], []byte{'\n'}))
	}
	var out []Decl
	for start := 1; ; {
		end, used := start, length(start)
		for end < lines && end-start+1 < size && (maxBytes <= 0 || used+1+length(end+1) <= maxBytes) {
			end++
			used += 1 + length(end)
		}
		out = append(out, Decl{Kind: "window", StartLine: start, EndLine: end})
		if end == lines {
			return out
		}
		next := max(end+1-overlap*(end-start+1)/size, start+1)
		// The overlap gives up its oldest lines until the line after end fits
		// beside it. A line over maxBytes fits beside none, so its window
		// starts at it and holds nothing old.
		if maxBytes > 0 {
			used := length(end + 1)
			for l := next; l <= end; l++ {
				used += length(l) + 1
			}
			for used > maxBytes && next <= end {
				used -= length(next) + 1
				next++
			}
		}
		start = next
	}
}

// Matches reports whether path matches any of the doublestar globs. The code
// that reads a glob from the configuration or a repository file makes sure
// that the glob is valid. So Matches does not do that again.
func Matches(globs []string, path string) bool {
	for _, g := range globs {
		if doublestar.MatchUnvalidated(g, path) {
			return true
		}
	}
	return false
}
