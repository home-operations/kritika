package indexer

import (
	"errors"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/go-git/go-billy/v6/memfs"
	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/go-git/go-git/v6/plumbing/storer"
	"github.com/go-git/go-git/v6/storage/memory"
	"golang.org/x/sync/errgroup"

	"github.com/home-operations/kritika/internal/chunk"
	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/contextpack"
	"github.com/home-operations/kritika/internal/gittest"
	"github.com/home-operations/kritika/internal/textcut"
)

func TestBuildIncrementalRejectsTruncation(t *testing.T) {
	head, base := trees(t)
	tests := []struct {
		name                 string
		files, bytes, chunks int
	}{
		{name: "file limit", files: 0, bytes: DefaultOptions.MaxBytes, chunks: DefaultOptions.MaxChunks},
		{name: "byte limit", files: DefaultOptions.MaxFiles, bytes: 1, chunks: DefaultOptions.MaxChunks},
		{name: "chunk limit inside a file", files: DefaultOptions.MaxFiles, bytes: DefaultOptions.MaxBytes, chunks: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := DefaultOptions
			opts.MaxFiles, opts.MaxBytes, opts.MaxChunks = tt.files, tt.bytes, tt.chunks
			chunks, changed, stats, err := Build(t.Context(), head, base, nil, opts)
			if !errors.Is(err, ErrBudgetReached) || !stats.Truncated || chunks != nil || changed != nil {
				t.Fatalf("Build = %v, %v, %+v, %v", chunks, changed, stats, err)
			}
			chunks, _, stats, err = Build(t.Context(), head, nil, nil, opts)
			if err != nil || !stats.Truncated || len(chunks) > opts.MaxChunks || stats.Bytes > opts.MaxBytes {
				t.Fatalf("full Build = %d, %+v, %v", len(chunks), stats, err)
			}
		})
	}
}

const goFile = `package demo

type Widget struct{ Name string }

func Build(name string) *Widget { return &Widget{Name: name} }

func Use() { _ = Build("x") }
`

func trees(t *testing.T) (head, base *object.Tree) {
	t.Helper()
	fs := memfs.New()
	r, _ := git.Init(memory.NewStorage(), git.WithWorkTree(fs))
	gittest.Unsigned(t, r)
	wt, _ := r.Worktree()
	write := func(name, content string) {
		f, _ := fs.Create(name)
		_, _ = f.Write([]byte(content))
		_ = f.Close()
		_, _ = wt.Add(name)
	}
	sig := &object.Signature{Name: "t", Email: "t@t", When: time.Now()}
	write("demo.go", goFile)
	write("values.yaml", strings.Repeat("key: value\n", 130))
	write("vendor/x.go", "package x\n\nfunc V() {}\n")
	write("gone.go", "package demo\n\nfunc Gone() {}\n")
	baseHash, _ := wt.Commit("base", &git.CommitOptions{Author: sig})
	write("demo.go", goFile+"\nfunc More() {}\n")
	_, _ = wt.Remove("gone.go")
	headHash, _ := wt.Commit("head", &git.CommitOptions{Author: sig})
	bc, _ := r.CommitObject(baseHash)
	hc, _ := r.CommitObject(headHash)
	base, _ = bc.Tree()
	head, _ = hc.Tree()
	return head, base
}

func TestBuildFull(t *testing.T) {
	head, _ := trees(t)
	chunks, changed, stats, err := Build(t.Context(), head, nil, []string{"vendor/**"}, Options{})
	if err != nil || changed != nil {
		t.Fatalf("err=%v changed=%v", err, changed)
	}
	var symbols []string
	windows := 0
	for _, c := range chunks {
		if c.Path == "vendor/x.go" {
			t.Fatalf("ignored path chunked: %+v", c)
		}
		if c.Kind == "window" {
			windows++
			if c.Path != "values.yaml" || c.Language != "yaml" {
				t.Fatalf("window from %+v", c)
			}
		}
		if c.Symbol != "" {
			symbols = append(symbols, c.Symbol)
		}
	}
	if windows != 3 {
		t.Fatalf("windows = %d, want 3 for 130 lines at 60/10", windows)
	}
	for _, want := range []string{"Widget", "Build", "Use", "More"} {
		if !contains(symbols, want) {
			t.Fatalf("symbols %v missing %s", symbols, want)
		}
	}
	if stats.Files != 2 || stats.Parsed != 1 || stats.Skipped < 1 {
		t.Fatalf("stats = %+v", stats)
	}
}

