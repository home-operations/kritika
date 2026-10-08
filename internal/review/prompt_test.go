package review

import (
	"strings"
	"testing"
)

func TestShortSHA(t *testing.T) {
	if ShortSHA("0123456789") != "0123456" || ShortSHA("abc") != "abc" {
		t.Fatal("ShortSHA")
	}
}

func TestFitDiffLeavesOutWhatDoesNotFit(t *testing.T) {
	small := "diff --git a/a.go b/a.go\n+a\n"
	large := "diff --git a/gen.lock b/gen.lock\n" + strings.Repeat("+x\n", 100)
	last := "diff --git a/z.go b/z.go\n+z\n"
	diff := small + large + last
	if got, omitted := FitDiff(diff, len(diff)); got != diff || omitted != nil {
		t.Fatalf("a diff that fits was cut: %v omitted", omitted)
	}
	got, omitted := FitDiff(diff, len(small)+len(last)+8)
	if !strings.Contains(got, "a/a.go") || !strings.Contains(got, "a/z.go") || strings.Contains(got, "gen.lock") {
		t.Fatalf("kept = %q, want the two small files", got)
	}
	if len(omitted) != 1 || omitted[0] != "gen.lock" {
		t.Fatalf("omitted = %v, want gen.lock", omitted)
	}
}

// TestBuildMarksFileStatus: the changed-files list marks what the diff adds,
// deletes or renames, as go-git writes their headers, a file left out of
// the prompt included; a modified file stays unmarked.
func TestBuildMarksFileStatus(t *testing.T) {
	diff := "diff --git a/cmd/new.go b/cmd/new.go\nnew file mode 100644\nindex 0000000..1111111\n--- /dev/null\n+++ b/cmd/new.go\n" +
		"@@ -0,0 +1,2 @@\n+package cmd\n+" + strings.Repeat("x", 20_000) + "\n" +
		"diff --git a/logo.png b/logo.png\nnew file mode 100644\nindex 0000000..5555555\nBinary files /dev/null and b/logo.png differ\n" +
		"diff --git a/old.go b/old.go\ndeleted file mode 100644\nindex 2222222..0000000\n--- a/old.go\n+++ /dev/null\n" +
		"@@ -1 +0,0 @@\n-package old\n" +
		"diff --git a/a/before.go b/a/after.go\nrename from a/before.go\nrename to a/after.go\n" +
		"diff --git a/main.go b/main.go\nindex 3333333..4444444 100644\n--- a/main.go\n+++ b/main.go\n" +
		"@@ -1 +1 @@\n-package old\n+package main\n"
	in := Input{Repository: "a/b", Number: 1, Changed: []string{"a/after.go", "cmd/new.go", "logo.png", "main.go", "old.go"}, Diff: diff}
	in.BudgetTokens = 1000
	msg, omitted, _ := Build(in)
	if len(omitted) != 1 || omitted[0] != "cmd/new.go" {
		t.Fatalf("omitted = %v, want the large new file left out", omitted)
	}
	want := "Changed files (5):\n- a/after.go (renamed from a/before.go)\n- cmd/new.go (new)\n- logo.png (new)\n- main.go\n- old.go (deleted)\n"
	if !strings.Contains(msg, want) {
		t.Fatalf("message lacks\n%s\nin:\n%s", want, msg)
	}
}
