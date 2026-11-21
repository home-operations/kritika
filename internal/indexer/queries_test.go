package indexer

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/home-operations/kritika/internal/chunk"
	"github.com/home-operations/kritika/internal/contextpack"
	"github.com/home-operations/kritika/internal/udiff"
)

// fileDiff is one file's diff of hunks, each given as its "@@" line and
// its lines.
func fileDiff(oldPath, newPath string, hunks ...string) string {
	from, to := "a/"+oldPath, "b/"+newPath
	if oldPath == "" {
		from = "/dev/null"
	}
	if newPath == "" {
		to = "/dev/null"
	}
	name := cmp.Or(newPath, oldPath)
	return "diff --git a/" + name + " b/" + name + "\n--- " + from + "\n+++ " + to + "\n" + strings.Join(hunks, "")
}

// added is a hunk of a new file of lines.
func added(lines ...string) string {
	return fmt.Sprintf("@@ -0,0 +1,%d @@\n+", len(lines)) + strings.Join(lines, "\n+") + "\n"
}

func TestHunkQueries(t *testing.T) {
	license := strings.Repeat("// Licensed under the terms that are long enough to fill a query on their own.\n", 50)
	newFile := added(append(strings.Split(strings.TrimSuffix(license, "\n"), "\n"),
		"package lib", "", "import \"fmt\"", "", "func A() {", "\tfmt.Println(\"a\")", "}", "", "func B() {}")...)
	long := "\t// " + strings.Repeat("x", 1500)
	indented := strings.Repeat(" ", contextpack.SimilarQueryChars) + "deep()"
	filler := make([]string, 0, 200)
	for i := range 200 {
		filler = append(filler, fmt.Sprintf("line %d of a text with no declarations to cut it by", i))
	}
	tests := []struct {
		name   string
		diff   string
		ignore []string
		want   []string
		paths  []string
	}{
		{
			name:  "the changed region as the head has it",
			diff:  fileDiff("main.go", "main.go", "@@ -1,1 +1,3 @@\n package main\n+\n+func b() {}\n"),
			want:  []string{"package main\n\nfunc b() {}\n"},
			paths: []string{"main.go"},
		},
		{
			name:  "a little context around the changed lines",
			diff:  fileDiff("a.go", "a.go", "@@ -1,9 +1,9 @@\n c1\n c2\n c3\n c4\n c5\n-old\n+new\n c6\n c7\n"),
			want:  []string{"c3\nc4\nc5\nnew\nc6\nc7\n"},
			paths: []string{"a.go"},
		},
		{
			name: "ignored paths and paths the index holds no chunk of",
			diff: fileDiff("vendor/x.go", "vendor/x.go", added("func v() {}")) + fileDiff("", "go.sum", added("example.com/m v1.0.0 h1:x")) +
				fileDiff("", "a.go", added("func a() {}")),
			ignore: []string{"vendor/**"}, want: []string{"func a() {}\n"}, paths: []string{"a.go"},
		},
		{
			name: "encrypted and generated files the index leaves out",
			diff: fileDiff("", "secrets.sops.yaml", added("password: ENC[AES256_GCM,data:x]")) +
				fileDiff("", "api/v1/api_pb2.py", added("DESCRIPTOR = _descriptor.FileDescriptor()")) +
				fileDiff("", "a.go", added("func a() {}")),
			want: []string{"func a() {}\n"}, paths: []string{"a.go"},
		},
		{
			name: "a hunk that adds nothing and a deleted file",
			diff: fileDiff("a.go", "a.go", "@@ -1,3 +1,2 @@\n x\n-y\n z\n", "@@ -9,2 +8,3 @@\n p\n+\n q\n") +
				fileDiff("gone.go", "", "@@ -1,1 +0,0 @@\n-func g() {}\n"),
		},
		{
			name: "files in turn, the most changed first",
			diff: fileDiff("a.go", "a.go", "@@ -1,1 +1,2 @@\n a\n+a1\n", "@@ -20,1 +21,2 @@\n a\n+a2\n", "@@ -40,1 +42,2 @@\n a\n+a3\n") +
				fileDiff("b.go", "b.go", "@@ -1,1 +1,5 @@\n b\n+b1\n+b2\n+b3\n+b4\n"),
			want:  []string{"b\nb1\nb2\nb3\nb4\n", "a\na1\n", "a\na2\n", "a\na3\n"},
			paths: []string{"b.go", "a.go", "a.go", "a.go"},
		},
		{
			name:  "a large new file by its declarations, not its license",
			diff:  fileDiff("", "lib.go", newFile),
			want:  []string{"func A() {\n\tfmt.Println(\"a\")\n}\n", "func B() {}\n"},
			paths: []string{"lib.go", "lib.go"},
		},
		{
			name: "a large new script by its declarations and then its top-level statements",
			diff: fileDiff("", "run.py", added(append(strings.Split(strings.ReplaceAll(strings.TrimSuffix(license, "\n"), "//", "#"), "\n"),
				"import sys", "", "def helper(argv):", "    return argv[1:]", "", "", "# Run it.", "if __name__ == \"__main__\":", "    print(helper(sys.argv))")...)),
			want:  []string{"def helper(argv):\n    return argv[1:]\n", "if __name__ == \"__main__\":\n    print(helper(sys.argv))\n"},
			paths: []string{"run.py", "run.py"},
		},
		{
			name:  "a change below the first bound's worth of a declaration",
			diff:  fileDiff("a.go", "a.go", "@@ -1,4 +1,5 @@\n func A() {\n "+long+"\n "+long+"\n+\tchanged()\n }\n"),
			want:  []string{long + "\n\tchanged()\n}\n"},
			paths: []string{"a.go"},
		},
		{
			name:  "a line indented past the bound, by its code",
			diff:  fileDiff("", "deep.md", added(indented)) + fileDiff("", "a.go", added("func a() {}")),
			want:  []string{"deep()", "func a() {}\n"},
			paths: []string{"deep.md", "a.go"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, paths := HunkQueries(tt.diff, tt.ignore, nil)
			if !slices.Equal(got, tt.want) || !slices.Equal(paths, tt.paths) {
				t.Fatalf("HunkQueries = %q of %q, want %q of %q", got, paths, tt.want, tt.paths)
			}
		})
	}
	t.Run("a large region without declarations in windows of its changed lines", func(t *testing.T) {
		got, _ := HunkQueries(fileDiff("", "notes.md", added(filler...)), nil, nil)
		if len(got) < 2 || !strings.HasPrefix(got[0], "line 0 ") || !strings.HasPrefix(got[1], "line ") {
			t.Fatalf("windows = %q", got)
		}
		for _, q := range got {
			if len(q) > contextpack.SimilarQueryChars {
				t.Fatalf("a window of %d characters", len(q))
			}
		}
	})
	// A declaration that gives way to the nested ones holding a change still
	// has its own changed lines asked about, after them.
	fields := lines(60, "  private f%02d = \""+strings.Repeat("x", 40)+"\";")
	body := lines(60, "    x%02d = \""+strings.Repeat("x", 40)+"\"")
	for _, tt := range []struct {
		name, diff string
		nested     string
		outside    string
	}{
		{
			name: "a changed field beside a changed method",
			diff: fileDiff("store.ts", "store.ts", "@@ -1,64 +1,66 @@\n export class Store {\n+  private changed = 1;\n"+unchanged(fields)+
				"   get(): number {\n+    return 2;\n   }\n }\n"),
			nested:  "  get(): number {\n    return 2;\n  }\n",
			outside: "export class Store {\n  private changed = 1;\n  private f00 = ",
		},
		{
			name: "a changed statement beside a changed nested function",
			diff: fileDiff("outer.py", "outer.py", "@@ -1,64 +1,66 @@\n def outer():\n+    setup = 1\n"+unchanged(body)+
				"     def inner():\n+        return 2\n     return inner\n"),
			nested:  "    def inner():\n        return 2\n",
			outside: "def outer():\n    setup = 1\n    x00 = ",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := HunkQueries(tt.diff, nil, nil)
			if len(got) != 2 || got[0] != tt.nested || !strings.HasPrefix(got[1], tt.outside) || len(got[1]) > contextpack.SimilarQueryChars {
				t.Fatalf("HunkQueries = %q, want %q and then a window from %q", got, tt.nested, tt.outside)
			}
		})
	}
	// Declarations that share a line: a sibling that starts on the line the
	// last one asked about ends on is asked about for its own changes, and a
	// declaration nested from its parent's first line takes the parent's
	// place as one nested further down does.
	jsBody := strings.Split(strings.TrimSuffix(lines(40, "  const v%02d = \""+strings.Repeat("x", 40)+"\";"), "\n"), "\n")
	jsFields := lines(70, "  f%02d = \""+strings.Repeat("x", 40)+"\";")
	for _, tt := range []struct {
		name, diff string
		want       func([]string) bool
	}{
		{
			name: "siblings on a shared line, both changed",
			diff: fileDiff("", "two.js", added(slices.Concat([]string{"function first() {"}, jsBody,
				[]string{"} function next() {"}, jsBody, []string{"}"})...)),
			want: func(got []string) bool {
				return len(got) == 2 && strings.HasPrefix(got[0], "function first() {") && strings.HasSuffix(got[0], "} function next() {\n") &&
					strings.HasPrefix(got[1], "} function next() {") && strings.HasSuffix(got[1], "\n}\n")
			},
		},
		{
			name: "a method nested from its class's first line",
			diff: fileDiff("big.js", "big.js", "@@ -1,72 +1,72 @@\n-class Big { small() { return 0; }\n+class Big { small() { return 1; }\n"+
				unchanged(jsFields)+"-}\n+};\n"),
			want: func(got []string) bool {
				return len(got) == 2 && got[0] == "class Big { small() { return 1; }\n" && strings.HasSuffix(got[1], "\n};\n")
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := HunkQueries(tt.diff, nil, nil)
			if !tt.want(got) {
				t.Fatalf("HunkQueries = %d queries %q", len(got), got)
			}
		})
	}
	t.Run("bounded", func(t *testing.T) {
		var many strings.Builder
		for i := range contextpack.SimilarQueries + 3 {
			many.WriteString(fileDiff("", fmt.Sprintf("f%d.go", i), added(fmt.Sprintf("func f%d() {}", i))))
		}
		if got, paths := HunkQueries(many.String(), nil, nil); len(got) != contextpack.SimilarQueries || len(paths) != contextpack.SimilarQueries {
			t.Fatalf("%d queries of %d paths, want %d", len(got), len(paths), contextpack.SimilarQueries)
		}
		if got, _ := HunkQueries("", nil, nil); len(got) != 0 {
			t.Fatalf("queries of an empty diff = %q", got)
		}
	})
}