func TestBuildIncremental(t *testing.T) {
	head, base := trees(t)
	chunks, changed, _, err := Build(t.Context(), head, base, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 2 || !contains(changed, "demo.go") || !contains(changed, "gone.go") {
		t.Fatalf("changed = %v", changed)
	}
	for _, c := range chunks {
		if c.Path != "demo.go" {
			t.Fatalf("unchanged path chunked: %+v", c)
		}
	}
	if len(chunks) < 4 {
		t.Fatalf("chunks = %+v", chunks)
	}
}

func TestBuildStopsAtBudget(t *testing.T) {
	head, _ := trees(t)
	opts := DefaultOptions
	opts.MaxFiles = 1
	_, _, stats, err := Build(t.Context(), head, nil, nil, opts)
	if err != nil || !stats.Truncated || stats.Files != 1 {
		t.Fatalf("err=%v stats=%+v", err, stats)
	}
}

// TestBuildKeepsTextValidUTF8: every chunk is valid UTF-8, as the staging
// table's text column requires: a file that is not UTF-8 is skipped, and a
// chunk cut to its limit ends on a rune boundary.
func TestBuildKeepsTextValidUTF8(t *testing.T) {
	fs := memfs.New()
	r, _ := git.Init(memory.NewStorage(), git.WithWorkTree(fs))
	gittest.Unsigned(t, r)
	wt, _ := r.Worktree()
	for name, content := range map[string]string{
		"latin1.yaml": "key: caf\xe9\n",
		"wide.yaml":   "key: " + strings.Repeat("é", 50) + "\n",
	} {
		f, _ := fs.Create(name)
		_, _ = f.Write([]byte(content))
		_ = f.Close()
		_, _ = wt.Add(name)
	}
	hash, err := wt.Commit("c", &git.CommitOptions{Author: &object.Signature{Name: "t", Email: "t@t", When: time.Now()}})
	if err != nil {
		t.Fatal(err)
	}
	c, _ := r.CommitObject(hash)
	head, _ := c.Tree()
	opts := DefaultOptions
	opts.MaxChunkBytes = 8
	chunks, _, stats, err := Build(t.Context(), head, nil, nil, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) == 0 || stats.Skipped != 1 {
		t.Fatalf("chunks = %+v, stats = %+v; want wide.yaml chunked and latin1.yaml skipped", chunks, stats)
	}
	for _, c := range chunks {
		if c.Path != "wide.yaml" || !utf8.ValidString(c.Text) {
			t.Fatalf("chunk %q of %s is not valid UTF-8 or should have been skipped", c.Text, c.Path)
		}
	}
}

func contains(ss []string, s string) bool {
	return slices.Contains(ss, s)
}

// TestBuildSkipsUnindexedFiles: files that SOPS encrypted or a code generator
// wrote are left out of the index without an ignore rule. The review's
// default ignores leave them in, so a pull request that changes one is still
// reviewed, and Unindexed says the same as Build.
func TestBuildSkipsUnindexedFiles(t *testing.T) {
	tests := []struct {
		path    string
		indexed bool
	}{
		{"kubernetes/apps/foo/secret.sops.yaml", false},
		{"talos/talsecret.sops.yml", false},
		{"secret.sops.json", false},
		{"api/v1/service_pb2.py", false},
		{"api/v1/service_pb2.pyi", false},
		{"api/v1/service_pb2_grpc.py", false},
		{"cpp/service.pb.cc", false},
		{"cpp/service.pb.h", false},
		{"lib/model.g.dart", false},
		{"lib/model.freezed.dart", false},
		{".sops.yaml", true},
		{"lib/model.dart", true},
		{"api/v1/service.py", true},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			fs := memfs.New()
			r, _ := git.Init(memory.NewStorage(), git.WithWorkTree(fs))
			gittest.Unsigned(t, r)
			wt, _ := r.Worktree()
			f, _ := fs.Create(tt.path)
			_, _ = f.Write([]byte("value = 1\n"))
			_ = f.Close()
			_, _ = wt.Add(tt.path)
			hash, err := wt.Commit("c", &git.CommitOptions{Author: &object.Signature{Name: "t", Email: "t@t", When: time.Now()}})
			if err != nil {
				t.Fatal(err)
			}
			c, _ := r.CommitObject(hash)
			head, _ := c.Tree()
			chunks, _, _, err := Build(t.Context(), head, nil, nil, Options{})
			if err != nil {
				t.Fatal(err)
			}
			if indexed := len(chunks) > 0; indexed != tt.indexed {
				t.Fatalf("indexed = %v, want %v: %+v", indexed, tt.indexed, chunks)
			}
			if Unindexed(tt.path) == tt.indexed {
				t.Errorf("Unindexed(%q) = %v, want %v", tt.path, !tt.indexed, tt.indexed)
			}
			if chunk.Matches(configfile.DefaultIgnore, tt.path) {
				t.Errorf("the review's default ignores leave out %s", tt.path)
			}
		})
	}
}

