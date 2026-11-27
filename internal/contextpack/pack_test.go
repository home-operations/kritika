package contextpack

import (
	"fmt"
	"math/rand/v2"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-billy/v6/memfs"
	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/go-git/go-git/v6/storage/memory"

	"github.com/home-operations/kritika/internal/chunk"
	"github.com/home-operations/kritika/internal/gittest"
	"github.com/home-operations/kritika/internal/textcut"
)

func TestBoundedCandidatesPreserveSelection(t *testing.T) {
	random := rand.New(rand.NewPCG(1, 2))
	for _, n := range []int{0, 1, 2, 5} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			for range 100 {
				all := make([]Chunk, 0, 100)
				set := &candidates{}
				for i := range 100 {
					c := Chunk{Path: fmt.Sprint(random.IntN(8)), StartLine: random.IntN(15), Text: fmt.Sprint(i)}
					all = append(all, c)
					set.count++
					if keepCandidate(set.chunks, c, n) {
						set.chunks = append(set.chunks, c)
					}
					if got, want := set.pick(n), pick(all, n); !reflect.DeepEqual(got, want) {
						t.Fatalf("selection at %d: %+v, want %+v", i, got, want)
					}
					if len(set.chunks) > max(0, 3*n-2) {
						t.Fatalf("retained %d candidates for limit %d", len(set.chunks), n)
					}
				}
			}
		})
	}
}

func TestScanBoundsCandidatesAndText(t *testing.T) {
	fs := memfs.New()
	r, err := git.Init(memory.NewStorage(), git.WithWorkTree(fs))
	if err != nil {
		t.Fatal(err)
	}
	gittest.Unsigned(t, r)
	wt, err := r.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	for i := range 20 {
		var src strings.Builder
		src.WriteString("package demo\n")
		for j := range 50 {
			fmt.Fprintf(&src, "func Call%d() {\nTarget()\n%s}\n", j, strings.Repeat("// padding 世界 padding padding padding\n", 30))
		}
		name := fmt.Sprintf("f%03d.go", i)
		f, err := fs.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(src.String())); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := wt.Add(name); err != nil {
			t.Fatal(err)
		}
	}
	h, err := wt.Commit("head", &git.CommitOptions{Author: &object.Signature{Name: "t", Email: "t@t", When: time.Now()}})
	if err != nil {
		t.Fatal(err)
	}
	c, err := r.CommitObject(h)
	if err != nil {
		t.Fatal(err)
	}
	head, err := c.Tree()
	if err != nil {
		t.Fatal(err)
	}
	opts := DefaultOptions()
	opts.MaxChunkBytes = 80
	b := &builder{in: Input{Head: head}, opts: opts, parser: &chunk.Parser{}, changedSymbols: []string{"Target"}}
	if err := scanParts(t.Context(), []*builder{b}, &b.stats); err != nil {
		t.Fatal(err)
	}
	retained := b.callers["Target"].chunks
	if len(retained) > 2*opts.CallersPerSymbol-1 {
		t.Fatalf("retained %d chunks from 1000 matches", len(retained))
	}
	for _, c := range retained {
		if len(c.Text) > opts.MaxChunkBytes+len("\n… (truncated)") || !strings.HasSuffix(c.Text, "\n… (truncated)") {
			t.Fatalf("candidate text = %q", c.Text)
		}
	}
	out := b.assemble()
	if len(out) != opts.CallersPerSymbol {
		t.Fatalf("emitted %d callers", len(out))
	}
	for i, c := range out {
		if c.Path != fmt.Sprintf("f%03d.go", i) || c.Symbol != "Call0" || strings.Count(c.Text, "… (truncated)") != 1 {
			t.Fatalf("caller %d = %+v", i, c)
		}
	}
}

const widgetBase = `package demo

// Widget is a thing.
type Widget struct {
	Name string
}

func Build(name string) *Widget {
	if name == "" {
		name = "default"
	}
	if len(name) > 64 {
		name = name[:64]
	}
	w := &Widget{Name: name}
	if w.Name == "skip" {
		return nil
	}
	return w
}
`

const widgetHead = `package demo

// Widget is a thing.
type Widget struct {
	Name string
}

func Build(name string) *Widget {
	if name == "" {
		name = "default"
	}
	if len(name) > 64 {
		name = name[:64]
	}
	w := &Widget{Name: Normalize(name)}
	if w.Name == "skip" {
		return nil
	}
	return w
}
`

