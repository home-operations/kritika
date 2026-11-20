package indexer

import (
	"fmt"
	"strings"
	"testing"
	"unicode"

	"github.com/home-operations/kritika/internal/chunk"
	"github.com/home-operations/kritika/internal/textcut"
)

// lines numbers each of n lines that format gives, from 0.
func lines(n int, format string) string {
	var b strings.Builder
	for i := range n {
		fmt.Fprintf(&b, format+"\n", i)
	}
	return b.String()
}

// TestPiecesCoverTheFile: every line that holds a word lands in a stored
// chunk whose text is its whole line range, under the declaration that
// holds it, however large the declaration or long its lines.
func TestPiecesCoverTheFile(t *testing.T) {
	tests := []struct {
		name, path, src string
		// symbols names, by a line's text, the symbol of the chunk
		// that stores it.
		symbols map[string]string
	}{
		{
			name: "python function with an inner function", path: "big.py",
			src: "import os\n\n\ndef big(x):\n    \"\"\"Docstring of big.\"\"\"\n    total = 0\n    def inner(y):\n        return y + 1\n" +
				"    total += inner(1)\n" + lines(155, "    total += %d") + "    return total\n",
			symbols: map[string]string{
				`    """Docstring of big."""`: "big", "        return y + 1": "inner", "    total += 100": "big", "    return total": "big",
			},
		},
		{
			name: "java class of fields with an oversized method before a small one", path: "Demo.java",
			src: "package demo;\n\nimport java.util.List;\n\n/** Demo holds things. */\npublic class Demo {\n" + lines(30, "    private int f%d = 0;") +
				"\n    /** Big. */\n    public int big() {\n        int t = 0;\n" + lines(160, "        t += %d;") + "        return t;\n    }\n\n" +
				"    private int y = 1;\n\n    /** Small. */\n    public int small() { return y; }\n}\n",
			symbols: map[string]string{
				"public class Demo {": "Demo", "    private int f20 = 0;": "Demo", "    /** Big. */": "big", "        t += 120;": "big",
				"    /** Small. */": "small", "    public int small() { return y; }": "small",
			},
		},
		{
			name: "typescript class", path: "store.ts",
			src: "export class Store {\n" + lines(20, "  private a%d = 0;") + "  // get doc\n  get(): number { return 1; }\n  big(): number {\n    let t = 0;\n" +
				lines(140, "    t += %d;") + "    return t;\n  }\n  last(): number { return 2; }\n}\n",
			symbols: map[string]string{
				"  private a3 = 0;": "Store", "  // get doc": "get", "    t += 70;": "big", "  last(): number { return 2; }": "last",
			},
		},
		{
			name: "rust impl", path: "point.rs",
			src: "impl Point {\n    pub fn big() -> i32 {\n        let mut t = 0;\n" + lines(160, "        t += %d;") +
				"        t\n    }\n    /// Small.\n    pub fn small() -> i32 { 1 }\n}\n",
			symbols: map[string]string{"        t += 159;": "big", "    /// Small.": "small"},
		},
		{
			name: "go function of long lines", path: "long.go",
			src:     "package demo\n\nfunc Long() {\n" + lines(120, "\t_ = %03d // "+strings.Repeat("x", 90)) + "}\n",
			symbols: map[string]string{"func Long() {": "Long", "\t_ = 119 // " + strings.Repeat("x", 90): "Long"},
		},
		{
			name: "rust attributes that share a line", path: "point.rs",
			src: "/// A point in space\n#[derive(Debug, Clone)] // Copy would be wrong here\npub struct Point { x: i32 }\n\n" +
				"#[allow(clippy::too_many_arguments)] // the API needs them\npub fn build(a: i32) -> i32 { a }\n\n#[allow(dead_code)]\n\nfn lone() {}\n",
			symbols: map[string]string{
				"/// A point in space": "Point", "#[derive(Debug, Clone)] // Copy would be wrong here": "Point",
				"#[allow(clippy::too_many_arguments)] // the API needs them": "build", "fn lone() {}": "lone",
			},
		},
		{
			name: "one-character statements", path: "run.sh",
			src: "f() { echo hi; }\nf\n",
		},
		{
			name: "sql comment statements", path: "schema.sql",
			src: "CREATE TABLE t (a int);\nCOMMENT ON COLUMN t.a IS 'the a column';\nCREATE INDEX i ON t (a);\n",
		},
		{
			name: "markdown of long lines", path: "notes.md",
			src: lines(100, "Line %d "+strings.Repeat("word ", 60)),
		},
		{
			name: "a line over the byte cap", path: "data.json",
			src: "{\n\"a\": \"" + strings.Repeat("z", 7000) + "\",\n\"b\": 1\n}\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := (&chunk.Parser{}).Parse(tt.path, []byte(tt.src))
			defer f.Close()
			opts := DefaultOptions
			stored := map[int]chunk.Decl{}
			for _, p := range pieces(f, opts) {
				text := f.Text(p.StartLine, p.EndLine)
				if strings.IndexFunc(text, isWord) < 0 && p.Kind != "window" {
					t.Errorf("piece %+v holds no word: %q", p, text)
				}
				if len(text) > opts.MaxChunkBytes && p.StartLine != p.EndLine {
					t.Errorf("piece %+v is %d bytes, over %d", p, len(text), opts.MaxChunkBytes)
				}
				if cut := textcut.Prefix(text, opts.MaxChunkBytes); strings.Count(cut, "\n") != p.EndLine-p.StartLine {
					t.Errorf("piece %+v stores %d lines", p, strings.Count(cut, "\n")+1)
				}
				for l := p.StartLine; l <= p.EndLine; l++ {
					if _, ok := stored[l]; !ok || stored[l].Lines() > p.Lines() {
						stored[l] = p
					}
				}
			}
			for i, line := range strings.Split(strings.TrimSuffix(tt.src, "\n"), "\n") {
				p, ok := stored[i+1]
				if strings.IndexFunc(line, isWord) >= 0 && !ok {
					t.Errorf("line %d %q is in no piece", i+1, line)
				}
				if want, check := tt.symbols[line]; check && p.Symbol != want {
					t.Errorf("line %d %q is in %+v, want symbol %s", i+1, line, p, want)
				}
			}
		})
	}
}