// TestBuildSignatures: a window of a declaration that does not show the line
// naming the declaration carries that line, cut to maxSignatureBytes, and its
// text stays the source's lines. A piece that shows the line, a declaration
// stored whole and a window of a file with no declarations carry none.
func TestBuildSignatures(t *testing.T) {
	type sig struct {
		head      int
		signature string
	}
	tests := []struct {
		name, path, src string
		// signatures gives, by symbol, the line that names the
		// declaration and the signature of its windows that do not show it.
		signatures map[string]sig
	}{
		{
			name: "go function with a long body", path: "long.go",
			src: "package demo\n\n// Long does things.\n// It is long.\nfunc Long(a int) int {\n\tt := a\n" + lines(200, "\tt += %d") +
				"\treturn t\n}\n\nfunc Small() {}\n",
			signatures: map[string]sig{"Long": {5, "func Long(a int) int {"}},
		},
		{
			name: "go function of long lines within the line bound", path: "wide.go",
			src:        "package demo\n\nfunc Wide() {\n" + lines(120, "\t_ = %03d // "+strings.Repeat("x", 90)) + "}\n",
			signatures: map[string]sig{"Wide": {3, "func Wide() {"}},
		},
		{
			name: "python class with a long method", path: "foo.py",
			src: "class Foo:\n    \"\"\"Doc.\"\"\"\n\n    @staticmethod\n    def big(x):\n        total = 0\n" + lines(200, "        total += %d") +
				"        return total\n\n    def small(self):\n        return 1\n",
			signatures: map[string]sig{"big": {5, "def big(x):"}},
		},
		{
			name: "typescript class with fields after its methods", path: "store.ts",
			src: "export class Store {\n  get(): number { return 1; }\n  big(): number {\n    let t = 0;\n" + lines(160, "    t += %d;") +
				"    return t;\n  }\n" + lines(70, "  private a%d = 0;") + "}\n",
			signatures: map[string]sig{"Store": {1, "export class Store {"}, "big": {3, "big(): number {"}},
		},
		{
			name: "javascript function with a long signature", path: "render.js",
			src: "/** Renders. */\nfunction render(items, sep = \"-" + strings.Repeat("é", 150) + "\") {\n  const out = [];\n" +
				lines(200, "  out.push(%d);") + "  return out.join(sep);\n}\n",
			signatures: map[string]sig{"render": {2, "function render(items, sep = \"-" + strings.Repeat("é", 84)}},
		},
		{
			name: "yaml", path: "values.yaml", src: strings.Repeat("key: value\n", 130),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chunks, _, _, err := Build(t.Context(), oneFileTree(t, tt.path, tt.src), nil, nil, Options{})
			if err != nil {
				t.Fatal(err)
			}
			src := strings.Split(tt.src, "\n")
			carried := map[string]int{}
			for _, c := range chunks {
				want := ""
				if s, ok := tt.signatures[c.Symbol]; ok && (s.head < c.StartLine || s.head > c.EndLine) {
					want = s.signature
					carried[c.Symbol]++
				}
				if c.Signature != want {
					t.Errorf("chunk %s L%d-%d signature = %q, want %q", c.Symbol, c.StartLine, c.EndLine, c.Signature, want)
				}
				if len(c.Signature) > maxSignatureBytes || !utf8.ValidString(c.Signature) {
					t.Errorf("chunk %s L%d-%d signature %q is over %d bytes or not UTF-8", c.Symbol, c.StartLine, c.EndLine, c.Signature, maxSignatureBytes)
				}
				if first, _, _ := strings.Cut(c.Text, "\n"); first != src[c.StartLine-1] {
					t.Errorf("chunk %s L%d-%d starts %q, want the source's line %q", c.Symbol, c.StartLine, c.EndLine, first, src[c.StartLine-1])
				}
			}
			for symbol := range tt.signatures {
				if carried[symbol] == 0 {
					t.Errorf("no window of %s carries its signature: %+v", symbol, chunks)
				}
			}
		})
	}
}

