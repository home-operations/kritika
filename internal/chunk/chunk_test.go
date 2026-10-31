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
			windows := Windows(src, 60, 10)
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
		{"Widget", "struct", "", 6, 8},
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
	src := []byte(strings.Repeat("x\n", 130))
	w := Windows(src, 60, 10)
	if len(w) != 3 || w[0] != (Decl{Kind: "window", StartLine: 1, EndLine: 60}) || w[1].StartLine != 51 || w[2].StartLine != 101 || w[2].EndLine != 130 {
		t.Fatalf("Windows = %+v", w)
	}
	if got := Windows([]byte("one"), 60, 10); len(got) != 1 || got[0].EndLine != 1 {
		t.Fatalf("Windows(one line) = %+v", got)
	}
	if Windows(nil, 60, 10) != nil {
		t.Fatal("empty file must give no windows")
	}
}

func contains(ss []string, s string) bool {
	return slices.Contains(ss, s)
}

// TestParseBounds: every parser has its memory budget. A file built to drive
// the parse stack deep falls back to fixed windows within the parse timeout,
// which alone does not stop it.
func TestParseBounds(t *testing.T) {
	var p Parser
	for _, path := range []string{"x.go", "x.js"} {
		if got := p.language(path).parser.MemoryBudgetBytes(); got != parseMemoryBudget {
			t.Errorf("%s: memory budget = %d, want %d", path, got, parseMemoryBudget)
		}
	}
	start := time.Now()
	f := p.Parse("deep.js", bytes.Repeat([]byte("("), 128<<10))
	if took := time.Since(start); took > parseTimeout {
		t.Errorf("a deep parse took %s, past the %s timeout", took, parseTimeout)
	}
	if len(f.Decls) != 0 {
		t.Errorf("a deep parse found %d declarations", len(f.Decls))
	}
}
