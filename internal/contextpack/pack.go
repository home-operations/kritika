// Package contextpack builds the review context that the runner writes next
// to the diff. That context is the PR-head overlay (whole declarations that
// the diff touches), exact definitions of identifiers on changed lines, and
// callers of changed declarations. Everything comes from the two trees that
// the runner fetched. Nothing needs an index or a model key.
package contextpack

import (
	"bytes"
	"cmp"
	"context"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/go-git/go-git/v6/plumbing/object"

	"github.com/home-operations/kritika/internal/chunk"
	"github.com/home-operations/kritika/internal/textcut"
	"github.com/home-operations/kritika/internal/tuning"
)

// Stages, in the order the prompt spends its budget on them.
const (
	StageOverlay    = "overlay"
	StageDefinition = "definition"
	StageCaller     = "caller"
	// StageSimilar is what the gateway finds in the embedding index for the
	// diff's hunks.
	StageSimilar = "similar"
)

// SimilarRequest is what a run sends the gateway to search its
// repository's index: the texts to embed. The answer leaves out the paths
// the run reported (runspec.SimilarExcludeReport), and Exclude besides,
// which a runner of an earlier version sends in place of that report.
type SimilarRequest struct {
	Queries []string `json:"queries"`
	Exclude []string `json:"exclude,omitempty"`
	// Kind is what the queries are, which picks the embedder's prompt for
	// them: SimilarKindDiff, as "" is, or SimilarKindSearch.
	Kind string `json:"kind,omitempty"`
	// Limit is the most chunks the answer holds, up to tuning's
	// ContextPack.SimilarLimit. 0 leaves it to the gateway. One query keeps a
	// few of them at most, but for a request of SimilarKindSearch of one
	// query, which keeps up to Limit.
	Limit int `json:"limit,omitempty"`
	// Languages are, for a request of SimilarKindDiff, the language of each
	// query's file, as package chunk names it, or "" when it is unknown: the
	// search by name splits a query as the index splits that language's
	// code, where a hyphen can be a minus. A request without them, as a
	// runner of an earlier version sends, has each query split as a
	// SimilarKindSearch query is.
	Languages []string `json:"languages,omitempty"`
}

// Similar-code query kinds: the hunks of a review's diff, and the agent's
// search_code queries.
const (
	SimilarKindDiff   = "diff"
	SimilarKindSearch = "search"
)

// SimilarResponse is the gateway's answer: the nearest chunks, StageSimilar
// each, and whether the repository has an index to search at all. So a run
// can tell an empty answer from no index.
type SimilarResponse struct {
	Chunks  []Chunk `json:"chunks"`
	Indexed bool    `json:"indexed"`
}

// Chunk is one piece of context and the stage that contributed it.
type Chunk struct {
	Stage     string `json:"stage"`
	Path      string `json:"path"`
	Language  string `json:"language,omitempty"`
	Symbol    string `json:"symbol,omitempty"`
	Kind      string `json:"kind,omitempty"`
	Scope     string `json:"scope,omitempty"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	// Ref is the identifier or changed symbol that pulled the chunk in.
	Ref  string `json:"ref,omitempty"`
	Text string `json:"text"`
}

// Input is what Build reads.
type Input struct {
	Head, Base *object.Tree
	Diff       string
	// Changed lists head-side paths the diff touches.
	Changed []string
	// Ignore globs (doublestar) are skipped everywhere.
	Ignore []string
}

// Options bound the work and the output.
type Options struct {
	// MaxDeclLines is the largest declaration used whole. A bigger one gets a
	// window around the change instead.
	MaxDeclLines int
	// WindowRadius is the fixed-window fallback's reach on each side.
	WindowRadius int
	// MaxIdentifiers caps stage 2 lookups per review, most frequent first.
	MaxIdentifiers int
	// DefinitionsPerIdentifier and CallersPerSymbol cap each lookup.
	// MaxDefinitions and MaxCallers cap the stages.
	DefinitionsPerIdentifier, CallersPerSymbol int
	MaxDefinitions, MaxCallers                 int
	// MaxChunkBytes truncates one chunk. MaxPackBytes stops adding chunks.
	MaxChunkBytes, MaxPackBytes int
	// MaxScanFiles and MaxScanBytes bound the head-tree walk.
	MaxScanFiles, MaxScanBytes int
	// MaxFileBytes skips files larger than this everywhere.
	MaxFileBytes int
}

// DefaultOptions are the options as tuning's ContextPack has them, whose
// MaxPackBytes sizes a pack to the model's budget with room for the diff.
func DefaultOptions() Options {
	t := tuning.Get().ContextPack
	return Options{
		MaxDeclLines: t.MaxDeclLines, WindowRadius: t.WindowRadius, MaxIdentifiers: t.MaxIdentifiers,
		DefinitionsPerIdentifier: t.DefinitionsPerName, CallersPerSymbol: t.CallersPerSymbol,
		MaxDefinitions: t.MaxDefinitions, MaxCallers: t.MaxCallers,
		MaxChunkBytes: int(t.MaxChunkBytes), MaxPackBytes: int(t.MaxPackBytes),
		MaxScanFiles: t.MaxScanFiles, MaxScanBytes: int(t.MaxScanBytes), MaxFileBytes: int(t.MaxFileBytes),
	}
}

// Stats says what Build did, for the run log.
type Stats struct {
	Overlay, Definitions, Callers int
	Identifiers, ChangedSymbols   int
	FilesScanned, FilesParsed     int
	BytesScanned                  int
	ScanTruncated                 bool
	Elapsed                       time.Duration
}

// Build runs stages 1 to 3.
func Build(ctx context.Context, in Input, opts Options) ([]Chunk, Stats, error) {
	out, stats, err := BuildParts(ctx, in.Head, in.Base, in.Ignore, []Part{{Diff: in.Diff, Changed: in.Changed}}, opts)
	if err != nil {
		return nil, stats, err
	}
	return out[0], stats, nil
}

// Part selects one review part's diff and head-side paths.
type Part struct {
	Diff    string
	Changed []string
}

// BuildParts builds independent packs with one shared repository scan. Output
// budgets apply to each part. Scan statistics count shared work once.
func BuildParts(ctx context.Context, head, base *object.Tree, ignore []string, parts []Part, opts Options) ([][]Chunk, Stats, error) {
	if opts.MaxDeclLines == 0 {
		opts = DefaultOptions()
	}
	start := time.Now()
	var stats Stats
	builders := make([]*builder, len(parts))
	parser := &chunk.Parser{Limits: chunk.Limits{MaxBytes: opts.MaxFileBytes}}
	for i, part := range parts {
		b := &builder{in: Input{Head: head, Base: base, Ignore: ignore, Diff: part.Diff, Changed: part.Changed}, opts: opts, parser: parser}
		builders[i] = b
		if err := b.overlay(ctx); err != nil {
			return nil, stats, err
		}
		stats.Overlay += b.stats.Overlay
		stats.Identifiers += b.stats.Identifiers
		stats.ChangedSymbols += b.stats.ChangedSymbols
	}
	if err := scanParts(ctx, builders, &stats); err != nil {
		return nil, stats, err
	}
	out := make([][]Chunk, len(parts))
	for i, b := range builders {
		out[i] = b.assemble()
		stats.Definitions += b.stats.Definitions
		stats.Callers += b.stats.Callers
	}
	stats.Elapsed = time.Since(start)
	return out, stats, nil
}

type builder struct {
	in     Input
	opts   Options
	parser *chunk.Parser
	stats  Stats

	overlayChunks []Chunk
	// overlayDecls keys "path\x00symbol" of declarations the diff changed,
	// so their own text is never offered as a caller or definition.
	overlayDecls map[string]bool
	// identifiers, most frequent first, and the changed symbols to find
	// callers for.
	identifiers    []string
	changedSymbols []string
	// hits are candidate chunks per word, in scan order.
	definitions map[string]*candidates
	callers     map[string]*candidates
}

func (b *builder) ignored(path string) bool { return chunk.Matches(b.in.Ignore, path) }

func (b *builder) read(tree *object.Tree, path string) []byte {
	if tree == nil {
		return nil
	}
	f, err := tree.File(path)
	if err != nil {
		return nil
	}
	return b.readFile(f)
}

// readFile reads f once, as bytes. A file over the size cap, or a binary
// file, which holds a NUL, is nothing to read.
func (b *builder) readFile(f *object.File) []byte {
	if f.Size > int64(b.opts.MaxFileBytes) {
		return nil
	}
	r, err := f.Reader()
	if err != nil {
		return nil
	}
	defer func() { _ = r.Close() }()
	src, err := io.ReadAll(r)
	if err != nil || bytes.IndexByte(src, 0) >= 0 {
		return nil
	}
	return src
}

// overlay is stage 1, and it also collects what stages 2 and 3 look for.
func (b *builder) overlay(ctx context.Context) error {
	d := parseDiff(b.in.Diff)
	b.overlayDecls = map[string]bool{}
	idCount := map[string]int{}
	seenSymbol := map[string]bool{}
	// A renamed file's base side is under its old name, as are the lines
	// the diff removed from it.
	basePath := map[string]string{}
	for old, renamed := range Renames(b.in.Diff) {
		basePath[renamed] = old
	}
	for _, path := range b.in.Changed {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if b.ignored(path) {
			continue
		}
		src := b.read(b.in.Head, path)
		if src == nil {
			continue
		}
		f := b.parser.Parse(path, src)
		added := d.added[path]
		for _, id := range f.Identifiers(added) {
			idCount[id]++
		}
		// The declarations are all the rest of the pass needs.
		f.Close()
		old := cmp.Or(basePath[path], path)
		if base := b.read(b.in.Base, old); base != nil {
			bf := b.parser.Parse(old, base)
			for _, id := range bf.Identifiers(d.removed[old]) {
				idCount[id]++
			}
			bf.Close()
		}
		covered := map[int]bool{}
		pending := runs(added, b.opts.WindowRadius)
		for len(pending) > 0 {
			run := pending[0]
			pending = pending[1:]
			if covered[run[0]] && covered[run[1]] {
				continue
			}
			c := Chunk{Stage: StageOverlay, Path: path, Language: f.Language}
			var text []byte
			decl := f.Enclosing([]int{run[0], run[1]}, b.opts.MaxDeclLines)
			if decl != nil {
				c.Symbol, c.Kind, c.Scope = decl.Symbol, decl.Kind, decl.Scope
				start, full := b.declText(f, decl, run[0])
				// A comment or body over MaxChunkBytes ahead of the run's last
				// line would leave the cut text without it, so a window shows
				// the run instead, still as the declaration's.
				ahead := len(full) - len(f.Bytes(run[0], decl.EndLine))
				if len(full) <= b.opts.MaxChunkBytes || ahead+len(f.Bytes(run[0], run[1])) <= b.opts.MaxChunkBytes {
					c.StartLine, text = start, full
				}
			}
			if len(text) == 0 {
				c.StartLine, text = b.window(f, run)
				if len(text) == 0 {
					continue
				}
			}
			// keptEnd and cutText keep at most MaxChunkBytes bytes of the
			// text, and need one more to tell that it is over, so only those
			// are copied: a long run is cut into many chunks, and each would
			// otherwise copy all of the run that is left.
			c.Text = string(text[:min(len(text), b.opts.MaxChunkBytes+1)])
			if copied != nil {
				*copied += len(c.Text)
			}
			// The chunk claims only the lines its cut keeps whole. The rest
			// of a run that a window at radius zero cannot hold is shown by
			// another, from its next added line. A window clamped to the
			// file's end is not cut, and has no rest to show.
			c.EndLine = b.keptEnd(c.StartLine, c.Text)
			if c.EndLine < run[1] && len(c.Text) > b.opts.MaxChunkBytes {
				i, _ := slices.BinarySearch(added, max(c.EndLine, run[0])+1)
				pending = slices.Insert(pending, 0, [2]int{added[i], run[1]})
			}
			for l := c.StartLine; l <= c.EndLine; l++ {
				covered[l] = true
			}
			if c.Symbol != "" {
				b.overlayDecls[path+"\x00"+c.Symbol] = true
				if !seenSymbol[c.Symbol] {
					seenSymbol[c.Symbol] = true
					b.changedSymbols = append(b.changedSymbols, c.Symbol)
				}
			}
			// A declaration the diff already shows whole adds nothing.
			if subset(c.StartLine, c.EndLine, d.shown[path]) {
				continue
			}
			c.Text = b.cutText(c.Text)
			b.overlayChunks = append(b.overlayChunks, c)
		}
	}
	for id := range idCount {
		if len(id) >= 3 && !seenSymbol[id] {
			b.identifiers = append(b.identifiers, id)
		}
	}
	slices.SortFunc(b.identifiers, func(x, y string) int { return cmp.Or(cmp.Compare(idCount[y], idCount[x]), cmp.Compare(x, y)) })
	b.identifiers = b.identifiers[:min(len(b.identifiers), b.opts.MaxIdentifiers)]
	b.stats.Overlay = len(b.overlayChunks)
	b.stats.Identifiers = len(b.identifiers)
	b.stats.ChangedSymbols = len(b.changedSymbols)
	return nil
}

// copied, when a test points it at a count, adds up the bytes overlay
// copies out of the files it reads. Copying the rest of a long run again
// for each chunk cut from it is quadratic in the run's size, and the count
// shows that on any machine. A test that sets it does not run in parallel.
var copied *int

func subset(start, end int, shown map[int]string) bool {
	for l := start; l <= end; l++ {
		if _, ok := shown[l]; !ok {
			return false
		}
	}
	return true
}

// wordStart reports whether c can start an identifier: [A-Za-z_].
func wordStart(c byte) bool { return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || c == '_' }

func (b *builder) addCandidate(f *chunk.File, decl *chunk.Decl, line int, word string, changed bool) {
	var stage string
	var target map[string]*candidates
	var limit int
	switch {
	case decl.Symbol == word && !changed:
		stage, target, limit = StageDefinition, b.definitions, b.opts.DefinitionsPerIdentifier
	case changed && decl.Symbol != word:
		stage, target, limit = StageCaller, b.callers, b.opts.CallersPerSymbol
	default:
		return
	}
	start, text := b.declText(f, decl, line)
	c := Chunk{
		Stage: stage, Path: f.Path, Language: f.Language, Symbol: decl.Symbol, Kind: decl.Kind, Scope: decl.Scope,
		StartLine: start, EndLine: decl.EndLine, Ref: word,
	}
	set := target[word]
	if set == nil {
		set = &candidates{}
		target[word] = set
	}
	set.count++
	if !keepCandidate(set.chunks, c, limit) {
		return
	}
	// cutText keeps at most MaxChunkBytes bytes of the text, and needs one more
	// to tell that it is over, so only those are copied.
	c.Text = b.cutText(string(text[:min(len(text), b.opts.MaxChunkBytes+1)]))
	set.chunks = append(set.chunks, c)
}

// declText is decl's text, a slice of f's source, and the line it starts on.
// When the text is over MaxChunkBytes, the comments and decorators above
// decl's head line go first, from the top and each whole, so the cut keeps
// more of its body and does not start inside a block comment or a
// decorator's arguments. One that closes on the head line goes up to its
// close, so the text then starts mid-line. Line line, which pulled decl in,
// stays, and so does the rest of a comment that holds it.
func (b *builder) declText(f *chunk.File, decl *chunk.Decl, line int) (start int, text []byte) {
	start, text = decl.StartLine, f.Bytes(decl.StartLine, decl.EndLine)
	for len(text) > b.opts.MaxChunkBytes {
		next := start + 1
		for next <= decl.EndLine && f.LeadContinues(next) {
			next++
		}
		if next > min(decl.HeadLine, line) {
			break
		}
		for ; start < next; start++ {
			_, text, _ = bytes.Cut(text, []byte{'\n'})
		}
	}
	// A comment that closes on the head line cannot go whole by lines. When
	// line is not in it, the text starts past its close, mid-line, rather
	// than with the comment alone.
	if len(text) > b.opts.MaxChunkBytes && start < decl.HeadLine && decl.HeadLine <= line {
		if past, ok := f.LeadEnds(decl.HeadLine); ok {
			start, text = decl.HeadLine, f.Bytes(decl.HeadLine, decl.EndLine)[past:]
		}
	}
	return start, text
}

// window is the fixed-window fallback for run: WindowRadius lines on each
// side, fewer when that would put it over MaxChunkBytes, so the cut keeps
// the whole run when the run alone fits. It returns the line the text
// starts on. The text is a slice of f's source.
func (b *builder) window(f *chunk.File, run [2]int) (start int, text []byte) {
	radius := b.opts.WindowRadius
	for radius > 0 && len(f.Bytes(run[0]-radius, run[1]+radius)) > b.opts.MaxChunkBytes {
		radius--
	}
	start = max(run[0]-radius, 1)
	return start, f.Bytes(start, run[1]+radius)
}

// keptEnd is the last line that text, starting on line start, keeps whole
// once cutText cuts it, or start when the cut falls inside that line.
func (b *builder) keptEnd(start int, text string) int {
	if len(text) <= b.opts.MaxChunkBytes {
		return start + strings.Count(text, "\n")
	}
	// A line is whole when the cut is at or past its newline.
	kept := len(textcut.Prefix(text, b.opts.MaxChunkBytes))
	return max(start, start+strings.Count(text[:kept+1], "\n")-1)
}

func (b *builder) cutText(text string) string {
	if len(text) > b.opts.MaxChunkBytes {
		return textcut.Prefix(text, b.opts.MaxChunkBytes) + "\n… (truncated)"
	}
	return text
}

type candidates struct {
	chunks []Chunk
	count  int
}

func (c *candidates) pick(n int) []Chunk {
	if c == nil || n <= 0 {
		return nil
	}
	if c.count > n && len(c.chunks) <= n {
		return pickDiverse(c.chunks, n)
	}
	return pick(c.chunks, n)
}

// keepCandidate keeps the first n matches, the first n files, and at most
// n-1 distinct extra ranges. The initial matches preserve encounter order
// when under the cap, even for declarations sharing a line.
func keepCandidate(cs []Chunk, c Chunk, n int) bool {
	if n <= 0 {
		return false
	}
	if len(cs) < n {
		return true
	}
	distinct, same := 0, false
	extras := 0
	for i, x := range cs {
		same = same || x.Path == c.Path
		first := !slices.ContainsFunc(cs[:i], func(y Chunk) bool { return y.Path == x.Path })
		if first {
			distinct++
		} else if !slices.ContainsFunc(cs[:i], func(y Chunk) bool { return y.Path == x.Path && y.StartLine == x.StartLine }) {
			extras++
		}
	}
	if !same {
		return distinct < n
	}
	if slices.ContainsFunc(cs, func(x Chunk) bool { return x.Path == c.Path && x.StartLine == c.StartLine }) {
		return false
	}
	return extras < n-1
}

// assemble orders and caps the stages.
func (b *builder) assemble() []Chunk {
	var out []Chunk
	total := 0
	add := func(c Chunk) bool {
		if total+len(c.Text) > b.opts.MaxPackBytes {
			return false
		}
		total += len(c.Text)
		out = append(out, c)
		return true
	}
	for _, c := range b.overlayChunks {
		if !add(c) {
			return out
		}
	}
	n := 0
	for _, id := range b.identifiers {
		for _, c := range b.definitions[id].pick(b.opts.DefinitionsPerIdentifier) {
			if n >= b.opts.MaxDefinitions {
				break
			}
			if !add(c) {
				return out
			}
			n++
		}
	}
	b.stats.Definitions = n
	n = 0
	for _, s := range b.changedSymbols {
		for _, c := range b.callers[s].pick(b.opts.CallersPerSymbol) {
			if n >= b.opts.MaxCallers {
				break
			}
			if !add(c) {
				return out
			}
			n++
		}
	}
	b.stats.Callers = n
	return out
}

// pick keeps up to n chunks. It prefers one per file before a second from the
// same file.
func pick(cs []Chunk, n int) []Chunk {
	if n <= 0 {
		return nil
	}
	if len(cs) <= n {
		return cs
	}
	return pickDiverse(cs, n)
}

func pickDiverse(cs []Chunk, n int) []Chunk {
	var out []Chunk
	seen := map[string]bool{}
	for _, c := range cs {
		if !seen[c.Path] {
			seen[c.Path] = true
			out = append(out, c)
			if len(out) == n {
				return out
			}
		}
	}
	for _, c := range cs {
		if len(out) == n {
			break
		}
		if !slices.ContainsFunc(out, func(x Chunk) bool { return x.Path == c.Path && x.StartLine == c.StartLine }) {
			out = append(out, c)
		}
	}
	return out
}