// TestBuildSignaturesCopyTheirCut: the windows of a declaration whose
// naming line is long each keep only the signature cut from it, not the
// whole line, which every window's signature would otherwise hold until the
// index is uploaded. What the chunks hold is counted in bytes of live heap,
// which does not depend on how fast the machine is.
func TestBuildSignaturesCopyTheirCut(t *testing.T) {
	head := "func Long(a int) int { // " + strings.Repeat("x", 256<<10)
	src := "package demo\n\n" + head + "\n\tt := a\n" + lines(3000, "\tt += %d") + "\treturn t\n}\n"
	f, err := oneFileTree(t, "long.go", src).File("long.go")
	if err != nil {
		t.Fatal(err)
	}
	newBuilder := func() *builder {
		return &builder{opts: DefaultOptions, parser: &chunk.Parser{Limits: chunk.Limits{MaxBytes: DefaultOptions.MaxFileBytes}}}
	}
	// A first build loads the grammar, which stays loaded.
	if err := newBuilder().file(f); err != nil {
		t.Fatal(err)
	}
	b := newBuilder()
	var before, after runtime.MemStats
	runtime.GC()
	runtime.GC()
	runtime.ReadMemStats(&before)
	if err := b.file(f); err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	runtime.GC()
	runtime.ReadMemStats(&after)
	carried := 0
	for _, c := range b.chunks {
		if c.Signature != "" {
			carried++
			if want := textcut.Prefix(head, maxSignatureBytes); c.Signature != want {
				t.Fatalf("chunk L%d-%d signature = %q, want %q", c.StartLine, c.EndLine, c.Signature, want)
			}
		}
	}
	if carried < 20 {
		t.Fatalf("%d windows carry the signature, want the declaration cut into at least 20", carried)
	}
	if held := int64(after.HeapAlloc) - int64(before.HeapAlloc); held > int64(carried/4*len(head)) {
		t.Errorf("the %d chunks hold %d bytes, over %d copies of the %d-byte naming line", len(b.chunks), held, carried/4, len(head))
	}
	runtime.KeepAlive(b.chunks)
}

// TestBuildChunksCopyTheirCut: a line over MaxChunkBytes is stored cut,
// and its chunk holds only the cut, not the whole line, until the index is
// uploaded. That holds for a line indented past the bound too, which is
// stored from its first word. What the chunks hold is counted in bytes of
// live heap.
func TestBuildChunksCopyTheirCut(t *testing.T) {
	const n, size = 8, 100 << 10
	var src strings.Builder
	for i := range n {
		indent := ""
		if i%2 == 1 {
			indent = strings.Repeat(" ", DefaultOptions.MaxChunkBytes+10)
		}
		fmt.Fprintf(&src, "%sline%d %s\n", indent, i, strings.Repeat("word ", size/5))
	}
	f, err := oneFileTree(t, "long.md", src.String()).File("long.md")
	if err != nil {
		t.Fatal(err)
	}
	newBuilder := func() *builder {
		return &builder{opts: DefaultOptions, parser: &chunk.Parser{Limits: chunk.Limits{MaxBytes: DefaultOptions.MaxFileBytes}}}
	}
	if err := newBuilder().file(f); err != nil {
		t.Fatal(err)
	}
	b := newBuilder()
	var before, after runtime.MemStats
	runtime.GC()
	runtime.GC()
	runtime.ReadMemStats(&before)
	if err := b.file(f); err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	runtime.GC()
	runtime.ReadMemStats(&after)
	if len(b.chunks) != n {
		t.Fatalf("%d chunks, want one per line, %d", len(b.chunks), n)
	}
	for i, c := range b.chunks {
		if !strings.HasPrefix(c.Text, fmt.Sprintf("line%d word", i)) || len(c.Text) > DefaultOptions.MaxChunkBytes {
			t.Fatalf("chunk %d text = %q, %d bytes, want line%d from its first word within %d bytes", i, textcut.Prefix(c.Text, 40), len(c.Text), i, DefaultOptions.MaxChunkBytes)
		}
	}
	if held := int64(after.HeapAlloc) - int64(before.HeapAlloc); held > int64(n*size/4) {
		t.Errorf("the %d chunks hold %d bytes, over a quarter of their %d-byte lines", n, held, n*size)
	}
	// The file's blob, in the repository's memory, was live before the
	// build and must still count as live after it.
	runtime.KeepAlive(f)
	runtime.KeepAlive(b.chunks)
}

