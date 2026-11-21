package indexer

import (
	"fmt"
	"strings"
	"testing"

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
			name: "class over the byte cap within the line cap", path: "wide.ts",
			src: "export class Wide {\n" + lines(40, "  private w%d = \""+strings.Repeat("x", 200)+"\";") +
				"  // small doc\n  small(): number { return 1; }\n}\n",
			symbols: map[string]string{
				"export class Wide {": "Wide", "  // small doc": "small", "  small(): number { return 1; }": "small",
			},
		},
		{
			name: "short gaps that fit their methods' lines but not their bytes", path: "gaps.ts",
			src: "export class Gaps {\n  private head = \"" + strings.Repeat("h", 4000) + "\";\n  m(): number {\n    let t = 0;\n" +
				lines(40, "    t += %d; // "+strings.Repeat("m", 60)) + "    return t;\n  }\n  n(): number {\n    let u = 0;\n" +
				lines(40, "    u += %d; // "+strings.Repeat("n", 60)) + "    return u;\n  }\n  private tail = \"" + strings.Repeat("t", 4000) + "\";\n}\n",
			symbols: map[string]string{
				"  private head = \"" + strings.Repeat("h", 4000) + "\";": "Gaps", "  m(): number {": "m",
				"  n(): number {": "n", "  private tail = \"" + strings.Repeat("t", 4000) + "\";": "Gaps",
			},
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
			name: "identifiers of punctuation", path: "tail.js",
			src: "function f() { return 1; }\n$_++;\n",
		},
		{
			name: "declarations that share a line", path: "shared.js",
			src: "function a() { return 1; } function b() { return 2; }\n\nfunction c() { return 3; } function d() {\n  return 4;\n}\n\nrun(); const x = 1;\n",
			symbols: map[string]string{
				"function a() { return 1; } function b() { return 2; }": "a", "function c() { return 3; } function d() {": "d", "run(); const x = 1;": "x",
			},
		},
		{
			name: "sql comment statements", path: "schema.sql",
			src: "CREATE TABLE t (a int);\nCOMMENT ON COLUMN t.a IS 'the a column';\nCREATE INDEX i ON t (a);\n",
		},
		{
			name: "a long line before a closing one", path: "data.go",
			src: "package demo\n\nconst blob = `\n" + strings.Repeat("QUJD", 2000) + "\n`\n",
		},
		// A parse that error recovery leaves short without an error, which
		// odvcencio/gotreesitter#1632 did not fix, and one that it leaves with
		// an error node at the top level.
		{
			name: "a parse that stops short", path: "short.hpp",
			src: "struct a : b<c>\n#endif\nint x;\n",
		},
		{
			name: "a parse with an error at the top level", path: "hidden.hpp",
			src: "  typedef detached_t default_completion_token_type;\n  {\n  }\n  return typename decay_t<T>::template rebind_executor<\n    T>::other;\n\n" +
				"int after() { return 1; }\n",
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
			first := map[int]chunk.Decl{}
			for _, p := range pieces(f, opts) {
				if o, ok := first[p.StartLine]; ok {
					t.Errorf("pieces %+v and %+v both start on line %d", o, p, p.StartLine)
				}
				first[p.StartLine] = p
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
		{
			name: "c++ templates", path: "max.cpp",
			src: "#include <vector>\n#include <string>\n\ntemplate <typename T>\nT max_of(T a, T b) {\n  return a > b ? a : b;\n}\n\n" +
				"template <typename T>\nT min_of(T a, T b) {\n  return a < b ? a : b;\n}\n",
			want: []span{{"", "other", 1, 2}, {"max_of", "function", 4, 7}, {"min_of", "function", 9, 12}},
		},
		{
			name: "c++ blocks around declarations", path: "blocks.cpp",
			src: "#include <cstdio>\n\nnamespace {\nint helper(int x) { return x + 1; }\n}\n\nextern \"C\" {\nint exported(int x) { return helper(x); }\n}\n\n" +
				"#if defined(FOO)\nint foo_only(void) { return 1; }\n#endif\n\nint main() { return 0; }\n",
			want: []span{
				{"", "other", 1, 1}, {"helper", "function", 3, 4}, {"exported", "function", 7, 8},
				{"foo_only", "function", 11, 13}, {"main", "function", 15, 15},
			},
		},
		{
			name: "python blocks around definitions", path: "compat.py",
			src: "import os\n\ntry:\n    import ujson as json\nexcept ImportError:\n    import json\n\n" +
				"if os.name == \"nt\":\n    def path_sep():\n        return \"\\\\\"\nelse:\n    def path_sep():\n        return \"/\"\n",
			want: []span{{"", "other", 1, 6}, {"path_sep", "function", 8, 10}, {"path_sep", "function", 11, 13}},
		},
		{
			name: "go alias group", path: "alias.go",
			src:  "package e\n\nimport \"fmt\"\n\ntype (\n\tA = fmt.Stringer\n\tB = error\n)\n",
			want: []span{{"", "import", 1, 3}, {"A", "", 5, 6}, {"B", "", 7, 7}},
		},
		{
			name: "hcl blocks", path: "main.tf",
			src: "resource \"aws_s3_bucket\" \"b\" {\n  bucket = \"x\"\n}\n\nvariable \"region\" {\n  default = \"us-east-1\"\n}\n\n" +
				"output \"id\" {\n  value = aws_s3_bucket.b.id\n}\n",
			want: []span{{"b", "", 1, 3}, {"region", "", 5, 7}, {"id", "", 9, 11}},
		},
		{
			name: "a declaration that starts on the last line of another", path: "shared.js",
			src:  "function a() {\n  return 1;\n} function b() { return 2; }\n",
			want: []span{{"a", "function", 1, 3}, {"b", "function", 3, 3}},
		},
		{
			name: "a statement on the last line of a declaration", path: "tail.js",
			src:  "function a() {\n  return 1;\n} run();\n",
			want: []span{{"a", "function", 1, 3}},
		},
		{
			name: "a statement on the last line of a parse error", path: "error.hpp",
			src:  "public:\n#if !defined(DOCS)\n  ::x; f();\nint run() { return 1; }\n",
			want: []span{{"public", "other", 1, 3}, {"run", "function", 4, 4}},
		},
		{
			name: "a named statement on the last line of a parse error", path: "access.hpp",
			src:  "public:\n#if !defined(DOCS)\n  typedef int type;\n#endif\n\nint run() { return 1; }\n",
			want: []span{{"public", "other", 1, 3}, {"type", "other", 3, 3}, {"", "other", 4, 4}, {"run", "function", 6, 6}},
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

func isWord(r rune) bool { return chunk.Worded(r) }