// TestHunkQueriesLeadingComments: a comment block that leads straight into
// a declaration is part of it. When the declaration's own lines are changed
// too, they are asked about first, so a block long enough to fill every
// query does not crowd them out; when only the comment is, it is still asked
// about.
func TestHunkQueriesLeadingComments(t *testing.T) {
	header := strings.Split(strings.TrimSuffix(strings.Repeat("# Licensed under the terms that are long enough to fill a query on their own.\n", 500), "\n"), "\n")
	t.Run("a declaration under a comment block that fills every query", func(t *testing.T) {
		got, _ := HunkQueries(fileDiff("", "run.py", added(append(header, "def first(x):", "    return x")...)), nil, nil)
		if len(got) != contextpack.SimilarQueries || got[0] != "def first(x):\n    return x\n" || !strings.HasPrefix(got[1], "# Licensed") {
			t.Fatalf("HunkQueries = %d queries, first %q, want %d, def first(x) and then the comment", len(got), got[:min(len(got), 2)], contextpack.SimilarQueries)
		}
	})
	t.Run("a changed comment block over an unchanged declaration", func(t *testing.T) {
		got, _ := HunkQueries(fileDiff("run.py", "run.py", "@@ -1,2 +1,502 @@\n+"+strings.Join(header, "\n+")+"\n def first(x):\n     return x\n"), nil, nil)
		if len(got) != contextpack.SimilarQueries || !strings.HasPrefix(got[0], "# Licensed") {
			t.Fatalf("HunkQueries = %d queries, first %q, want %d of the comment", len(got), got[:min(len(got), 1)], contextpack.SimilarQueries)
		}
	})
}