// oneFileTree is the tree of a commit that holds one file.
func oneFileTree(t *testing.T, path, content string) *object.Tree {
	t.Helper()
	fs := memfs.New()
	r, _ := git.Init(memory.NewStorage(), git.WithWorkTree(fs))
	gittest.Unsigned(t, r)
	wt, _ := r.Worktree()
	f, _ := fs.Create(path)
	_, _ = f.Write([]byte(content))
	_ = f.Close()
	_, _ = wt.Add(path)
	hash, err := wt.Commit("c", &git.CommitOptions{Author: &object.Signature{Name: "t", Email: "t@t", When: time.Now()}})
	if err != nil {
		t.Fatal(err)
	}
	c, _ := r.CommitObject(hash)
	tree, _ := c.Tree()
	return tree
}

// TestBuildSkipsWordlessWindows: a window of white space alone, as a line
// too long to share a window with its neighbours makes, is not stored, and
// does not count against MaxChunks.
func TestBuildSkipsWordlessWindows(t *testing.T) {
	fs := memfs.New()
	r, _ := git.Init(memory.NewStorage(), git.WithWorkTree(fs))
	gittest.Unsigned(t, r)
	wt, _ := r.Worktree()
	f, _ := fs.Create("gap.md")
	_, _ = f.Write([]byte("intro words\n" + strings.Repeat(" ", DefaultOptions.MaxChunkBytes) + "\nclosing words\n"))
	_ = f.Close()
	_, _ = wt.Add("gap.md")
	hash, err := wt.Commit("c", &git.CommitOptions{Author: &object.Signature{Name: "t", Email: "t@t", When: time.Now()}})
	if err != nil {
		t.Fatal(err)
	}
	c, _ := r.CommitObject(hash)
	head, _ := c.Tree()
	opts := DefaultOptions
	opts.MaxChunks = 2
	chunks, _, stats, err := Build(t.Context(), head, nil, nil, opts)
	if err != nil {
		t.Fatal(err)
	}
	texts := make([]string, 0, len(chunks))
	for _, c := range chunks {
		texts = append(texts, c.Text)
	}
	if !slices.Equal(texts, []string{"intro words", "closing words"}) || stats.Truncated {
		t.Fatalf("chunks %q, truncated %v; want the two worded lines alone, untruncated", texts, stats.Truncated)
	}
}

// TestBuildKeepsALineIndentedPastTheBound: a line with more white space
// before its first word than a chunk holds is stored from that word, so
// the name on it is found, as a query of the same line asks about it.
func TestBuildKeepsALineIndentedPastTheBound(t *testing.T) {
	line := strings.Repeat(" ", DefaultOptions.MaxChunkBytes+10) + "callSomething()"
	head := oneFileTree(t, "deep.go", "package lib\n\nfunc F() {\n"+line+"\n}\n")
	chunks, _, _, err := Build(t.Context(), head, nil, nil, DefaultOptions)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(chunks, func(c Chunk) bool {
		return c.StartLine == 4 && c.EndLine == 4 && c.Symbol == "F" && c.Text == "callSomething()"
	}) {
		t.Fatalf("chunks %+v hold no chunk of line 4 from its first word", chunks)
	}
}

// TestChunkText: a piece's text is cut to the bound, from its start unless
// that leaves no word and the piece has one past the bound. The indexer's
// cut of the source's bytes is the same as the runner's of a string.
func TestChunkText(t *testing.T) {
	for _, tt := range []struct{ name, text, want string }{
		{"within the bound", " a b", " a b"},
		{"at the bound", "abcd", "abcd"},
		{"cut from the start", "abcdefgh", "abcd"},
		{"cut before a rune across the bound", "abcéf", "abc"},
		{"a word within the bound", "  ab" + strings.Repeat(" ", 10) + "cd", "  ab"},
		{"indented past the bound", strings.Repeat(" ", 10) + "(name)", "name"},
		{"indented past the bound to a rune", strings.Repeat(" ", 10) + "éabcd", "éab"},
		{"wordless", strings.Repeat(" ", 10) + "()", "    "},
		{"empty", "", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := ChunkText(tt.text, 4); got != tt.want {
				t.Errorf("ChunkText(%q, 4) = %q, want %q", tt.text, got, tt.want)
			}
			if got := chunkText([]byte(tt.text), 4); got != tt.want {
				t.Errorf("chunkText(%q, 4) = %q, want %q", tt.text, got, tt.want)
			}
		})
	}
}

