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
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
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

// parseTimeout stops a pathological parse.
const parseTimeout = 5 * time.Second

// parseMemoryBudget bounds one parse's arena and scratch memory. The
// library's default is the larger of 512 MiB and 512 bytes per input byte.
// That default is past a runner pod's memory limit for a file of the size
// that defaultMaxBytes lets through. A parse that it stops falls back to
// fixed windows.
const parseMemoryBudget = 256 << 20

// parseStackDepthLimit stops a parse whose stack runs deep, as a long run of
// open brackets drives it: the library recovers at the end of such a file
// without heeding parseTimeout, for minutes.
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
	if err != nil || tree == nil || tree.RootNode() == nil {
		// A parse stopped early returns its partial tree with the error.
		tree.Release()
		return f
	}
	f.lang, f.tree = lp.lang, tree
	f.Decls = declarations(lp, tree, src)
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
	parser.SetTimeoutMicros(uint64(parseTimeout.Microseconds()))
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
// them. The other is the root's named children: every top-level statement, so
// a type or constant block that the tags query does not cover still becomes a
// chunk.
func declarations(lp *languageParser, tree *gotreesitter.Tree, src []byte) []Decl {
	root := tree.RootNode()
	starts := lineStarts(src)
	lead := leadLines(root, lp.lang, src, starts)
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
		d.HeadLine = headLine(d, lead, src, starts)
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
	for i := range root.NamedChildCount() {
		c := root.NamedChild(i)
		typ := c.Type(lp.lang)
		if strings.Contains(typ, "comment") || c.EndPoint().Row == c.StartPoint().Row && c.EndByte()-c.StartByte() < 2 {
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
		// An export or a decorated definition around one outlined
		// declaration widens that declaration, which keeps its symbol.
		if j := wrapped(out, top, d, lead); j >= 0 {
			out[j].StartLine, out[j].StartByte = d.StartLine, d.StartByte
			continue
		}
		if dn := c.ChildByFieldName("declaration", lp.lang); dn != nil && d.Kind == "other" {
			d.Kind = kindFromType(dn.Type(lp.lang))
		}
		add(d)
	}
	// A lead line that ends another declaration is that declaration's.
	ends := map[int]bool{}
	for _, d := range out {
		ends[d.EndLine] = true
	}
	for i := range out {
		line := out[i].StartLine
		for line > 1 && lead[line-1] && !ends[line-1] {
			line--
		}
		if line < out[i].StartLine {
			out[i].StartLine, out[i].StartByte = line, starts[line-1]
		}
	}
	for _, d := range unheld(out, loose, len(starts)) {
		add(d)
	}
	slices.SortFunc(out, func(a, b Decl) int {
		return cmp.Or(cmp.Compare(a.StartByte, b.StartByte), cmp.Compare(b.EndByte, a.EndByte))
	})
	return out
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

// headLine is the first line of d that holds more than lead nodes and
// white space, or d's first line when none does.
func headLine(d Decl, lead []bool, src []byte, starts []int) int {
	for l := d.StartLine; l <= d.EndLine && l <= len(starts); l++ {
		if lo, hi := lineText(src, starts, l-1); !lead[l] && lo < hi {
			return l
		}
	}
	return d.StartLine
}

// lineText is the byte range of the text of 0-based row, without the white
// space around it or the byte order mark that starts a file.
func lineText(src []byte, starts []int, row int) (lo, hi int) {
	lo, hi = starts[row], len(src)
	if row+1 < len(starts) {
		hi = starts[row+1]
	}
	if row == 0 && bytes.HasPrefix(src, byteOrderMark) {
		lo += len(byteOrderMark)
	}
	line := src[lo:hi]
	text := bytes.TrimLeftFunc(line, unicode.IsSpace)
	lo += len(line) - len(text)
	return lo, lo + len(bytes.TrimRightFunc(text, unicode.IsSpace))
}

var byteOrderMark = []byte("\ufeff")

// leads reports whether a node of type typ leads into the declaration
// below it rather than standing alone: a comment, a decorator, or an
// attribute or annotation. A Rust inner attribute (inner_attribute_item)
// applies to what encloses it, so it does not lead.
func leads(typ string) bool {
	switch typ {
	case "attribute_item", "attribute_list", "annotation", "marker_annotation":
		return true
	}
	return strings.Contains(typ, "comment") || strings.Contains(typ, "decorator")
}

// leadLines marks, by 1-based line, the lines that lead nodes fill: every
// character on the line that is not white space belongs to one such node,
// and a node that continues onto the line began on a lead line.
func leadLines(root *gotreesitter.Node, lang *gotreesitter.Language, src []byte, starts []int) []bool {
	// reach is, by 0-based row, the end of the row's text that lead nodes
	// fill from its start, with only white space between them. from is the
	// 1-based line where a lead node that continues onto the row began.
	reach, from := make([]int, len(starts)), make([]int, len(starts))
	var walk func(n *gotreesitter.Node)
	walk = func(n *gotreesitter.Node) {
		if !leads(n.Type(lang)) {
			for i := range n.ChildCount() {
				walk(n.Child(i))
			}
			return
		}
		first, last := int(n.StartPoint().Row), int(n.EndPoint().Row)
		if last > first && n.EndPoint().Column == 0 {
			last-- // a line comment that holds its newline
		}
		for row := first; row <= last && row < len(starts); row++ {
			lo, hi := lineText(src, starts, row)
			if p := skipSpace(src, cmp.Or(reach[row], lo), hi); int(n.StartByte()) <= p {
				reach[row] = max(p, int(n.EndByte()))
			}
			if row > first {
				from[row] = first + 1
			}
		}
	}
	walk(root)
	lead := make([]bool, len(starts)+1)
	for row := range starts {
		lo, hi := lineText(src, starts, row)
		lead[row+1] = lo < hi && reach[row] > 0 && skipSpace(src, reach[row], hi) >= hi &&
			(from[row] == 0 || lead[from[row]])
	}
	return lead
}

// skipSpace is the offset of the first byte from i to hi that is not white
// space, or hi.
func skipSpace(src []byte, i, hi int) int {
	for i < hi && (src[i] == ' ' || src[i] == '\t' || src[i] == '\r' || src[i] == '\n' || src[i] == '\v' || src[i] == '\f') {
		i++
	}
	return i
}

// wrapped returns the index in out of the one outlined top-level
// declaration that d wraps, or -1. d wraps it when it holds no other one,
// ends on its last line, and holds nothing above it but lead lines. top
// indexes the top-level declarations of out by StartByte.
func wrapped(out []Decl, top []int, d Decl, lead []bool) int {
	k, _ := slices.BinarySearchFunc(top, d.StartByte, func(i, b int) int { return cmp.Compare(out[i].StartByte, b) })
	if k == len(top) || k+1 < len(top) && out[top[k+1]].EndByte <= d.EndByte {
		return -1
	}
	o := out[top[k]]
	if o.EndByte > d.EndByte || o.EndLine != d.EndLine {
		return -1
	}
	for l := d.StartLine; l < o.StartLine; l++ {
		if !lead[l] {
			return -1
		}
	}
	return top[k]
}

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
// lines and spans at most maxLines, or nil. Declarations are nested, so
// "smallest" prefers a method over its class.
func (f *File) Enclosing(lines []int, maxLines int) *Decl {
	var best *Decl
	for i := range f.Decls {
		d := &f.Decls[i]
		if d.Lines() > maxLines {
			continue
		}
		ok := true
		for _, l := range lines {
			if !d.Contains(l) {
				ok = false
				break
			}
		}
		if ok && (best == nil || d.Lines() < best.Lines()) {
			best = d
		}
	}
	return best
}

var identifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Identifiers returns the identifiers that appear on the given lines, most
// frequent first. Only named leaf nodes whose type mentions "identifier"
// count, which excludes keywords (anonymous), strings, numbers and
// comments in every grammar that follows tree-sitter conventions.
func (f *File) Identifiers(lines []int) []string {
	if f.tree == nil || len(lines) == 0 {
		return nil
	}
	want := map[int]bool{}
	for _, l := range lines {
		want[l] = true
	}
	counts := map[string]int{}
	lo, hi := slices.Min(lines), slices.Max(lines)
	var walk func(n *gotreesitter.Node)
	walk = func(n *gotreesitter.Node) {
		row := int(n.StartPoint().Row) + 1
		if int(n.EndPoint().Row)+1 < lo || row > hi {
			return
		}
		if n.ChildCount() == 0 {
			if n.IsNamed() && want[row] && strings.Contains(n.Type(f.lang), "identifier") {
				if t := n.Text(f.Source); identifier.MatchString(t) {
					counts[t]++
				}
			}
			return
		}
		for i := range n.ChildCount() {
			walk(n.Child(i))
		}
	}
	walk(f.tree.RootNode())
	out := slices.Collect(maps.Keys(counts))
	slices.SortFunc(out, func(a, b string) int { return cmp.Or(cmp.Compare(counts[b], counts[a]), cmp.Compare(a, b)) })
	return out
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
	if f.lineOffsets == nil && len(f.Source) > 0 {
		f.lineOffsets = lineStarts(f.Source)
	}
	startLine, endLine = max(first-radius, 1), min(last+radius, len(f.lineOffsets))
	if startLine > endLine {
		return 0, 0, ""
	}
	start, end := f.lineOffsets[startLine-1], len(f.Source)
	if endLine < len(f.lineOffsets) {
		end = f.lineOffsets[endLine] - 1
	} else if end > 0 && f.Source[end-1] == '\n' {
		end--
	}
	return startLine, endLine, string(f.Source[start:end])
}

// Text returns the source of a line range, 1-based and inclusive.
func (f *File) Text(startLine, endLine int) string {
	_, _, text := f.Window(startLine, endLine, 0)
	return text
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
		start = max(end+1-overlap*(end-start+1)/size, start+1)
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
