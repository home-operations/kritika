package chunk

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestFileWindow(t *testing.T) {
	tests := []struct {
		name, src           string
		first, last, radius int
		start, end          int
		text                string
	}{
		{name: "empty"},
		{name: "unterminated", src: "one\ntwo", first: 2, last: 2, start: 2, end: 2, text: "two"},
		{name: "trailing newline", src: "one\ntwo\n", first: 2, last: 2, start: 2, end: 2, text: "two"},
		{name: "blank final line", src: "one\n\n", first: 1, last: 2, start: 1, end: 2, text: "one\n"},
		{name: "radius", src: "l1\nl2\nl3\nl4\nl5\n", first: 3, last: 3, radius: 1, start: 2, end: 4, text: "l2\nl3\nl4"},
		{name: "clamped", src: "one\ntwo\nthree\n", first: 2, last: 2, radius: 9, start: 1, end: 3, text: "one\ntwo\nthree"},
		{name: "past end", src: "one\n", first: 2, last: 3},
		{name: "reversed", src: "one\ntwo\n", first: 2, last: 1},
		{name: "UTF8 and CRLF", src: "é\r\n世界\r\n", first: 2, last: 2, start: 2, end: 2, text: "世界\r"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := (&Parser{}).Parse("file.yaml", []byte(tt.src))
			f.Close()
			for range 2 {
				s, e, text := f.Window(tt.first, tt.last, tt.radius)
				if s != tt.start || e != tt.end || text != tt.text {
					t.Fatalf("Window = %d %d %q, want %d %d %q", s, e, text, tt.start, tt.end, tt.text)
				}
			}
			if tt.radius == 0 && f.Text(tt.first, tt.last) != tt.text {
				t.Fatal("Text differs from Window")
			}
		})
	}
}

func BenchmarkFileWindows(b *testing.B) {
	for _, lines := range []int{1000, 10000, 50000} {
		b.Run(fmt.Sprint(lines), func(b *testing.B) {
			src := []byte(strings.Repeat("value: abcdefghijklmnopqrstuvwxyz\n", lines))
			windows := Windows(src, 60, 10, 0)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				f := &File{Source: src}
				for _, w := range windows {
					_ = f.Text(w.StartLine, w.EndLine)
				}
			}
		})
	}
}

const goSource = `package demo

import "fmt"

// Widget is a thing.
type Widget struct {
	Name string
}

const (
	Small = 1
	Large = 2
)

func (w *Widget) Describe(prefix string) string {
	return fmt.Sprintf("%s %s", prefix, w.Name)
}

func Build(name string) *Widget {
	w := &Widget{Name: name}
	fmt.Println(w.Describe("built"))
	return w
}
`

func TestParseGoDeclarations(t *testing.T) {
	p := &Parser{}
	f := p.Parse("demo.go", []byte(goSource))
	if f.Language != "go" {
		t.Fatalf("language = %q", f.Language)
	}
	byName := map[string]Decl{}
	for _, d := range f.Decls {
		if _, dup := byName[d.Symbol]; dup {
			t.Errorf("%s declared twice in %+v", d.Symbol, f.Decls)
		}
		byName[d.Symbol] = d
	}
	tests := []struct {
		symbol, kind, scope string
		start, end          int
	}{
		{"Widget", "struct", "", 5, 8},
		{"Describe", "method", "Widget", 15, 17},
		{"Build", "function", "", 19, 23},
	}
	for _, tt := range tests {
		d, ok := byName[tt.symbol]
		if !ok {
			t.Fatalf("no declaration %q in %+v", tt.symbol, f.Decls)
		}
		if d.Kind != tt.kind || d.Scope != tt.scope || d.StartLine != tt.start || d.EndLine != tt.end {
			t.Errorf("%s = %+v, want kind=%s scope=%s L%d-%d", tt.symbol, d, tt.kind, tt.scope, tt.start, tt.end)
		}
	}
	if _, ok := byName["Small"]; !ok {
		// The const block is one declaration. Its first name is the symbol.
		t.Errorf("const block not chunked: %+v", f.Decls)
	}
	if d := f.Enclosing([]int{21}, 100); d == nil || d.Symbol != "Build" {
		t.Fatalf("Enclosing(21) = %+v", d)
	}
	if d := f.Enclosing([]int{21}, 3); d != nil {
		t.Fatalf("Enclosing with a 3-line cap should be nil, got %+v", d)
	}
	if d := f.Enclosing([]int{15, 22}, 100); d != nil {
		t.Fatalf("no declaration spans lines 15 and 22, got %+v", d)
	}
	ids := f.Identifiers([]int{20, 21})
	want := []string{"Describe", "Name", "Println", "Widget", "fmt", "name", "w"}
	for _, id := range want {
		if !contains(ids, id) {
			t.Errorf("identifiers %v missing %q", ids, id)
		}
	}
	if contains(ids, "built") || contains(ids, "return") {
		t.Errorf("identifiers %v should exclude string contents and keywords", ids)
	}
	if ids[0] != "w" {
		t.Errorf("most frequent identifier should come first, got %v", ids)
	}
}

