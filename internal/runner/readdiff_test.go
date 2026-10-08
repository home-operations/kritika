package runner

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

const readDiffSample = "diff --git a/main.go b/main.go\n" +
	"index 1111111..2222222 100644\n" +
	"--- a/main.go\n" +
	"+++ b/main.go\n" +
	"@@ -1,3 +1,3 @@\n" +
	" package main\n" +
	"-var x = 1\n" +
	"+var x = 2\n" +
	" var y = 3\n" +
	"@@ -10,2 +10,3 @@ func f() {\n" +
	" \ta()\n" +
	"+\tb()\n" +
	" }\n" +
	"\\ No newline at end of file\n" +
	"diff --git a/new.go b/new.go\n" +
	"new file mode 100644\n" +
	"index 0000000..3333333\n" +
	"--- /dev/null\n" +
	"+++ b/new.go\n" +
	"@@ -0,0 +1,2 @@\n" +
	"+package main\n" +
	"+var z = 4\n"

// numberedMain is main.go's section of readDiffSample as read_diff shows it.
const numberedMain = "diff --git a/main.go b/main.go\n" +
	"index 1111111..2222222 100644\n" +
	"--- a/main.go\n" +
	"+++ b/main.go\n" +
	"@@ -1,3 +1,3 @@\n" +
	"1\t package main\n" +
	"\t-var x = 1\n" +
	"2\t+var x = 2\n" +
	"3\t var y = 3\n" +
	"@@ -10,2 +10,3 @@ func f() {\n" +
	"10\t \ta()\n" +
	"11\t+\tb()\n" +
	"12\t }\n" +
	"\t\\ No newline at end of file\n"

func readDiff(t *testing.T, tool *readDiffTool, input string) (string, error) {
	t.Helper()
	return tool.Run(t.Context(), json.RawMessage(input))
}

func TestReadDiff(t *testing.T) {
	whole := newReadDiffTool(readDiffSample, 32<<10)
	for _, tt := range []struct {
		name, input, want string
	}{
		{"a modified file", `{"path":"main.go"}`, numberedMain},
		{"a path given as relative", `{"path":"./main.go"}`, numberedMain},
		{"a new file", `{"path":"new.go"}`, "diff --git a/new.go b/new.go\nnew file mode 100644\nindex 0000000..3333333\n" +
			"--- /dev/null\n+++ b/new.go\n@@ -0,0 +1,2 @@\n1\t+package main\n2\t+var z = 4\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := readDiff(t, whole, tt.input)
			if err != nil || got != tt.want {
				t.Fatalf("read_diff %s = %q, %v; want %q", tt.input, got, err, tt.want)
			}
		})
	}
	for _, input := range []string{`{"path":"missing.go"}`, `{"path":"main.go","page":2}`} {
		if got, err := readDiff(t, whole, input); err == nil {
			t.Fatalf("read_diff %s = %q, want an error", input, got)
		}
	}
}

// TestReadDiffPages: a diff longer than a page comes in pages cut at line
// ends, which together are the whole, so a page that starts inside a hunk
// still leads each line with its number.
func TestReadDiffPages(t *testing.T) {
	tool := newReadDiffTool(readDiffSample, pageNoteRoom+80)
	note := regexp.MustCompile(`\[page (\d+) of (\d+) of this file's diff(; read_diff with page \d+ reads on)?\]$`)
	var pages []string
	for page := 1; ; page++ {
		got, err := readDiff(t, tool, `{"path":"main.go","page":`+strconv.Itoa(page)+`}`)
		if err != nil {
			t.Fatalf("page %d: %v", page, err)
		}
		m := note.FindStringSubmatch(got)
		if m == nil || m[1] != strconv.Itoa(page) || len(got) > tool.maxBytes {
			t.Fatalf("page %d = %q, want it numbered within %d bytes", page, got, tool.maxBytes)
		}
		pages = append(pages, strings.TrimSuffix(got, m[0]))
		if m[1] == m[2] {
			if m[3] != "" {
				t.Fatalf("the last page %q points past itself", got)
			}
			break
		}
	}
	if got := strings.Join(pages, ""); got != numberedMain {
		t.Fatalf("the pages together = %q, want %q", got, numberedMain)
	}
	insideHunk := false
	for _, p := range pages[1:] {
		insideHunk = insideHunk || p[0] == '\t' || '0' <= p[0] && p[0] <= '9'
	}
	if !insideHunk {
		t.Fatalf("no page starts inside a hunk: %q", pages)
	}
}

func TestDiffPagesCutsALongLine(t *testing.T) {
	pages := diffPages("short\n"+strings.Repeat("x", 100)+"\nend\n", 40)
	if len(pages) != 3 || pages[0] != "short\n" || pages[2] != "end\n" ||
		len(pages[1]) > 40 || !strings.HasSuffix(pages[1], " [cut]\n") {
		t.Fatalf("pages = %q", pages)
	}
}