// TestPiecesAtTheTopLevel: the package clause, imports and other unnamed
// statements at the top of a file are stored together, while each named
// declaration that fits the bounds is a chunk of its own.
func TestPiecesAtTheTopLevel(t *testing.T) {
	type span struct {
		symbol, kind string
		start, end   int
	}
	tests := []struct {
		name, path, src string
		want            []span
	}{
		{
			name: "go", path: "demo.go",
			src:  "// Package demo does things.\npackage demo\n\nimport (\n\t\"fmt\"\n\t\"os\"\n)\n\nconst one = 1\n\nfunc A() { fmt.Println(os.Args, one) }\n",
			want: []span{{"", "import", 1, 7}, {"one", "constant", 9, 9}, {"A", "function", 11, 11}},
		},
		{
			name: "python", path: "demo.py",
			src:  "\"\"\"Module doc.\"\"\"\nimport os\nimport sys\n\nos.environ.setdefault(\"X\", \"1\")\n\ndef f():\n    return sys.argv\n",
			want: []span{{"", "other", 1, 5}, {"f", "function", 7, 8}},
		},
		{
			name: "typescript", path: "demo.ts",
			src:  "import { a } from \"a\";\nimport { b } from \"b\";\n\nexport const x = a + b;\n",
			want: []span{{"", "import", 1, 2}, {"x", "", 4, 4}},
		},
		{
			name: "a run past the window", path: "many.py",
			src:  lines(70, "import m%d") + "\ndef f():\n    return 1\n",
			want: []span{{"", "import", 1, 60}, {"", "import", 61, 70}, {"f", "function", 72, 73}},
		},
		{
			name: "a declaration within the bounds", path: "long.go",
			src:  "package demo\n\nfunc Long() {\n" + lines(100, "\t_ = %d") + "}\n",
			want: []span{{"demo", "import", 1, 1}, {"Long", "function", 3, 104}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := (&chunk.Parser{}).Parse(tt.path, []byte(tt.src))
			defer f.Close()
			var got []span
			for _, p := range pieces(f, DefaultOptions) {
				got = append(got, span{p.Symbol, p.Kind, p.StartLine, p.EndLine})
			}
			if len(got) != len(tt.want) {
				t.Fatalf("pieces = %+v, want %+v", got, tt.want)
			}
			for i, w := range tt.want {
				if g := got[i]; g.symbol != w.symbol || g.start != w.start || g.end != w.end || w.kind != "" && g.kind != w.kind {
					t.Errorf("piece %d = %+v, want %+v", i, g, w)
				}
			}
		})
	}
}

func isWord(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }
