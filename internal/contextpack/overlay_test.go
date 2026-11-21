package contextpack

import (
	"fmt"
	"strings"
	"testing"

	"github.com/home-operations/kritika/internal/chunk"
)

// overlayRun is a text file of n added lines, each a wide unchanged line
// from the next, so they make one run far over MaxChunkBytes, and its diff.
func overlayRun(n int) (src, diff string) {
	wide := strings.Repeat("w", DefaultOptions.MaxChunkBytes/2)
	var text, d strings.Builder
	d.WriteString("diff --git a/run.txt b/run.txt\n--- a/run.txt\n+++ b/run.txt\n")
	for i := range n {
		line := 2*i + 1
		fmt.Fprintf(&text, "changed%d\n%s\n", line, wide)
		fmt.Fprintf(&d, "@@ -%d,1 +%d,1 @@\n-old%d\n+changed%d\n", line, line, line, line)
	}
	return text.String(), d.String()
}

// TestOverlaySplitsALongRunInBoundedCopies: a run of nearby changes far over
// MaxChunkBytes is shown in chunks cut from it one after another, and each
// copies only what its cut can keep rather than the rest of the run.
func TestOverlaySplitsALongRunInBoundedCopies(t *testing.T) {
	src, diff := overlayRun(100)
	b := &builder{
		in:   Input{Head: headTree(t, map[string]string{"run.txt": src}), Diff: diff, Changed: []string{"run.txt"}},
		opts: DefaultOptions, parser: &chunk.Parser{},
	}
	n := 0
	copied = &n
	defer func() { copied = nil }()
	if err := b.overlay(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(b.overlayChunks) < 2 || !strings.HasPrefix(b.overlayChunks[0].Text, "changed1\n") {
		t.Fatalf("overlay = %d chunks, want the run in several from line 1", len(b.overlayChunks))
	}
	for _, c := range b.overlayChunks {
		if !strings.HasSuffix(c.Text, "\n… (truncated)") && c.EndLine != 199 {
			t.Errorf("overlay L%d-%d is neither cut nor the run's end", c.StartLine, c.EndLine)
		}
	}
	if n > 2*len(src) {
		t.Errorf("overlay copied %d bytes out of a %d-byte file", n, len(src))
	}
}

func BenchmarkOverlayLongRun(b *testing.B) {
	src, diff := overlayRun(150)
	head := headTree(b, map[string]string{"run.txt": src})
	b.ReportAllocs()
	for b.Loop() {
		bl := &builder{in: Input{Head: head, Diff: diff, Changed: []string{"run.txt"}}, opts: DefaultOptions, parser: &chunk.Parser{}}
		if err := bl.overlay(b.Context()); err != nil {
			b.Fatal(err)
		}
	}
}
