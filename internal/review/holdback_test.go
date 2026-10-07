package review

import "testing"

func TestHoldBack(t *testing.T) {
	delta := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -10,3 +10,4 @@\n x\n+y\n z\n w\n" +
		"diff --git a/b.go b/b.go\n--- a/b.go\n+++ b/b.go\n@@ -20,3 +20,2 @@\n p\n-q\n r\n" +
		"diff --git a/img.png b/img.png\nBinary files a/img.png and b/img.png differ\n" +
		"diff --git a/old1.go b/new1.go\n--- a/old1.go\n+++ b/new1.go\n@@ -1,2 +1,2 @@\n a\n-b\n+B\n" +
		"diff --git a/old2.go b/new2.go\n--- a/old2.go\n+++ b/new2.go\n@@ -1,2 +1,2 @@\n a\n-b\n+B\n"
	paths := []string{"a.go", "b.go", "img.png", "cut.go", "new1.go", "new2.go"}
	prior := []Finding{
		{Path: "c.go", Line: 5, Title: "Old one"}, {Path: "d.go", Line: 3, Title: "fixed since"},
		{Path: "old1.go", Line: 40, Title: "moved along"}, {Path: "old2.go", Line: 40, Title: "gone with the move"},
	}
	reported := []Finding{{Path: "c.go", Line: 40, Title: "old  one"}, {Path: "new1.go", Line: 41, Title: "moved along"}}
	held := HoldBack(prior, reported, delta, paths)
	tests := []struct {
		name string
		f    Finding
		want bool
	}{
		{"three lines after an added line", Finding{Path: "a.go", Line: 14, Title: "n"}, false},
		{"four lines after an added line", Finding{Path: "a.go", Line: 15, Title: "n"}, true},
		{"three lines before an added line", Finding{Path: "a.go", Line: 8, Title: "n"}, false},
		{"four lines before an added line", Finding{Path: "a.go", Line: 7, Title: "n"}, true},
		{"a range ending near an added line", Finding{Path: "a.go", Line: 2, EndLine: 8, Title: "n"}, false},
		{"where lines were removed", Finding{Path: "b.go", Line: 24, Title: "n"}, false},
		{"past where lines were removed", Finding{Path: "b.go", Line: 25, Title: "n"}, true},
		{"made by the last review, moved", Finding{Path: "c.go", Line: 40, Title: "OLD ONE"}, false},
		{"in a file the push did not touch", Finding{Path: "e.go", Line: 1, Title: "n"}, true},
		{"beside an earlier finding not reported again", Finding{Path: "d.go", Line: 50, Title: "fixed, reworded"}, false},
		{"in a binary file", Finding{Path: "img.png", Line: 1, Title: "n"}, false},
		{"in a file cut from the delta", Finding{Path: "cut.go", Line: 1, Title: "n"}, false},
		{"made by the last review in a renamed file", Finding{Path: "new1.go", Line: 41, Title: "moved along"}, false},
		{"new in a renamed file", Finding{Path: "new1.go", Line: 30, Title: "n"}, true},
		{"in a renamed file whose earlier finding went unreported", Finding{Path: "new2.go", Line: 30, Title: "n"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := held(tt.f); got != tt.want {
				t.Fatalf("held(%s:%d) = %v, want %v", tt.f.Path, tt.f.Line, got, tt.want)
			}
		})
	}
}
