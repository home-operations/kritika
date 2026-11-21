package indexer

import (
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-git/go-billy/v6/memfs"
	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/go-git/go-git/v6/storage/memory"

	"github.com/home-operations/kritika/internal/gittest"
)

// corpus is a tree of the shapes the chunker used to drop code from: an
// oversized declaration with nested ones (a class of fields, a long function
// with one closure, an oversized method before a small one, an oversized
// nested declaration), exported, decorated and templated declarations, doc
// comments and attributes, long lines, a line over the byte cap, unnamed
// top-level statements, and a parse that error recovery leaves short, along
// with the shapes that gave a window of repeated lines or of no word at all.
// symbols names, by a line's text,
// the symbol of the smallest chunk that stores it.
var corpus = []struct {
	path, src string
	symbols   map[string]string
	// runs are line ranges that one chunk stores together.
	runs [][2]int
}{
	{
		path: "run.go",
		src: "// Package demo does things.\npackage demo\n\nimport (\n\t\"fmt\"\n\t\"os\"\n)\n\n// Run is long.\nfunc Run() int {\n" +
			"\tt := 0\n" + lines(170, "\tt += %d") + "\tfmt.Println(os.Args, t)\n\treturn t\n}\n",
		symbols: map[string]string{"// Run is long.": "Run", "\tt += 90": "Run", "\treturn t": "Run"},
		runs:    [][2]int{{1, 7}},
	},
	{
		path: "closure.js",
		src: "/** Runs the steps. */\nfunction run(items) {\n  let t = 0;\n  function step(n) {\n    return n + 1;\n  }\n" +
			lines(170, "  t += step(items[%d]);") + "  return t;\n}\n",
		symbols: map[string]string{
			"/** Runs the steps. */": "run", "  let t = 0;": "run", "    return n + 1;": "step", "  t += step(items[90]);": "run", "  return t;": "run",
		},
	},
	{
		path: "Fields.java",
		src: "package demo;\n\n/** Fields holds many things. */\npublic class Fields {\n" + lines(40, "    private int f%d = 0;") +
			"\n    /** Big adds. */\n    public int big() {\n        int t = 0;\n" + lines(170, "        t += f%d;") +
			"        return t;\n    }\n\n    /** Small. */\n    @Override\n    public String toString() { return \"fields\"; }\n}\n",
		symbols: map[string]string{
			"public class Fields {": "Fields", "    private int f30 = 0;": "Fields", "    /** Big adds. */": "big", "        t += f150;": "big",
			"    /** Small. */": "toString", "    @Override": "toString", "    public String toString() { return \"fields\"; }": "toString",
		},
	},
	{
		path: "store.ts",
		src: "import { a } from \"a\";\n\n/** Adds. */\nexport function add(x: number): number {\n  return x + a;\n}\n\nexport class Store {\n" +
			lines(25, "  private s%d = 0;") + "  big(): number {\n    let t = 0;\n" + lines(160, "    t += %d;") + "    return t;\n  }\n" +
			"  // last doc\n  last(): number { return 2; }\n}\n",
		symbols: map[string]string{
			"/** Adds. */": "add", "export function add(x: number): number {": "add", "  private s10 = 0;": "Store", "    t += 120;": "big",
			"  // last doc": "last", "  last(): number { return 2; }": "last",
		},
	},
	{
		path: "app.py",
		src: "\"\"\"App module.\"\"\"\nimport os\n\n\n# Routes things.\n@route(\"/x\")\ndef handler(req):\n    return req\n\n\nclass Service:\n" +
			"    \"\"\"Service doc.\"\"\"\n\n    def big(self, x):\n        total = 0\n" + lines(170, "        total += x * %d") +
			"        return total\n\n    @property\n    def name(self):\n        return \"svc\"\n\n\nif os.name == \"nt\":\n" +
			"    def sep():\n        return \"\\\\\"\nelse:\n    def sep():\n        return \"/\"\n",
		symbols: map[string]string{
			"# Routes things.": "handler", "@route(\"/x\")": "handler", "        total += x * 100": "big", "    @property": "name",
			"        return \"svc\"": "name", "        return \"/\"": "sep",
		},
		runs: [][2]int{{1, 2}},
	},
	{
		path: "tmpl.cpp",
		src: "#include <vector>\n#include <string>\n\ntemplate <typename T>\nT max_of(T a, T b) {\n  return a > b ? a : b;\n}\n\n" +
			"namespace util {\nint helper(int x) { return x + 1; }\n\nint big(int x) {\n  int t = x;\n" + lines(160, "  t += helper(%d);") +
			"  return t;\n}\n}\n",
		symbols: map[string]string{
			"template <typename T>": "max_of", "int helper(int x) { return x + 1; }": "helper", "  t += helper(80);": "big",
		},
	},
	{
		path: "point.rs",
		src: "/// A point in space\n#[derive(Debug, Clone)] // Copy would be wrong here\npub struct Point { x: i32 }\n\nimpl Point {\n" +
			"    pub fn big() -> i32 {\n        let mut t = 0;\n" + lines(160, "        t += %d;") + "        t\n    }\n    /// Small.\n" +
			"    pub fn small() -> i32 { 1 }\n}\n",
		symbols: map[string]string{
			"/// A point in space": "Point", "#[derive(Debug, Clone)] // Copy would be wrong here": "Point", "        t += 159;": "big",
			"    /// Small.": "small",
		},
	},
	{
		path: "wide.go",
		src:  "package demo\n\nfunc Wide() {\n" + lines(120, "\t_ = %03d // "+strings.Repeat("x", 90)) + "}\n",
		symbols: map[string]string{
			"func Wide() {": "Wide", "\t_ = 060 // " + strings.Repeat("x", 90): "Wide", "\t_ = 119 // " + strings.Repeat("x", 90): "Wide",
		},
	},
	{path: "notes.md", src: lines(100, "Line %d "+strings.Repeat("word ", 60))},
	{path: "blob.go", src: "package demo\n\nconst blob = `\n" + strings.Repeat("QUJD", 2000) + "\n`\n"},
	{path: "values.yaml", src: lines(70, "k%d: v") + "big: " + strings.Repeat("z", 7000) + "\n" + lines(5, "after%d: v")},
	{path: "run.sh", src: "f() { echo hi; }\nf\n"},
	{path: "schema.sql", src: "CREATE TABLE t (a int);\nCOMMENT ON COLUMN t.a IS 'the a column';\nCREATE INDEX i ON t (a);\n"},
	{path: "short.hpp", src: "struct a : b<c>\n#endif\nint x;\n"},
}

