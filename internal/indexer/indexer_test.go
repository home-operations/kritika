package indexer

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/go-git/go-billy/v6/memfs"
	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/go-git/go-git/v6/storage/memory"

	"github.com/home-operations/kritika/internal/chunk"
	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/gittest"
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