func TestCloseKeepsTheDeclarations(t *testing.T) {
	p := &Parser{}
	f := p.Parse("demo.go", []byte(goSource))
	if ids := f.Identifiers([]int{19, 20, 21, 22, 23}); len(ids) == 0 {
		t.Fatal("no identifiers before Close")
	}
	decls := len(f.Decls)
	f.Close()
	f.Close()
	if len(f.Decls) != decls || f.Enclosing([]int{20}, 100) == nil {
		t.Fatalf("declarations after Close = %d, want %d still usable", len(f.Decls), decls)
	}
	if ids := f.Identifiers([]int{20}); ids != nil {
		t.Fatalf("identifiers after Close = %v, want none", ids)
	}
	p.Parse("none.txt", []byte("plain")).Close()
}

func TestParseFallsBackForConfigAndUnknown(t *testing.T) {
	p := &Parser{}
	yaml := p.Parse("values.yaml", []byte("a: 1\nb:\n  c: 2\n"))
	if yaml.Language != "yaml" || len(yaml.Decls) != 0 {
		t.Fatalf("yaml = lang %q decls %d; want no declarations", yaml.Language, len(yaml.Decls))
	}
	unknown := p.Parse("noext", []byte("hello"))
	if unknown.Language != "" || len(unknown.Decls) != 0 || unknown.Identifiers([]int{1}) != nil {
		t.Fatalf("unknown = %+v", unknown)
	}
	binary := p.Parse("x.go", []byte("package x\x00"))
	if len(binary.Decls) != 0 {
		t.Fatal("a file with a NUL byte must not be parsed")
	}
	name, decls := Language("main.rs")
	if name != "rust" || !decls {
		t.Fatalf("Language(main.rs) = %q %v", name, decls)
	}
	if name, decls := Language("Dockerfile"); name != "dockerfile" || decls {
		t.Fatalf("Language(Dockerfile) = %q %v; want a grammar without declarations", name, decls)
	}
}

func TestWindows(t *testing.T) {
	type span struct{ start, end int }
	tests := []struct {
		name                    string
		src                     string
		size, overlap, maxBytes int
		want                    []span
	}{
		{name: "empty", size: 60, overlap: 10},
		{name: "one line", src: "one", size: 60, overlap: 10, want: []span{{1, 1}}},
		{name: "by lines", src: strings.Repeat("x\n", 130), size: 60, overlap: 10, maxBytes: 6000, want: []span{{1, 60}, {51, 110}, {101, 130}}},
		{name: "no byte cap", src: strings.Repeat("x\n", 130), size: 60, overlap: 10, want: []span{{1, 60}, {51, 110}, {101, 130}}},
		// Ten bytes a line with its newline: 30 bytes hold three lines, and
		// the overlap shrinks with the window.
		{name: "by bytes", src: strings.Repeat("123456789\n", 7), size: 60, overlap: 10, maxBytes: 30, want: []span{{1, 3}, {4, 6}, {7, 7}}},
		{name: "by bytes with overlap", src: strings.Repeat("123456789\n", 14), size: 6, overlap: 3, maxBytes: 60, want: []span{{1, 6}, {4, 9}, {7, 12}, {10, 14}}},
		{name: "line over the cap", src: "short\n" + strings.Repeat("y", 50) + "\nshort\n", size: 60, overlap: 10, maxBytes: 20, want: []span{{1, 1}, {2, 2}, {3, 3}}},
		// The overlap before a line over the cap would hold nothing new.
		{
			name: "line over the cap after the overlap", src: strings.Repeat("x\n", 70) + strings.Repeat("y", 7000) + "\n" + strings.Repeat("x\n", 5),
			size: 60, overlap: 10, maxBytes: 6000, want: []span{{1, 60}, {51, 70}, {71, 71}, {72, 76}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []span
			for _, w := range Windows([]byte(tt.src), tt.size, tt.overlap, tt.maxBytes) {
				if w.Kind != "window" {
					t.Fatalf("kind = %q", w.Kind)
				}
				got = append(got, span{w.StartLine, w.EndLine})
			}
			if !slices.Equal(got, tt.want) {
				t.Fatalf("Windows = %v, want %v", got, tt.want)
			}
		})
	}
}

func contains(ss []string, s string) bool {
	return slices.Contains(ss, s)
}