const useSource = `package demo

import "fmt"

func Normalize(s string) string {
	return s
}

func Use() {
	fmt.Println(Build("x"))
}

func Unrelated() {}
`

// repo builds base and head commits in memory and returns their trees and
// the diff between them.
func repo(t *testing.T) (head, base *object.Tree, diff string) {
	t.Helper()
	fs := memfs.New()
	r, err := git.Init(memory.NewStorage(), git.WithWorkTree(fs))
	if err != nil {
		t.Fatal(err)
	}
	gittest.Unsigned(t, r)
	wt, _ := r.Worktree()
	write := func(name, content string) {
		f, _ := fs.Create(name)
		_, _ = f.Write([]byte(content))
		_ = f.Close()
		_, _ = wt.Add(name)
	}
	sig := &object.Signature{Name: "t", Email: "t@t", When: time.Now()}
	write("widget.go", widgetBase)
	write("use.go", useSource)
	write("vendor/dep.go", "package dep\n\nfunc Build() {}\n")
	write("values.yaml", "image: 1\n")
	baseHash, _ := wt.Commit("base", &git.CommitOptions{Author: sig})
	write("widget.go", widgetHead)
	headHash, _ := wt.Commit("head", &git.CommitOptions{Author: sig})
	bc, _ := r.CommitObject(baseHash)
	hc, _ := r.CommitObject(headHash)
	base, _ = bc.Tree()
	head, _ = hc.Tree()
	patch, _ := bc.Patch(hc)
	return head, base, patch.String()
}