// corpusTree is the tree of a commit that holds the corpus.
func corpusTree(t *testing.T) *object.Tree {
	t.Helper()
	fs := memfs.New()
	r, _ := git.Init(memory.NewStorage(), git.WithWorkTree(fs))
	gittest.Unsigned(t, r)
	wt, _ := r.Worktree()
	for _, f := range corpus {
		w, _ := fs.Create(f.path)
		_, _ = w.Write([]byte(f.src))
		_ = w.Close()
		_, _ = wt.Add(f.path)
	}
	hash, err := wt.Commit("c", &git.CommitOptions{Author: &object.Signature{Name: "t", Email: "t@t", When: time.Now()}})
	if err != nil {
		t.Fatal(err)
	}
	c, _ := r.CommitObject(hash)
	tree, _ := c.Tree()
	return tree
}

// TestBuildCoversEveryLine: every line that holds a word is stored, as the
// source has it, in a chunk under the declaration that holds it, and no
// chunk is cut short or stored twice. A chunk over MaxChunkBytes is one
// line, the only text that is cut.
func TestBuildCoversEveryLine(t *testing.T) {
	chunks, _, _, err := Build(t.Context(), corpusTree(t), nil, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string][]Chunk{}
	for _, c := range chunks {
		byPath[c.Path] = append(byPath[c.Path], c)
	}
	for _, f := range corpus {
		t.Run(f.path, func(t *testing.T) {
			src := strings.Split(strings.TrimSuffix(f.src, "\n"), "\n")
			stored := checkChunks(t, byPath[f.path], src)
			for _, r := range f.runs {
				if !slices.ContainsFunc(byPath[f.path], func(c Chunk) bool { return c.StartLine <= r[0] && r[1] <= c.EndLine }) {
					t.Errorf("no chunk stores lines %d-%d together", r[0], r[1])
				}
			}
			for line := range f.symbols {
				if !slices.Contains(src, line) {
					t.Fatalf("symbols names %q, which is no line of the file", line)
				}
			}
			for i, line := range src {
				c, ok := stored[i+1]
				if strings.IndexFunc(line, isWord) >= 0 && !ok {
					t.Errorf("line %d %q is in no chunk", i+1, line)
				}
				if want, check := f.symbols[line]; check && c.Symbol != want {
					t.Errorf("line %d %q is in %s L%d-%d, want symbol %s", i+1, line, c.Symbol, c.StartLine, c.EndLine, want)
				}
			}
		})
	}
}

