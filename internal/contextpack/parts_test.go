package contextpack

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-billy/v6/memfs"
	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/go-git/go-git/v6/storage/memory"

	"github.com/home-operations/kritika/internal/gittest"
)

func partsTree(t testing.TB, files int) (*object.Tree, []Part) {
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
	write := func(path, text string) {
		t.Helper()
		f, err := fs.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(text)); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := wt.Add(path); err != nil {
			t.Fatal(err)
		}
	}
	parts := make([]Part, 8)
	for i := range parts {
		path := fmt.Sprintf("target%d.go", i)
		write(path, fmt.Sprintf("package demo\nfunc Target%d() {\n// context\nHelper%d()\nShared()\n}\n", i, i))
		parts[i] = Part{Changed: []string{path}, Diff: fmt.Sprintf(
			"diff --git a/%s b/%s\n--- a/%s\n+++ b/%s\n@@ -4,2 +4,2 @@\n-Old()\n+Helper%d()\n Shared()\n", path, path, path, path, i)}
	}
	var helpers strings.Builder
	helpers.WriteString("package demo\nfunc Shared() {}\n")
	for i := range parts {
		fmt.Fprintf(&helpers, "func Helper%d() {}\n", i)
	}
	write("helpers.go", helpers.String())
	for i := range files {
		var src strings.Builder
		src.WriteString("package demo\n")
		for j := range 12 {
			fmt.Fprintf(&src, "func Caller%d() {\n", j)
			for k := range parts {
				fmt.Fprintf(&src, "Target%d()\n", k)
			}
			src.WriteString("}\n")
		}
		write(fmt.Sprintf("caller%03d.go", i), src.String())
	}
	h, err := wt.Commit("head", &git.CommitOptions{Author: &object.Signature{Name: "t", Email: "t@t", When: time.Unix(1, 0)}})
	if err != nil {
		t.Fatal(err)
	}
	commit, err := r.CommitObject(h)
	if err != nil {
		t.Fatal(err)
	}
	head, err := commit.Tree()
	if err != nil {
		t.Fatal(err)
	}
	return head, parts
}

func TestBuildParts(t *testing.T) {
	head, parts := partsTree(t, 12)
	for _, tt := range []struct {
		name   string
		ignore []string
		adjust func(*Options)
	}{
		{name: "independent selections"},
		{name: "ignored files", ignore: []string{"caller*", "target2.go"}},
		{name: "file scan budget", adjust: func(o *Options) { o.MaxScanFiles = 3 }},
		{name: "byte scan budget", adjust: func(o *Options) { o.MaxScanBytes = 100 }},
		{name: "output budget", adjust: func(o *Options) { o.MaxPackBytes = 100 }},
		{name: "chunk budget", adjust: func(o *Options) { o.MaxChunkBytes = 20 }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			opts := DefaultOptions
			if tt.adjust != nil {
				tt.adjust(&opts)
			}
			got, stats, err := BuildParts(t.Context(), head, nil, tt.ignore, parts, opts)
			if err != nil {
				t.Fatal(err)
			}
			var scanned, parsed, bytes int
			for i, part := range parts {
				want, single, err := Build(t.Context(), Input{Head: head, Diff: part.Diff, Changed: part.Changed, Ignore: tt.ignore}, opts)
				if err != nil || !reflect.DeepEqual(got[i], want) {
					t.Fatalf("part %d = %+v, want %+v; error %v", i, got[i], want, err)
				}
				scanned += single.FilesScanned
				parsed += single.FilesParsed
				bytes += single.BytesScanned
			}
			if stats.FilesScanned >= scanned || stats.FilesParsed >= parsed || stats.BytesScanned >= bytes {
				t.Fatalf("shared work %+v; separate scans = %d files, %d parses, %d bytes", stats, scanned, parsed, bytes)
			}
			if tt.name == "independent selections" {
				if stats.FilesScanned != 21 || stats.FilesParsed != 21 || stats.Definitions != 8 || stats.Callers != 40 {
					t.Fatalf("shared stats = %+v", stats)
				}
				for i, cs := range got {
					for _, c := range cs {
						if c.Stage == StageDefinition && c.Symbol != fmt.Sprintf("Helper%d", i) {
							t.Fatalf("part %d received another part's definition: %+v", i, c)
						}
					}
				}
			}
		})
	}
}

func TestBuildPartsSharedSymbol(t *testing.T) {
	head, base, diff := repo(t)
	parts := []Part{
		{Diff: diff, Changed: []string{"widget.go"}},
		{Diff: "diff --git a/use.go b/use.go\n--- a/use.go\n+++ b/use.go\n@@ -10 +10 @@\n-Build(\"old\")\n+Build(\"new\")\n", Changed: []string{"use.go"}},
		{},
	}
	got, _, err := BuildParts(t.Context(), head, base, nil, parts, Options{})
	if err != nil {
		t.Fatal(err)
	}
	for i, part := range parts {
		want, _, err := Build(t.Context(), Input{Head: head, Base: base, Diff: part.Diff, Changed: part.Changed}, Options{})
		if err != nil || !reflect.DeepEqual(got[i], want) {
			t.Fatalf("part %d = %+v, want %+v; error %v", i, got[i], want, err)
		}
	}
	var defined bool
	for _, c := range got[1] {
		defined = defined || c.Stage == StageDefinition && c.Symbol == "Build"
	}
	if !defined {
		t.Fatalf("Build must be a definition for part 2 even though part 1 changes it: %+v", got[1])
	}
}

func TestBuildPartsEmptyAndCanceled(t *testing.T) {
	head, parts := partsTree(t, 1)
	for _, tt := range []struct {
		name  string
		head  *object.Tree
		parts []Part
	}{
		{name: "no parts", head: head},
		{name: "no head", parts: parts},
		{name: "no identifiers", head: head, parts: []Part{{}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			out, stats, err := BuildParts(t.Context(), tt.head, nil, nil, tt.parts, Options{})
			if err != nil || len(out) != len(tt.parts) || stats.FilesScanned != 0 {
				t.Fatalf("BuildParts = %+v, %+v, %v", out, stats, err)
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := BuildParts(ctx, head, nil, nil, parts, Options{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled build: %v", err)
	}
}

func BenchmarkBuildParts(b *testing.B) {
	head, parts := partsTree(b, 200)
	for _, n := range []int{1, 4, 8} {
		b.Run(fmt.Sprintf("parts=%d", n), func(b *testing.B) {
			b.Run("separate", func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					for _, p := range parts[:n] {
						if _, _, err := Build(b.Context(), Input{Head: head, Diff: p.Diff, Changed: p.Changed}, DefaultOptions); err != nil {
							b.Fatal(err)
						}
					}
				}
			})
			b.Run("shared", func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					if _, _, err := BuildParts(b.Context(), head, nil, nil, parts[:n], DefaultOptions); err != nil {
						b.Fatal(err)
					}
				}
			})
		})
	}
}