// manyDeclarations is a new Go file's diff of 10,000 short declarations and
// a string of 200,000 lines, the declarations first or last.
func manyDeclarations(declarationsFirst bool) string {
	blob := make([]string, 0, 200_002)
	blob = append(blob, "var s = `")
	for range 200_000 {
		blob = append(blob, "x")
	}
	blob = append(blob, "`")
	funcs := make([]string, 0, 10_000)
	for i := range 10_000 {
		funcs = append(funcs, fmt.Sprintf("func f%d() {}", i))
	}
	parts := [][]string{{"package a"}, funcs, blob}
	if !declarationsFirst {
		parts[1], parts[2] = blob, funcs
	}
	return fileDiff("", "a.go", added(slices.Concat(parts...)...))
}

// TestHunkQueriesLeaveOutImports: a large new file is asked about by its
// declarations, and its imports, includes and using directives take no
// query, the spare ones included, in the languages whose node types do not
// call them imports too.
func TestHunkQueriesLeaveOutImports(t *testing.T) {
	tests := []struct {
		name, path, comment string
		head, imports       []string
		decl                string
	}{
		{name: "c includes", path: "a.c", comment: "// %s", imports: []string{"#include <stdio.h>", "#include \"a.h\""}, decl: "int f(void) { return 0; }\n"},
		{
			name: "c++ includes and using directives", path: "a.cpp", comment: "// %s",
			imports: []string{"#include <vector>", "using namespace std;", "using std::vector;"}, decl: "int f() { return 0; }\n",
		},
		{name: "c# using directives", path: "a.cs", comment: "// %s", imports: []string{"using System;", "using static System.Math;"}, decl: "class A {}\n"},
		{name: "rust extern crate and use", path: "a.rs", comment: "// %s", imports: []string{"extern crate serde;", "use std::io;"}, decl: "fn f() {}\n"},
		{name: "java imports", path: "A.java", comment: "// %s", imports: []string{"package a;", "import java.util.List;"}, decl: "class A {}\n"},
		{name: "kotlin imports", path: "a.kt", comment: "// %s", imports: []string{"package a", "import x.Y"}, decl: "fun f() {}\n"},
		{
			name: "php use and require", path: "a.php", comment: "// %s", head: []string{"<?php"},
			imports: []string{"use A\\B;", "require_once 'x.php';"}, decl: "function f() {}\n",
		},
		{name: "ruby requires", path: "a.rb", comment: "# %s", imports: []string{"require 'json'", "require_relative 'x'"}, decl: "def f\n  1\nend\n"},
		{name: "swift imports", path: "a.swift", comment: "// %s", imports: []string{"import Foundation"}, decl: "func f() {}\n"},
		{name: "scala imports", path: "a.scala", comment: "// %s", imports: []string{"package a", "import x.Y"}, decl: "def f = 1\n"},
		{name: "julia using", path: "a.jl", comment: "# %s", imports: []string{"using LinearAlgebra"}, decl: "function f()\n  1\nend\n"},
		{
			name: "erlang includes", path: "a.erl", comment: "%% %s",
			imports: []string{"-include(\"x.hrl\").", "-include_lib(\"k/include/y.hrl\")."}, decl: "f() -> 1.\n",
		},
		{name: "ocaml open", path: "a.ml", comment: "(* %s *)", imports: []string{"open Printf"}, decl: "let f x = x + 1\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lines := slices.Clone(tt.head)
			for range 50 {
				lines = append(lines, fmt.Sprintf(tt.comment, "Licensed under the terms that are long enough to fill a query on their own."))
			}
			lines = slices.Concat(lines, tt.imports, []string{""}, strings.Split(strings.TrimSuffix(tt.decl, "\n"), "\n"))
			got, _ := HunkQueries(fileDiff("", tt.path, added(lines...)), nil, nil)
			if len(got) == 0 || got[0] != tt.decl {
				t.Fatalf("HunkQueries = %q, want %q first", got, tt.decl)
			}
			for _, q := range got {
				for _, imp := range tt.imports {
					if strings.Contains(q, imp) {
						t.Errorf("query %q holds the import %q", q, imp)
					}
				}
			}
		})
	}
}

