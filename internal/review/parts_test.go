package review

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// sizedDiff is a unified diff of the given files, each section n bytes
// long, in the order given.
func sizedDiff(t *testing.T, files ...any) string {
	t.Helper()
	var b strings.Builder
	for i := 0; i < len(files); i += 2 {
		p, n := files[i].(string), files[i+1].(int)
		head := fmt.Sprintf("diff --git a/%s b/%s\n", p, p)
		b.WriteString(head + "+" + strings.Repeat("x", n-len(head)-2) + "\n")
	}
	return b.String()
}

func TestSplitDiff(t *testing.T) {
	const k = 1 << 10
	for _, tt := range []struct {
		name     string
		diff     []any
		ignore   []string
		maxParts int
		want     [][]string
	}{
		{
			name: "a diff that fits one part is not split",
			diff: []any{"a/x.go", 30 * k, "b/y.go", 30 * k}, maxParts: 8,
		},
		{
			name: "ignored files count for nothing",
			diff: []any{"a/x.go", 30 * k, "package-lock.json", 200 * k}, ignore: []string{"**/package-lock.json"}, maxParts: 8,
		},
		{
			name: "one part is no split",
			diff: []any{"a/x.go", 50 * k, "b/y.go", 50 * k}, maxParts: 1,
		},
		{
			name: "a directory that fits a part starts one",
			diff: []any{"a/x.go", 30 * k, "b/y.go", 20 * k, "b/z.go", 20 * k, "c/w.go", 20 * k}, maxParts: 8,
			want: [][]string{{"a/x.go"}, {"b/y.go", "b/z.go", "c/w.go"}},
		},
		{
			name: "a directory larger than a part is cut between its files",
			diff: []any{"d/1.go", 30 * k, "d/2.go", 30 * k, "d/3.go", 30 * k}, maxParts: 8,
			want: [][]string{{"d/1.go", "d/2.go"}, {"d/3.go"}},
		},
		{
			name: "a file larger than a part is a part alone",
			diff: []any{"a/big.go", 100 * k, "a/small.go", 10 * k}, maxParts: 8,
			want: [][]string{{"a/big.go"}, {"a/small.go"}},
		},
		{
			name: "a directory's own files go before its subdirectories'",
			diff: []any{"a/b.go", 40 * k, "a/c/d.go", 40 * k, "a/e.go", 20 * k}, maxParts: 8,
			want: [][]string{{"a/b.go", "a/e.go"}, {"a/c/d.go"}},
		},
		{
			name: "past maxParts the parts grow",
			diff: []any{"a/x.go", 40 * k, "b/x.go", 40 * k, "c/x.go", 40 * k, "d/x.go", 40 * k, "e/x.go", 40 * k}, maxParts: 2,
			want: [][]string{{"a/x.go", "b/x.go", "c/x.go"}, {"d/x.go", "e/x.go"}},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := SplitDiff(sizedDiff(t, tt.diff...), tt.ignore, 64*k, tt.maxParts)
			if !slices.EqualFunc(got, tt.want, slices.Equal) {
				t.Fatalf("parts = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestSplitDiffTinySize: a part size too small to grow by an eighth still
// grows, so the split ends.
func TestSplitDiffTinySize(t *testing.T) {
	if parts := SplitDiff(sizedDiff(t, "a.go", 40, "b.go", 40, "c.go", 40), nil, 7, 2); len(parts) != 2 {
		t.Fatalf("parts = %q, want 2", parts)
	}
}

// TestSplitDiffKeepsEveryFile: whatever the sizes, every file no glob
// ignores is in exactly one part, within maxParts.
func TestSplitDiffKeepsEveryFile(t *testing.T) {
	const n = 40
	files, want := make([]any, 0, 2*n), make([]string, 0, n)
	for i := range n {
		p := fmt.Sprintf("dir%d/f%02d.go", i%7, i)
		files = append(files, p, (i%9+1)*7<<10)
		want = append(want, p)
	}
	parts := SplitDiff(sizedDiff(t, files...), nil, PartBytes, 4)
	got := slices.Concat(parts...)
	slices.Sort(got)
	slices.Sort(want)
	if len(parts) < 2 || len(parts) > 4 || !slices.Equal(got, want) {
		t.Fatalf("%d parts holding %q, want 2 to 4 holding each file once", len(parts), got)
	}
	if again := SplitDiff(sizedDiff(t, files...), nil, PartBytes, 4); !slices.EqualFunc(again, parts, slices.Equal) {
		t.Fatal("the same diff split differently")
	}
}

// TestBuildPart: a part's prompt lists every changed file, its own marked,
// says which part it is, and shows only its own files' diff.
func TestBuildPart(t *testing.T) {
	diff := sizedDiff(t, "a/x.go", 200, "b/y.go", 200)
	in := Input{Repository: "a/b", Number: 1, Changed: []string{"a/x.go", "b/y.go"}, Diff: PartDiff(diff, []string{"b/y.go"}),
		Part: &PartInput{Index: 2, Count: 2, Paths: []string{"b/y.go"}}}
	msg, _, _ := Build(in)
	for _, want := range []string{"- a/x.go\n- b/y.go (this part)\n", "reviewed in 2 parts", "this is part 2", "diff --git a/b/y.go"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("message lacks %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "diff --git a/a/x.go") {
		t.Fatalf("part 2's message shows part 1's diff:\n%s", msg)
	}
	if whole, _, _ := Build(Input{Repository: "a/b", Number: 1, Changed: in.Changed, Diff: diff}); strings.Contains(whole, "this part") {
		t.Fatalf("a review that is not split names a part:\n%s", whole)
	}
}

func TestCheckPart(t *testing.T) {
	check := CheckPart([]string{"a/x.go"}, []string{"b/y.go"})
	submit := func(path string) json.RawMessage {
		return json.RawMessage(`{"summary":{"take":"t","praise":[]},"findings":[{"path":"` + path + `","line":1,"severity":"nit",` +
			`"category":"correctness","title":"t","explanation":"e"}]}`)
	}
	if err := check(submit("a/x.go")); err != nil {
		t.Fatalf("a finding on the part's own file: %v", err)
	}
	if err := check(submit("docs/unchanged.md")); err != nil {
		t.Fatalf("a finding off the diff, which no part reviews: %v", err)
	}
	if err := check(submit("b/y.go")); err == nil || !strings.Contains(err.Error(), "a/x.go") {
		t.Fatalf("a finding on another part's file = %v, want it refused naming the part's own", err)
	}
	if err := check(json.RawMessage(`{"summary":{"take":""},"findings":[]}`)); err == nil {
		t.Fatal("a submission Check refuses passed")
	}
	lenient := Lenient(check)
	misspelled := func(path string) json.RawMessage {
		return json.RawMessage(strings.Replace(string(submit(path)), `"title"`, `"title_"`, 1))
	}
	if !lenient(misspelled("a/x.go")) {
		t.Fatal("a misspelled key on the part's own file is not taken as a fallback")
	}
	if lenient(misspelled("b/y.go")) {
		t.Fatal("a misspelled key on another part's file is taken as a fallback")
	}
}

func TestMergeParts(t *testing.T) {
	got := MergeParts([]Result{
		{Summary: Summary{Headline: "First", Take: "One.", Praise: []string{"a", "b"}, Checked: []string{"c1"}},
			Findings: []Finding{{Path: "a/x.go", Title: "x"}}},
		{Summary: Summary{Headline: "Second", Take: "Two.", Praise: []string{"c", "d"}, Checked: []string{"c2"}},
			Findings: []Finding{{Path: "b/y.go", Title: "y"}}},
	})
	if got.Summary.Headline != "First" || got.Summary.Take != "One.\n\nTwo." || !slices.Equal(got.Summary.Praise, []string{"a", "b", "c"}) ||
		!slices.Equal(got.Summary.Checked, []string{"c1", "c2"}) || len(got.Findings) != 2 || got.Findings[1].Path != "b/y.go" {
		t.Fatalf("merged = %+v", got)
	}
	if empty := MergeParts(nil); empty.Summary.Praise == nil || empty.Findings == nil {
		t.Fatalf("merging nothing = %+v, want empty lists the contract takes", empty)
	}
}