// TestBuildIsDeterministic: the same tree gives the same chunks, built one
// after another or many at once, as the vectors kept by a chunk's text rely
// on.
func TestBuildIsDeterministic(t *testing.T) {
	tree := corpusTree(t)
	build := func() ([]Chunk, error) {
		chunks, _, _, err := Build(t.Context(), tree, nil, nil, Options{})
		return chunks, err
	}
	want, err := build()
	if err != nil {
		t.Fatal(err)
	}
	const builds = 8
	got := make([][]Chunk, builds)
	errs := make([]error, builds)
	var wg sync.WaitGroup
	for i := range builds {
		wg.Go(func() { got[i], errs[i] = build() })
	}
	wg.Wait()
	for i := range builds {
		if errs[i] != nil {
			t.Fatalf("build %d: %v", i, errs[i])
		}
		if !slices.Equal(got[i], want) {
			t.Errorf("build %d gave %d chunks that differ from the first build's %d", i, len(got[i]), len(want))
		}
	}
}

// checkChunks checks that each of a file's chunks holds its lines as src has
// them, a word, and no lines that another chunk stores. It returns, by line,
// the smallest chunk that stores the line.
func checkChunks(t *testing.T, chunks []Chunk, src []string) map[int]Chunk {
	t.Helper()
	stored := map[int]Chunk{}
	seen := map[string]bool{}
	for _, c := range chunks {
		if c.StartLine < 1 || c.EndLine > len(src) || c.StartLine > c.EndLine {
			t.Fatalf("chunk %s L%d-%d is outside the file's %d lines", c.Symbol, c.StartLine, c.EndLine, len(src))
		}
		want := strings.Join(src[c.StartLine-1:c.EndLine], "\n")
		if c.Text != want && (c.StartLine != c.EndLine || !strings.HasPrefix(want, c.Text)) {
			t.Errorf("chunk %s L%d-%d holds %d bytes of its lines' %d", c.Symbol, c.StartLine, c.EndLine, len(c.Text), len(want))
		}
		if len(c.Text) > DefaultOptions.MaxChunkBytes && c.StartLine != c.EndLine {
			t.Errorf("chunk %s L%d-%d is %d bytes, over %d", c.Symbol, c.StartLine, c.EndLine, len(c.Text), DefaultOptions.MaxChunkBytes)
		}
		if strings.IndexFunc(c.Text, isWord) < 0 {
			t.Errorf("chunk %s L%d-%d holds no word: %q", c.Symbol, c.StartLine, c.EndLine, c.Text)
		}
		if seen[c.Text] || slices.ContainsFunc(chunks, func(o Chunk) bool {
			return o != c && o.StartLine <= c.StartLine && c.EndLine <= o.EndLine
		}) {
			t.Errorf("chunk %s L%d-%d repeats lines that another chunk stores", c.Symbol, c.StartLine, c.EndLine)
		}
		seen[c.Text] = true
		for l := c.StartLine; l <= c.EndLine; l++ {
			if s, ok := stored[l]; !ok || s.EndLine-s.StartLine > c.EndLine-c.StartLine {
				stored[l] = c
			}
		}
	}
	return stored
}