// TestHunkQueriesManyDeclarations: a region of many short declarations and
// a large one is asked about by the declarations in the order it has them,
// the large one in windows of its lines. BenchmarkHunkQueriesManyDeclarations
// compares what each order costs.
func TestHunkQueriesManyDeclarations(t *testing.T) {
	first, _ := HunkQueries(manyDeclarations(true), nil, nil)
	want := make([]string, contextpack.SimilarQueries)
	for i := range want {
		want[i] = fmt.Sprintf("func f%d() {}\n", i)
	}
	if !slices.Equal(first, want) {
		t.Fatalf("HunkQueries with the declarations first = %q, want %q", first, want)
	}
	last, _ := HunkQueries(manyDeclarations(false), nil, nil)
	if len(last) != contextpack.SimilarQueries || !strings.HasPrefix(last[0], "var s = `\nx\n") {
		t.Fatalf("HunkQueries with the declarations last = %d queries, the first %.20q, want %d from the string's start",
			len(last), last[0], contextpack.SimilarQueries)
	}
	for _, q := range last[1:] {
		if strings.Trim(q, "x\n") != "" || len(q) > contextpack.SimilarQueryChars {
			t.Fatalf("HunkQueries with the declarations last = %.20q, want windows of the string", q)
		}
	}
}