// TestParseBounds: every parser has its memory budget, and no parser has a
// timeout, which would make a file's declarations depend on how loaded the
// machine is: a parse that the deadline stops partway can return a tree of
// one error node and no error. A file built to drive the parse stack deep
// falls back to fixed windows at once.
func TestParseBounds(t *testing.T) {
	var p Parser
	for _, path := range []string{"x.go", "x.js"} {
		parser := p.language(path).parser
		if got := parser.MemoryBudgetBytes(); got != parseMemoryBudget {
			t.Errorf("%s: memory budget = %d, want %d", path, got, parseMemoryBudget)
		}
		if got := parser.TimeoutMicros(); got != 0 {
			t.Errorf("%s: timeout = %dµs, want none", path, got)
		}
	}
	start := time.Now()
	f := p.Parse("deep.js", bytes.Repeat([]byte("("), 128<<10))
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("a deep parse took %s", took)
	}
	if len(f.Decls) != 0 {
		t.Errorf("a deep parse found %d declarations", len(f.Decls))
	}
}

// TestDeclarationsTakeTheirLead: a declaration's range takes in the comments,
// decorators and attributes directly above it, and a statement that only
// wraps one declaration (an export, a decorated definition) is that
// declaration, under its symbol. Its head line is the first below them.
func TestDeclarationsTakeTheirLead(t *testing.T) {
	type want struct {
		symbol, kind, scope string
		start, end, head    int
	}
	tests := []struct {
		name, path, src string
		want            []want
	}{
		{
			name: "go doc comment", path: "demo.go",
			src: `package demo

// Widget is a thing.
// It has a name.
type Widget struct{ Name string }

// unattached

func Build() {}
`,
			want: []want{{"Widget", "struct", "", 3, 5, 5}, {"Build", "function", "", 9, 9, 9}},
		},
		{
			name: "python decorators and comments", path: "demo.py",
			src: `import os

# A helper.
@decorator(
    "x",
)
def helper(x):
    return x


class Foo:
    """Foo doc."""

    # bar doc
    def bar(self):
        return 1

    @property
    def baz(self):
        return 2
`,
			want: []want{
				{"helper", "function", "", 3, 8, 7},
				{"Foo", "class", "", 11, 20, 11},
				{"bar", "function", "Foo", 14, 16, 15},
				{"baz", "function", "Foo", 18, 20, 19},
			},
		},
		{
			name: "typescript exports", path: "demo.ts",
			src: `import { x } from "y";

/** Adds. */
export function add(a: number, b: number): number {
  return a + b;
}

export class Box {
  // get doc
  get(): number { return 1; }
}

export const limit = 1;
`,
			want: []want{
				{"add", "function", "", 3, 6, 4},
				{"Box", "class", "", 8, 11, 8},
				{"get", "method", "Box", 9, 10, 10},
				{"limit", "", "", 13, 13, 13},
			},
		},
		{
			name: "java javadoc", path: "Demo.java",
			src: `package demo;

/** A class. */
@Deprecated
public class Demo {
    private int x;

    /**
     * Gets.
     */
    @Override
    public int get() { return x; }
}
`,
			want: []want{{"Demo", "class", "", 3, 13, 5}, {"get", "method", "Demo", 8, 12, 12}},
		},
		{
			name: "rust doc comments and attributes", path: "demo.rs",
			src: `use std::fmt;

/// A point.
#[derive(Debug)]
pub struct Point { x: i32 }

impl Point {
    /// New.
    #[inline]
    pub fn new() -> Self { Point { x: 0 } }
}
`,
			want: []want{{"Point", "type", "", 3, 5, 5}, {"new", "function", "", 8, 10, 10}},
		},
		{
			name: "rust attributes that share a line", path: "demo.rs",
			src: `/// A point in space
#[derive(Debug, Clone)] // Copy would be wrong here
pub struct Point { x: i32 }

#[allow(clippy::too_many_arguments)] // the API needs them
#[inline] #[must_use]
pub fn build(a: i32) -> i32 { a }

#[allow(dead_code)]

fn lone() {}
`,
			want: []want{
				{"Point", "type", "", 1, 3, 3}, {"build", "function", "", 5, 7, 7},
				{"", "other", "", 9, 9, 9}, {"lone", "function", "", 11, 11, 11},
			},
		},
		{
			name: "java annotations on one line", path: "A.java",
			src: `class A {
    @Override @Deprecated
    public int get() { return 1; }
}
`,
			want: []want{{"A", "class", "", 1, 4, 1}, {"get", "method", "A", 2, 3, 3}},
		},
		{
			name: "byte order mark", path: "bom.go",
			src:  "\ufeff// Package c does things.\npackage c\n\nfunc F() {}\n",
			want: []want{{"c", "import", "", 1, 2, 2}, {"F", "function", "", 4, 4, 4}},
		},
		{
			name: "c preprocessor and a comment after code", path: "demo.c",
			src: `#include <stdio.h>
// Adds.
int add(int a) { return a; }
int x = 1; /* a
 b */
int f(void) { return x; }
`,
			want: []want{
				{"", "other", "", 1, 1, 1}, {"add", "function", "", 2, 3, 3},
				{"x", "", "", 4, 4, 4}, {"f", "function", "", 6, 6, 6},
			},
		},
		{
			name: "c++ templates", path: "demo.cpp",
			src: `#include <vector>
#include <string>

template <typename T>
T max_of(T a, T b) {
  return a > b ? a : b;
}

template <typename T>
T min_of(T a, T b) {
  return a < b ? a : b;
}
`,
			want: []want{
				{"", "other", "", 1, 1, 1}, {"", "other", "", 2, 2, 2},
				{"max_of", "function", "", 4, 7, 5}, {"min_of", "function", "", 9, 12, 10},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := (&Parser{}).Parse(tt.path, []byte(tt.src))
			defer f.Close()
			for _, w := range tt.want {
				i := slices.IndexFunc(f.Decls, func(d Decl) bool { return d.Symbol == w.symbol && d.StartLine == w.start })
				if i < 0 {
					t.Errorf("no declaration %s at line %d in %+v", w.symbol, w.start, f.Decls)
					continue
				}
				d := f.Decls[i]
				if d.EndLine != w.end || d.HeadLine != w.head || d.Scope != w.scope || w.kind != "" && d.Kind != w.kind {
					t.Errorf("%s = %+v, want kind=%s scope=%s L%d-%d head L%d", w.symbol, d, w.kind, w.scope, w.start, w.end, w.head)
				}
			}
			for _, d := range f.Decls {
				if d.Symbol != "" {
					continue
				}
				if slices.ContainsFunc(f.Decls, func(o Decl) bool { return o.Symbol != "" && o.EndLine == d.EndLine && o.StartLine >= d.StartLine }) {
					t.Errorf("unnamed %+v wraps a named declaration in %+v", d, f.Decls)
				}
			}
		})
	}
}

