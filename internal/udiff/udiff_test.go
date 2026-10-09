package udiff

import (
	"strconv"
	"strings"
	"testing"
)

const twoFiles = "diff --git a/x.go b/x.go\n--- a/x.go\n+++ b/x.go\n@@ -3,4 +3,5 @@\n a\n-b\n+B\n+C\n d\n@@ -20,2 +21,2 @@\n-x\n+y\n z\n" +
	"diff --git a/gone b/gone\ndeleted file mode 100644\n--- a/gone\n+++ /dev/null\n@@ -1 +0,0 @@\n-old\n\\ No newline at end of file\n"

func TestParse(t *testing.T) {
	tests := []struct {
		name  string
		diff  string
		paths []string
		hunks []int
		// lines is one hunk's lines as "kind old new", the first hunk of
		// the first file unless hunk says otherwise.
		lines string
	}{
		{name: "two files, two hunks, a deletion with a no-newline note", diff: twoFiles, paths: []string{"x.go", "gone"}, hunks: []int{2, 1},
			lines: "  3 3,- 4 4,+ 5 4,+ 5 5,  5 6"},
		{name: "a hunk's lines are counted, so removed and added file headers stay its lines",
			diff:  "diff --git a/q.sql b/q.sql\n--- a/q.sql\n+++ b/q.sql\n@@ -1,3 +1,3 @@\n SELECT 1;\n--- old comment\n+++ new comment\n SELECT 2;\n",
			paths: []string{"q.sql"}, hunks: []int{1}, lines: "  1 1,- 2 2,+ 3 2,  3 3"},
		{name: "once the counts are spent, a file header opens a file",
			diff:  "--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b\n--- a/y\n+++ b/y\n@@ -1 +1 @@\n-c\n+d\n",
			paths: []string{"x", "y"}, hunks: []int{1, 1}, lines: "- 1 1,+ 2 1"},
		{name: "a hunk short of its counts ends at the next hunk or file",
			diff:  "diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -1,9 +1,9 @@\n a\n@@ -20,9 +20,9 @@\n b\ndiff --git a/y b/y\n--- a/y\n+++ b/y\n@@ -1 +1 @@\n-c\n+d\n",
			paths: []string{"x", "y"}, hunks: []int{2, 1}, lines: "  1 1"},
		{name: "a rename alone has no hunk and is named by its diff line",
			diff:  "diff --git a/old.go b/moved.go\nsimilarity index 100%\nrename from old.go\nrename to moved.go\n",
			paths: []string{"moved.go"}, hunks: []int{0}},
		{name: "a new file counts from nothing", diff: "diff --git a/n b/n\nnew file mode 100644\n--- /dev/null\n+++ b/n\n@@ -0,0 +1,2 @@\n+p\n+q\n",
			paths: []string{"n"}, hunks: []int{1}, lines: "+ 0 1,+ 0 2"},
		{name: "an empty line in a hunk is a context line stripped of its space",
			diff:  "diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -1,2 +1,2 @@\n\n-a\n+b\n",
			paths: []string{"x"}, hunks: []int{1}, lines: "  1 1,- 2 2,+ 3 2"},
		{name: "what comes before the first diff line is a file of its own", diff: "stray\ndiff --git a/x b/x\n+x\n",
			paths: []string{"", "x"}, hunks: []int{0, 0}},
		{name: "a path keeps a space at its end, and not the tab git ends a name with a space with",
			diff:  "diff --git a/x.go  b/x.go \n--- a/x.go \n+++ b/x.go \n" + "diff --git a/x y.go b/x y.go\n--- a/x y.go\t\n+++ b/x y.go\t\n",
			paths: []string{"x.go ", "x y.go"}, hunks: []int{0, 0}},
		{name: "nothing", diff: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files := Parse(tt.diff)
			var back strings.Builder
			var paths []string
			var hunks []int
			for _, f := range files {
				back.WriteString(f.String())
				paths = append(paths, f.Path())
				hunks = append(hunks, len(f.Hunks))
			}
			if back.String() != tt.diff {
				t.Errorf("String() =\n%q\nwant\n%q", back.String(), tt.diff)
			}
			if strings.Join(paths, ",") != strings.Join(tt.paths, ",") || !equalInts(hunks, tt.hunks) {
				t.Errorf("paths %q with %v hunks, want %q with %v", paths, hunks, tt.paths, tt.hunks)
			}
			if tt.lines == "" {
				return
			}
			var lines []string
			for _, l := range files[0].Hunks[0].Lines {
				lines = append(lines, string(l.Kind)+" "+strconv.Itoa(l.OldLine)+" "+strconv.Itoa(l.NewLine))
			}
			if got := strings.Join(lines, ","); got != tt.lines {
				t.Errorf("lines = %q, want %q", got, tt.lines)
			}
		})
	}
}

func TestParseReadsHeaders(t *testing.T) {
	files := Parse(twoFiles)
	if len(files) != 2 {
		t.Fatalf("files = %d", len(files))
	}
	x, gone := files[0], files[1]
	if x.OldPath != "x.go" || x.NewPath != "x.go" || x.Header != "diff --git a/x.go b/x.go\n--- a/x.go\n+++ b/x.go\n" {
		t.Errorf("x = %+v", x)
	}
	if h := x.Hunks[1]; h.Header != "@@ -20,2 +21,2 @@\n" || h.OldStart != 20 || h.OldCount != 2 || h.NewStart != 21 || h.NewCount != 2 {
		t.Errorf("hunk = %+v", h)
	}
	if gone.OldPath != "gone" || gone.NewPath != "" || gone.Path() != "gone" || !strings.Contains(gone.Header, "deleted file mode") {
		t.Errorf("gone = %+v", gone)
	}
	if l := gone.Hunks[0].Lines[1]; l.Kind != '\\' || l.Text != " No newline at end of file" || l.String() != "\\ No newline at end of file\n" {
		t.Errorf("note = %+v", l)
	}
	if l := gone.Hunks[0].Lines[0]; l.Text != "old" || l.String() != "-old\n" {
		t.Errorf("line = %+v", l)
	}
}

func TestParseKeepsALastLineWithoutNewline(t *testing.T) {
	diff := "diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b"
	files := Parse(diff)
	if len(files) != 1 || files[0].String() != diff || files[0].Hunks[0].Lines[1].Text != "b" {
		t.Fatalf("files = %+v", files)
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