// BenchmarkHunkQueriesManyDeclarations: finding the declarations that hold an
// added line costs about as much for many short declarations after many
// added lines as before them, where it is little next to the parse.
func BenchmarkHunkQueriesManyDeclarations(b *testing.B) {
	for _, bc := range []struct {
		name  string
		first bool
	}{{"declarations first", true}, {"declarations last", false}} {
		b.Run(bc.name, func(b *testing.B) {
			diff := manyDeclarations(bc.first)
			b.ReportAllocs()
			for b.Loop() {
				HunkQueries(diff, nil, nil)
			}
		})
	}
}

// unchanged is text's lines as the context lines of a hunk.
func unchanged(text string) string {
	return " " + strings.ReplaceAll(strings.TrimSuffix(text, "\n"), "\n", "\n ") + "\n"
}

// TestRegionQueriesMarksNestedDeclarationsOnce: an added line in a long body
// inside many nested declarations over the bound asks about the body. The
// lines of the declarations that gave way to it are marked in one pass, not
// once for each declaration around the body.
func TestRegionQueriesMarksNestedDeclarationsOnce(t *testing.T) {
	const depth, body = 200, 2000
	region := make([]udiff.Line, 0, 2*depth+body)
	for i := range depth {
		region = append(region, udiff.Line{Kind: ' ', Text: fmt.Sprintf("function outer%d() {", i)})
	}
	for i := range body {
		region = append(region, udiff.Line{Kind: '+', Text: fmt.Sprintf("  const v%d = %d;", i, i)})
	}
	for range depth {
		region = append(region, udiff.Line{Kind: ' ', Text: "}"})
	}
	var n int
	marked = &n
	defer func() { marked = nil }()
	qs := regionQueries(&chunk.Parser{}, "nested.js", region, 3)
	if len(qs) != 3 || !strings.HasPrefix(qs[0], fmt.Sprintf("function outer%d() {\n  const v0 = 0;", depth-1)) {
		t.Fatalf("regionQueries asked %d queries, the first %.40q, want 3 of the innermost body", len(qs), qs)
	}
	if n > 2*len(region) {
		t.Errorf("regionQueries marked %d lines of a %d-line region, over %d", n, len(region), 2*len(region))
	}
}

// TestHunkQueriesLeavesOutWhatIsNotIndexable: files that Build skips for
// their size take no turn, however many lines they add, so a smaller file
// that adds fewer still gets a query. Indexable refuses a file at head over
// MaxFileBytes and no other.
func TestHunkQueriesLeavesOutWhatIsNotIndexable(t *testing.T) {
	var diff strings.Builder
	big := make([]string, 0, contextpack.SimilarQueries)
	for i := range contextpack.SimilarQueries {
		big = append(big, fmt.Sprintf("big%d.go", i))
		diff.WriteString(fileDiff("", big[i], added(fmt.Sprintf("func big%d() {", i), "\treturn", "}")))
	}
	diff.WriteString(fileDiff("", "small.go", added("func small() {}")))
	got, paths := HunkQueries(diff.String(), nil, func(path string) bool { return !slices.Contains(big, path) })
	if !slices.Equal(got, []string{"func small() {}\n"}) || !slices.Equal(paths, []string{"small.go"}) {
		t.Fatalf("HunkQueries = %q of %q, want small.go's alone", got, paths)
	}
	head := oneFileTree(t, "big.go", strings.Repeat("x", DefaultOptions.MaxFileBytes+1))
	if indexable := Indexable(head, []string{"big.go", "gone.go"}); indexable("big.go") || !indexable("gone.go") {
		t.Fatalf("Indexable(big.go) = %t, Indexable(gone.go) = %t; want false, true", indexable("big.go"), indexable("gone.go"))
	}
	head = oneFileTree(t, "fits.go", strings.Repeat("x", DefaultOptions.MaxFileBytes))
	if !Indexable(head, []string{"fits.go"})("fits.go") || !Indexable(nil, []string{"fits.go"})("fits.go") {
		t.Fatal("Indexable refused a file within MaxFileBytes, or a path with no head")
	}
}