// TestEnclosingMeasuresFromTheHeadLine: the comments above a declaration
// do not count against the line limit. A declaration they push over it is
// returned from its head line.
func TestEnclosingMeasuresFromTheHeadLine(t *testing.T) {
	src := "package demo\n\n" + strings.Repeat("// F does things.\n", 15) + "func F() {\n" +
		strings.Repeat("\t_ = 1\n", 138) + "}\n\n// G is small.\nfunc G() {}\n"
	f := (&Parser{}).Parse("demo.go", []byte(src))
	defer f.Close()
	tests := []struct {
		name       string
		lines      []int
		max        int
		start, end int
	}{
		{name: "body of a declaration its doc pushes over", lines: []int{100}, max: 150, start: 18, end: 157},
		{name: "head line", lines: []int{18}, max: 150, start: 18, end: 157},
		{name: "doc of a declaration its doc pushes over", lines: []int{5}, max: 150},
		{name: "everything fits", lines: []int{5, 100}, max: 155, start: 3, end: 157},
		{name: "body over the limit", lines: []int{100}, max: 139},
		{name: "small declaration keeps its doc", lines: []int{160}, max: 150, start: 159, end: 160},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := f.Enclosing(tt.lines, tt.max)
			switch {
			case tt.start == 0 && d != nil:
				t.Fatalf("Enclosing(%v, %d) = %+v, want nil", tt.lines, tt.max, d)
			case tt.start != 0 && (d == nil || d.StartLine != tt.start || d.EndLine != tt.end):
				t.Fatalf("Enclosing(%v, %d) = %+v, want L%d-%d", tt.lines, tt.max, d, tt.start, tt.end)
			}
		})
	}
}

// TestStatementsWithoutLetters: a top-level statement whose only identifiers
// are made of $ and _ is a declaration, while one of punctuation alone is
// not.
func TestStatementsWithoutLetters(t *testing.T) {
	tests := []struct {
		name, path, src string
		line            int
		want            bool
	}{
		{name: "javascript identifiers", path: "a.js", src: "function f() { return 1; }\n$_++;\n", line: 2, want: true},
		{name: "javascript empty statement", path: "a.js", src: "function f() { return 1; }\n;\n", line: 2},
		{name: "python ellipsis", path: "a.py", src: "def f():\n    pass\n\n...\n", line: 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := (&Parser{}).Parse(tt.path, []byte(tt.src))
			defer f.Close()
			if got := slices.ContainsFunc(f.Decls, func(d Decl) bool { return d.Contains(tt.line) }); got != tt.want {
				t.Errorf("line %d in a declaration = %t, want %t: %+v", tt.line, got, tt.want, f.Decls)
			}
		})
	}
}