func TestBuildStages(t *testing.T) {
	head, base, diff := repo(t)
	chunks, stats, err := Build(t.Context(), Input{Head: head, Base: base, Diff: diff, Changed: []string{"widget.go"}, Ignore: []string{"vendor/**"}}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	byStage := map[string][]Chunk{}
	for _, c := range chunks {
		byStage[c.Stage] = append(byStage[c.Stage], c)
	}
	ov := byStage[StageOverlay]
	if len(ov) != 1 || ov[0].Symbol != "Build" || ov[0].Kind != "function" || !strings.Contains(ov[0].Text, "Normalize(name)") || !strings.Contains(ov[0].Text, "return w") || ov[0].Path != "widget.go" {
		t.Fatalf("overlay = %+v", ov)
	}
	checkDefinitions(t, byStage[StageDefinition])
	callers := byStage[StageCaller]
	if len(callers) != 1 || callers[0].Symbol != "Use" || callers[0].Ref != "Build" || callers[0].Path != "use.go" {
		t.Fatalf("callers = %+v; want Use in use.go", callers)
	}
	if stats.FilesScanned != 2 || stats.FilesParsed != 2 || stats.Overlay != 1 || stats.Callers != 1 || stats.ScanTruncated {
		t.Fatalf("stats = %+v", stats)
	}
	for _, c := range chunks {
		if c.Text == "" || c.StartLine == 0 || c.EndLine < c.StartLine {
			t.Fatalf("bad chunk %+v", c)
		}
	}
}

func checkDefinitions(t *testing.T, defs []Chunk) {
	t.Helper()
	var normalize, widget bool
	for _, d := range defs {
		switch {
		case d.Symbol == "Normalize" && d.Path == "use.go" && d.Ref == "Normalize":
			normalize = true
		case d.Symbol == "Widget" && d.Path == "widget.go" && d.Kind == "struct":
			widget = true
		case d.Path == "vendor/dep.go":
			t.Fatalf("ignored path leaked into definitions: %+v", d)
		}
	}
	if !normalize || !widget {
		t.Fatalf("definitions = %+v; want Normalize (other file) and Widget (same file)", defs)
	}
}

func TestBuildWindowsConfigFiles(t *testing.T) {
	head, base, _ := repo(t)
	diff := "diff --git a/values.yaml b/values.yaml\n--- a/values.yaml\n+++ b/values.yaml\n@@ -1 +1 @@\n-image: 1\n+image: 2\n"
	chunks, _, err := Build(t.Context(), Input{Head: head, Base: base, Diff: diff, Changed: []string{"values.yaml"}}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	// A one-line file is fully shown by the diff, so the window adds nothing.
	if len(chunks) != 0 {
		t.Fatalf("chunks = %+v; want none", chunks)
	}
}

func TestParseDiff(t *testing.T) {
	d := parseDiff("diff --git a/x.go b/x.go\n--- a/x.go\n+++ b/x.go\n@@ -3,4 +3,5 @@\n a\n-b\n+B\n+C\n d\n e\n")
	if got := d.added["x.go"]; len(got) != 2 || got[0] != 4 || got[1] != 5 {
		t.Fatalf("added = %v", got)
	}
	if got := d.removed["x.go"]; len(got) != 1 || got[0] != 4 {
		t.Fatalf("removed = %v", got)
	}
	if _, ok3 := d.shown["x.go"][3]; !ok3 {
		t.Fatalf("shown = %v", d.shown["x.go"])
	}
	if _, ok7 := d.shown["x.go"][7]; !ok7 {
		t.Fatalf("shown = %v", d.shown["x.go"])
	}
	if _, ok8 := d.shown["x.go"][8]; ok8 {
		t.Fatalf("shown = %v", d.shown["x.go"])
	}
	if r := runs([]int{1, 2, 3, 10, 30, 31}, 5); len(r) != 3 || r[0] != [2]int{1, 3} || r[1] != [2]int{10, 10} || r[2] != [2]int{30, 31} {
		t.Fatalf("runs = %v", r)
	}
}

func TestPickPrefersDistinctFiles(t *testing.T) {
	cs := []Chunk{{Path: "a", StartLine: 1}, {Path: "a", StartLine: 10}, {Path: "b", StartLine: 1}}
	got := pick(cs, 2)
	if len(got) != 2 || got[0].Path != "a" || got[1].Path != "b" {
		t.Fatalf("pick = %+v", got)
	}
	if got := pick(cs, 5); len(got) != 3 {
		t.Fatalf("pick under the cap must keep everything, got %d", len(got))
	}
}

// TestDiffEdges: a new file's hunk, a deleted file's, and lines that read
// like file headers ("-- x" removed, "++ x" added) are all hunk lines.
func TestDiffEdges(t *testing.T) {
	diff := "diff --git a/new.go b/new.go\nnew file mode 100644\n--- /dev/null\n+++ b/new.go\n@@ -0,0 +1,2 @@\n+package x\n+func F() {}\n" +
		"diff --git a/q.sql b/q.sql\n--- a/q.sql\n+++ b/q.sql\n@@ -1,3 +1,3 @@\n SELECT 1;\n--- old comment\n+++ new comment\n SELECT 2;\n" +
		"diff --git a/gone.go b/gone.go\ndeleted file mode 100644\n--- a/gone.go\n+++ /dev/null\n@@ -1,2 +0,0 @@\n-package gone\n-var V int\n"
	d := parseDiff(diff)
	for name, got := range map[string][]int{"added new.go": d.added["new.go"], "added q.sql": d.added["q.sql"],
		"removed q.sql": d.removed["q.sql"], "removed gone.go": d.removed["gone.go"]} {
		want := map[string][]int{"added new.go": {1, 2}, "added q.sql": {2}, "removed q.sql": {2}, "removed gone.go": {1, 2}}[name]
		if !slices.Equal(got, want) {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}
	if shown := ShownLines(diff); len(shown["q.sql"]) != 3 || len(shown["new.go"]) != 2 || shown["gone.go"] != nil {
		t.Errorf("shown = %v", shown)
	}
	// The deleted file is matched by its base-side path, the only one it has.
	for _, tt := range []struct {
		ignore []string
		want   int
	}{{nil, 6}, {[]string{"*.sql"}, 4}, {[]string{"gone.go"}, 4}, {[]string{"**"}, 0}} {
		if got := ChangedLines(diff, tt.ignore); got != tt.want {
			t.Errorf("ChangedLines(ignore %v) = %d, want %d", tt.ignore, got, tt.want)
		}
	}
	// A removed line touches the head line that now follows it. A deleted
	// file has no head lines to touch.
	if touched := TouchedLines(diff); len(touched) != 2 || !slices.Equal(touched["new.go"], []int{1, 2}) ||
		!slices.Equal(touched["q.sql"], []int{2, 2}) {
		t.Errorf("touched = %v", touched)
	}
	moved := "diff --git a/old.go b/moved.go\nsimilarity index 80%\nrename from old.go\nrename to moved.go\n--- a/old.go\n+++ b/moved.go\n" +
		"@@ -1,2 +1,2 @@\n a\n-b\n+B\n" + diff
	if renames := Renames(moved); len(renames) != 1 || renames["old.go"] != "moved.go" {
		t.Errorf("renames = %v", renames)
	}
}

// TestOverlayReadsARenamedFilesBase: the lines that a diff removed from a
// renamed file are read from the base tree under the old name. So the
// identifiers they carry are looked up like any removed line's.
func TestOverlayReadsARenamedFilesBase(t *testing.T) {
	fs := memfs.New()
	r, err := git.Init(memory.NewStorage(), git.WithWorkTree(fs))
	if err != nil {
		t.Fatal(err)
	}
	gittest.Unsigned(t, r)
	wt, _ := r.Worktree()
	write := func(name, content string) {
		f, _ := fs.Create(name)
		_, _ = f.Write([]byte(content))
		_ = f.Close()
		_, _ = wt.Add(name)
	}
	sig := &object.Signature{Name: "t", Email: "t@t", When: time.Now()}
	// Enough unchanged lines for the rename to be detected.
	filler := "\n// line one of the file\n// line two of the file\n// line three of the file\n// line four of the file\n" +
		"// line five of the file\n// line six of the file\n// line seven of the file\n// line eight of the file\n"
	write("old.go", "package demo\n"+filler+"\nfunc Gone() { helperBefore() }\n")
	write("helpers.go", "package demo\n\nfunc helperBefore() {}\n\nfunc helperAfter() {}\n")
	baseHash, _ := wt.Commit("base", &git.CommitOptions{Author: sig})
	_, _ = wt.Remove("old.go")
	write("moved.go", "package demo\n"+filler+"\nfunc Gone() { helperAfter() }\n")
	headHash, _ := wt.Commit("head", &git.CommitOptions{Author: sig})
	bc, _ := r.CommitObject(baseHash)
	hc, _ := r.CommitObject(headHash)
	base, _ := bc.Tree()
	head, _ := hc.Tree()
	patch, _ := bc.Patch(hc)
	diff := patch.String()
	if renames := Renames(diff); renames["old.go"] != "moved.go" {
		t.Fatalf("no rename detected in:\n%s", diff)
	}
	chunks, _, err := Build(t.Context(), Input{Head: head, Base: base, Diff: diff, Changed: []string{"moved.go"}}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	var defined []string
	for _, c := range chunks {
		if c.Stage == StageDefinition {
			defined = append(defined, c.Symbol)
		}
	}
	slices.Sort(defined)
	if want := []string{"helperAfter", "helperBefore"}; !slices.Equal(defined, want) {
		t.Fatalf("definitions = %v, want %v: the removed line's helper comes from the base side under the old name", defined, want)
	}
}

// TestBuildStopsAtTheScanBudget: a head tree over the scan budget truncates
// the scan rather than failing the pack.
func TestBuildStopsAtTheScanBudget(t *testing.T) {
	head, base, diff := repo(t)
	opts := DefaultOptions()
	opts.MaxScanFiles = 1
	_, stats, err := Build(t.Context(), Input{Head: head, Base: base, Diff: diff, Changed: []string{"widget.go"}}, opts)
	if err != nil || !stats.ScanTruncated {
		t.Fatalf("Build = %+v, %v; want a truncated scan and no error", stats, err)
	}
}

// TestScanReadsDeclarationsFromTheirHead: a mention in the comments above a
// declaration does not make it a caller, while a decorator does. A caller
// whose comments would push its body past the byte budget loses comments
// from the top until its body fits.
func TestScanReadsDeclarationsFromTheirHead(t *testing.T) {
	head := headTree(t, map[string]string{
		"callers.go": "package demo\n\n// Doc is like Target, but louder.\nfunc Doc() {}\n\n" +
			"// Long calls Target.\n" + strings.Repeat("// padding padding padding\n", 20) + "func Long() {\n\tTarget()\n}\n\n" +
			"// Short is short.\nfunc Short() {\n\tTarget()\n}\n",
		"deco.py": "# Plain mentions Target.\ndef plain():\n    pass\n\n\n@Target\ndef wrapped():\n    pass\n",
	})
	opts := DefaultOptions()
	opts.MaxChunkBytes = 200
	b := &builder{in: Input{Head: head}, opts: opts, parser: &chunk.Parser{}, changedSymbols: []string{"Target"}}
	if err := scanParts(t.Context(), []*builder{b}, &b.stats); err != nil {
		t.Fatal(err)
	}
	got := map[string]Chunk{}
	for _, c := range b.callers["Target"].chunks {
		got[c.Symbol] = c
	}
	tests := []struct {
		symbol, start, end string
		line               int
	}{
		{symbol: "Long", start: "// padding", end: "}", line: 21},
		{symbol: "Short", start: "// Short is short.", end: "}", line: 31},
		{symbol: "wrapped", start: "@Target", end: "pass", line: 6},
	}
	for _, tt := range tests {
		c, ok := got[tt.symbol]
		if !ok {
			t.Errorf("no caller %s in %+v", tt.symbol, got)
			continue
		}
		if !strings.HasPrefix(c.Text, tt.start) || !strings.HasSuffix(c.Text, tt.end) || c.StartLine != tt.line ||
			strings.Count(c.Text, "\n") != c.EndLine-c.StartLine {
			t.Errorf("caller %s = L%d-%d %q, want it whole from line %d with %q", tt.symbol, c.StartLine, c.EndLine, c.Text, tt.line, tt.start)
		}
		delete(got, tt.symbol)
	}
	if len(got) > 0 {
		t.Errorf("callers %+v are only mentioned in comments", got)
	}
}

// TestUsersOfALongDecoratorLine: a decorator line of many identifiers and
// many hits finds each hit's identifier among them with one look, not by a
// scan of the line's identifiers for each hit.
func TestUsersOfALongDecoratorLine(t *testing.T) {
	const n = 60000
	names := make([]string, n)
	for i := range names {
		names[i] = fmt.Sprintf("i%05d", i)
	}
	pf := (&chunk.Parser{}).Parse("deco.py", []byte("@d("+strings.Join(names, ", ")+")\ndef f():\n    pass\n"))
	defer pf.Close()
	hits := make([]hit, n+1)
	for i := range n {
		hits[i] = hit{"wanted", 1}
	}
	hits[n] = hit{"i00007", 1}
	var lookedAt int
	looked = &lookedAt
	decls := users(pf, hits, DefaultOptions().MaxDeclLines)
	looked = nil
	if lookedAt > len(hits) {
		t.Errorf("users looked at %d identifiers for %d hits, want one a hit at most", lookedAt, len(hits))
	}
	if decls[0] != nil || decls[n] == nil || decls[n].Symbol != "f" {
		t.Fatalf("users = %v for a word the decorator does not name and %v for one it does, want nil and f", decls[0], decls[n])
	}
}

// TestUsersResolveALineOnce: many hits on one line look the line's
// declaration up once, rather than allocating a copy of it for every hit.
func TestUsersResolveALineOnce(t *testing.T) {
	const n = 1000
	pf := (&chunk.Parser{}).Parse("calls.go", []byte("package p\n\nfunc f() {\n\tg("+strings.Repeat("a, ", n)+")\n}\n"))
	defer pf.Close()
	hits := make([]hit, n)
	for i := range hits {
		hits[i] = hit{"a", 4}
	}
	var decls []*chunk.Decl
	allocs := testing.AllocsPerRun(10, func() { decls = users(pf, hits, DefaultOptions().MaxDeclLines) })
	if decls[0] == nil || decls[0].Symbol != "f" || decls[n-1] == nil || *decls[n-1] != *decls[0] {
		t.Fatalf("users = %v ... %v, want f for every hit", decls[0], decls[n-1])
	}
	if allocs > 10 {
		t.Errorf("users of %d hits on one line made %.0f allocations, want a few for the line", n, allocs)
	}
}

// TestOverlayTakesADeclarationItsCommentsPushOver: a change in a declaration
// that fits the line limit only without the comments above it is shown with
// the declaration from its head line, not with a window.
func TestOverlayTakesADeclarationItsCommentsPushOver(t *testing.T) {
	src := "package demo\n\n" + strings.Repeat("// F does things.\n", 15) + "func F() {\n" + strings.Repeat("\t_ = 0\n", 138) + "}\n"
	diff := "diff --git a/f.go b/f.go\n--- a/f.go\n+++ b/f.go\n@@ -100,1 +100,1 @@\n-\t_ = 1\n+\t_ = 0\n"
	b := &builder{
		in:   Input{Head: headTree(t, map[string]string{"f.go": src}), Diff: diff, Changed: []string{"f.go"}},
		opts: DefaultOptions(), parser: &chunk.Parser{},
	}
	if err := b.overlay(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(b.overlayChunks) != 1 {
		t.Fatalf("overlay = %+v, want one chunk", b.overlayChunks)
	}
	if c := b.overlayChunks[0]; c.Symbol != "F" || c.StartLine != 18 || c.EndLine != 157 || !strings.HasPrefix(c.Text, "func F() {") {
		t.Fatalf("overlay = %+v, want F from its head line 18 to 157", c)
	}
}

// TestOverlayKeepsTheHeadUnderACommentThatClosesOnIt: a block doc comment
// over MaxChunkBytes that closes on the line naming the function cannot go
// whole by lines. The overlay of a change in the body starts at the
// function's own text instead, so it keeps the signature and the change
// rather than the comment alone.
func TestOverlayKeepsTheHeadUnderACommentThatClosesOnIt(t *testing.T) {
	src := "package demo\n\n/*\n" + strings.Repeat(" * F does things, and does them at some length.\n", 140) + " */ func F() {\n\tchanged()\n}\n"
	if len(src) <= DefaultOptions().MaxChunkBytes {
		t.Fatalf("source is %d bytes, not over %d", len(src), DefaultOptions().MaxChunkBytes)
	}
	diff := "diff --git a/f.go b/f.go\n--- a/f.go\n+++ b/f.go\n@@ -145,1 +145,1 @@\n-\tunchanged()\n+\tchanged()\n"
	b := &builder{
		in:   Input{Head: headTree(t, map[string]string{"f.go": src}), Diff: diff, Changed: []string{"f.go"}},
		opts: DefaultOptions(), parser: &chunk.Parser{},
	}
	if err := b.overlay(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(b.overlayChunks) != 1 {
		t.Fatalf("overlay = %+v, want one chunk", b.overlayChunks)
	}
	if c := b.overlayChunks[0]; c.Symbol != "F" || c.StartLine != 144 || c.EndLine != 146 || c.Text != "func F() {\n\tchanged()\n}" {
		t.Fatalf("overlay = L%d-%d %q, want F's own text from line 144", c.StartLine, c.EndLine, textcut.Prefix(c.Text, 80))
	}
}

// TestOverlayWindowsAChangeTheDeclarationCutDrops: a change deep in a block
// doc comment over MaxChunkBytes, above a function that still fits the line
// limit with it, is past where the declaration's text is cut. The overlay is
// a window around the change instead, and covers only the window's lines.
func TestOverlayWindowsAChangeTheDeclarationCutDrops(t *testing.T) {
	doc := " * F does things, and does them at some length.\n"
	src := "package demo\n\n/*\n" + strings.Repeat(doc, 131) + " * CHANGED.\n" + strings.Repeat(doc, 8) + " */\nfunc F() {\n\t_ = 0\n}\n"
	diff := "diff --git a/f.go b/f.go\n--- a/f.go\n+++ b/f.go\n@@ -135,1 +135,1 @@\n-" + doc + "+ * CHANGED.\n"
	f := (&chunk.Parser{}).Parse("f.go", []byte(src))
	decl := f.Enclosing([]int{135}, DefaultOptions().MaxDeclLines)
	if decl == nil || decl.Symbol != "F" || decl.StartLine != 3 || f.Text(135, 135) != " * CHANGED." || len(f.Bytes(3, 134)) <= DefaultOptions().MaxChunkBytes {
		t.Fatalf("declaration = %+v, want F from line 3 with line 135 past MaxChunkBytes", decl)
	}
	f.Close()
	b := &builder{
		in:   Input{Head: headTree(t, map[string]string{"f.go": src}), Diff: diff, Changed: []string{"f.go"}},
		opts: DefaultOptions(), parser: &chunk.Parser{},
	}
	if err := b.overlay(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(b.overlayChunks) != 1 {
		t.Fatalf("overlay = %+v, want one chunk", b.overlayChunks)
	}
	c := b.overlayChunks[0]
	if c.Symbol != "F" || c.StartLine != 115 || c.EndLine != 147 || !strings.Contains(c.Text, " * CHANGED.\n") || !strings.HasSuffix(c.Text, "func F() {\n\t_ = 0\n}") {
		t.Fatalf("overlay = L%d-%d %q, want F's window around line 135, lines 115 to 147", c.StartLine, c.EndLine, textcut.Prefix(c.Text, 80))
	}
}

// TestOverlayWindowFitsMaxChunkBytes: a window whose lines are long is
// narrowed to MaxChunkBytes around the change, so the cut does not drop it.
func TestOverlayWindowFitsMaxChunkBytes(t *testing.T) {
	line := "key: " + strings.Repeat("v", 495) + "\n"
	src := strings.Repeat(line, 30) + "key: CHANGED\n" + strings.Repeat(line, 30)
	diff := "diff --git a/f.yaml b/f.yaml\n--- a/f.yaml\n+++ b/f.yaml\n@@ -31,1 +31,1 @@\n-key: old\n+key: CHANGED\n"
	b := &builder{
		in:   Input{Head: headTree(t, map[string]string{"f.yaml": src}), Diff: diff, Changed: []string{"f.yaml"}},
		opts: DefaultOptions(), parser: &chunk.Parser{},
	}
	if err := b.overlay(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(b.overlayChunks) != 1 {
		t.Fatalf("overlay = %+v, want one chunk", b.overlayChunks)
	}
	c := b.overlayChunks[0]
	if c.StartLine != 26 || c.EndLine != 36 || !strings.Contains(c.Text, "key: CHANGED") || len(c.Text) > DefaultOptions().MaxChunkBytes {
		t.Fatalf("overlay = L%d-%d, %d bytes, want lines 26 to 36 around line 31 within %d bytes", c.StartLine, c.EndLine, len(c.Text), DefaultOptions().MaxChunkBytes)
	}
}

// headTree commits files to an in-memory repository and returns the tree.
func headTree(t testing.TB, files map[string]string) *object.Tree {
	t.Helper()
	fs := memfs.New()
	r, err := git.Init(memory.NewStorage(), git.WithWorkTree(fs))
	if err != nil {
		t.Fatal(err)
	}
	gittest.Unsigned(t, r)
	wt, err := r.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	for name, src := range files {
		f, err := fs.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(src)); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := wt.Add(name); err != nil {
			t.Fatal(err)
		}
	}
	h, err := wt.Commit("head", &git.CommitOptions{Author: &object.Signature{Name: "t", Email: "t@t", When: time.Unix(1, 0)}})
	if err != nil {
		t.Fatal(err)
	}
	c, err := r.CommitObject(h)
	if err != nil {
		t.Fatal(err)
	}
	head, err := c.Tree()
	if err != nil {
		t.Fatal(err)
	}
	return head
}

// TestRejectedCandidatesCopyNoText: a match that the candidate cap turns
// away costs no copy of its declaration's text.
func TestRejectedCandidatesCopyNoText(t *testing.T) {
	src := "package demo\n\n// Big calls Target.\nfunc Big() {\n\tTarget()\n" + strings.Repeat("\t_ = \"padding padding padding\"\n", 400) + "}\n"
	f := (&chunk.Parser{}).Parse("big.go", []byte(src))
	defer f.Close()
	decl := f.Enclosing([]int{5}, DefaultOptions().MaxDeclLines+300)
	if decl == nil {
		t.Fatal("no declaration encloses line 5")
	}
	opts := DefaultOptions()
	opts.MaxChunkBytes = 200
	b := &builder{opts: opts, callers: map[string]*candidates{}}
	for range opts.CallersPerSymbol {
		b.addCandidate(f, decl, 5, "Target", true)
	}
	kept := len(b.callers["Target"].chunks)
	if allocs := testing.AllocsPerRun(100, func() { b.addCandidate(f, decl, 5, "Target", true) }); allocs > 0 {
		t.Errorf("a rejected candidate allocates %.0f times", allocs)
	}
	if got := len(b.callers["Target"].chunks); got != kept {
		t.Fatalf("kept %d candidates, then %d", kept, got)
	}
	if c := b.callers["Target"].chunks[0]; c.StartLine != 4 || !strings.HasPrefix(c.Text, "func Big() {\n\tTarget()") || !strings.HasSuffix(c.Text, "\n… (truncated)") {
		t.Errorf("candidate = L%d %q", c.StartLine, c.Text)
	}
}

// TestCandidateTrimsWholeLeadNodes: the comments and decorators above a
// declaration over MaxChunkBytes go first whole, never a line of one, so the
// candidate does not start inside a block comment or a decorator's arguments
// even when the cut of one line would bring it under the bound.
func TestCandidateTrimsWholeLeadNodes(t *testing.T) {
	for _, tt := range []struct {
		name, path, src string
		line            int
		want            string
	}{
		{
			name: "block comment", path: "big.go", line: 7,
			src:  "package demo\n\n/*\n * Big calls Target.\n */\nfunc Big() {\n\tTarget()\n}\n",
			want: "func Big() {",
		},
		{
			name: "decorator over lines", path: "big.py", line: 5,
			src:  "@app.route(\n    \"/big\",\n)\ndef big():\n    target()\n",
			want: "def big():",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := (&chunk.Parser{}).Parse(tt.path, []byte(tt.src))
			defer f.Close()
			decl := f.Enclosing([]int{tt.line}, DefaultOptions().MaxDeclLines)
			if decl == nil {
				t.Fatalf("no declaration encloses line %d", tt.line)
			}
			if decl.HeadLine <= decl.StartLine+1 {
				t.Fatalf("declaration %+v has no lead lines to trim", decl)
			}
			opts := DefaultOptions()
			// One byte under the declaration: the cut of its first line alone
			// would bring it under.
			opts.MaxChunkBytes = len(f.Bytes(decl.StartLine, decl.EndLine)) - 1
			b := &builder{opts: opts, callers: map[string]*candidates{}}
			b.addCandidate(f, decl, tt.line, "Target", true)
			c := b.callers["Target"].chunks[0]
			if c.StartLine != decl.HeadLine || !strings.HasPrefix(c.Text, tt.want) {
				t.Fatalf("candidate = L%d %q, want it from its head line %d", c.StartLine, c.Text, decl.HeadLine)
			}
		})
	}
}

// BenchmarkScanDocMentions scans a file whose declarations' doc comments
// name the wanted symbol, again and again, and never use it.
func BenchmarkScanDocMentions(b *testing.B) {
	for _, n := range []int{100, 1000} {
		var src strings.Builder
		src.WriteString("package demo\n")
		for i := range n {
			fmt.Fprintf(&src, "// F%d is like Target, Target and Target.\n// Target again.\nfunc F%d() {}\n", i, i)
		}
		head := headTree(b, map[string]string{"f.go": src.String()})
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				bd := &builder{in: Input{Head: head}, opts: DefaultOptions(), parser: &chunk.Parser{}, changedSymbols: []string{"Target"}}
				if err := scanParts(b.Context(), []*builder{bd}, &bd.stats); err != nil {
					b.Fatal(err)
				}
				if c := bd.callers["Target"]; c != nil {
					b.Fatalf("callers = %+v", c.chunks)
				}
			}
		})
	}
}

// TestOverlayShowsEveryChangePastTheCut: a declaration whose text is over
// MaxChunkBytes is cut, and its overlay covers only the lines the cut keeps
// whole, so a change past the cut, in the same run as one before it or in a
// run of its own, is still shown with lines around it.
func TestOverlayShowsEveryChangePastTheCut(t *testing.T) {
	src := []string{"package demo", ""}
	for range 10 {
		src = append(src, "// F does things.")
	}
	src = append(src, "func F() {")
	for len(src) < 139 {
		src = append(src, "\t_ = \"padding padding padding padding padding padding padding\"")
	}
	src = append(src, "}")
	for _, tt := range []struct {
		name    string
		changed []int
	}{
		{"a change in the comment and one in the body past the cut", []int{3, 120}},
		{"one run from the comment to past the cut", []int{3, 18, 33, 48, 63, 78, 93, 108, 123}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			lines := slices.Clone(src)
			var diff strings.Builder
			diff.WriteString("diff --git a/f.go b/f.go\n--- a/f.go\n+++ b/f.go\n")
			for _, l := range tt.changed {
				lines[l-1] = fmt.Sprintf("\tchanged%d()", l)
				if l <= 12 {
					lines[l-1] = fmt.Sprintf("// changed%d.", l)
				}
				fmt.Fprintf(&diff, "@@ -%d,1 +%d,1 @@\n-%s\n+%s\n", l, l, src[l-1], lines[l-1])
			}
			text := strings.Join(lines, "\n") + "\n"
			if n := len(strings.Join(lines[2:], "\n")); n <= DefaultOptions().MaxChunkBytes {
				t.Fatalf("declaration is %d bytes, not over %d", n, DefaultOptions().MaxChunkBytes)
			}
			b := &builder{
				in:   Input{Head: headTree(t, map[string]string{"f.go": text}), Diff: diff.String(), Changed: []string{"f.go"}},
				opts: DefaultOptions(), parser: &chunk.Parser{},
			}
			if err := b.overlay(t.Context()); err != nil {
				t.Fatal(err)
			}
			for _, c := range b.overlayChunks {
				if whole := strings.Join(lines[c.StartLine-1:c.EndLine], "\n"); !strings.HasPrefix(c.Text, whole) {
					t.Errorf("overlay L%d-%d does not hold its lines whole: %q", c.StartLine, c.EndLine, textcut.Prefix(c.Text, 80))
				}
			}
			for _, l := range tt.changed {
				if !slices.ContainsFunc(b.overlayChunks, func(c Chunk) bool {
					return c.StartLine <= l && l <= c.EndLine && c.StartLine < c.EndLine && strings.Contains(c.Text, lines[l-1]+"\n")
				}) {
					t.Errorf("no overlay chunk shows line %d with lines around it", l)
				}
			}
		})
	}
}