// sizeReads is a storer that records the objects whose content or size is
// read.
type sizeReads struct {
	storer.EncodedObjectStorer
	read map[plumbing.Hash]bool
}

func (s *sizeReads) EncodedObject(t plumbing.ObjectType, h plumbing.Hash) (plumbing.EncodedObject, error) {
	s.read[h] = true
	return s.EncodedObjectStorer.EncodedObject(t, h)
}

func (s *sizeReads) EncodedObjectSize(h plumbing.Hash) (int64, error) {
	s.read[h] = true
	return s.EncodedObjectStorer.EncodedObjectSize(h)
}

// TestIndexableReadsOnlyTheFilesThatTakeATurn: a file that HunkQueries
// leaves out before the ranking, being ignored, Unindexed, of no language
// or adding nothing, has its size read by nobody, and nor does one ranked
// below the files that fill the request.
func TestIndexableReadsOnlyTheFilesThatTakeATurn(t *testing.T) {
	fs := memfs.New()
	r, _ := git.Init(memory.NewStorage(), git.WithWorkTree(fs))
	gittest.Unsigned(t, r)
	wt, _ := r.Worktree()
	var diff strings.Builder
	var paths, used []string
	write := func(path string) {
		f, _ := fs.Create(path)
		_, _ = f.Write([]byte("// " + path + "\n"))
		_ = f.Close()
		_, _ = wt.Add(path)
		paths = append(paths, path)
	}
	for _, p := range []string{"vendor/lib.go", "api_pb2.py", "notes.unknownext", "removed.go"} {
		write(p)
	}
	diff.WriteString(fileDiff("", "vendor/lib.go", added("func lib() {}")))
	diff.WriteString(fileDiff("", "api_pb2.py", added("def api(): pass")))
	diff.WriteString(fileDiff("", "notes.unknownext", added("notes")))
	diff.WriteString(fileDiff("removed.go", "removed.go", "@@ -1,1 +0,0 @@\n-func gone() {}\n"))
	for i := range contextpack.SimilarQueries + 2 {
		p := fmt.Sprintf("f%02d.go", i)
		write(p)
		lines := make([]string, 0, contextpack.SimilarQueries+2-i)
		for j := range cap(lines) {
			lines = append(lines, fmt.Sprintf("var v%d_%d = %d", i, j, j))
		}
		diff.WriteString(fileDiff("", p, added(lines...)))
		if i < contextpack.SimilarQueries {
			used = append(used, p)
		}
	}
	hash, err := wt.Commit("c", &git.CommitOptions{Author: &object.Signature{Name: "t", Email: "t@t", When: time.Unix(1, 0)}})
	if err != nil {
		t.Fatal(err)
	}
	c, _ := r.CommitObject(hash)
	plain, _ := c.Tree()
	reads := &sizeReads{EncodedObjectStorer: r.Storer, read: map[plumbing.Hash]bool{}}
	head, err := object.GetTree(reads, plain.Hash)
	if err != nil {
		t.Fatal(err)
	}
	_, got := HunkQueries(diff.String(), []string{"vendor/**"}, Indexable(head, paths))
	if !slices.Equal(got, used) {
		t.Fatalf("HunkQueries asked about %q, want %q", got, used)
	}
	for _, p := range paths {
		e, err := plain.FindEntry(p)
		if err != nil {
			t.Fatal(err)
		}
		if read := reads.read[e.Hash]; read != slices.Contains(used, p) {
			t.Errorf("%s: read %t, want %t", p, read, !read)
		}
	}
}

// TestIndexableIsSharedSafely: split reviews share one predicate between
// HunkQueries calls that run at once, and each gets the same answer.
func TestIndexableIsSharedSafely(t *testing.T) {
	head := oneFileTree(t, "big.go", strings.Repeat("x", DefaultOptions.MaxFileBytes+1))
	indexable := Indexable(head, []string{"big.go", "gone.go"})
	var g errgroup.Group
	for range 8 {
		g.Go(func() error {
			if indexable("big.go") || !indexable("gone.go") {
				return errors.New("Indexable refused gone.go or took big.go")
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		t.Fatal(err)
	}
}
