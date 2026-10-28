package review

import (
	"slices"
	"testing"
)

// TestBumps: a line rewritten with only a version-like token or two
// changed is a bump, in any file but a lockfile, an address among them
// since its shape is a version's; a line that moves anything else, or a
// plain number, is not.
func TestBumps(t *testing.T) {
	file := func(path string, lines ...string) string {
		diff := "diff --git a/" + path + " b/" + path + "\n--- a/" + path + "\n+++ b/" + path + "\n@@ -1,8 +1,8 @@\n"
		for _, l := range lines {
			diff += l + "\n"
		}
		return diff
	}
	for _, tt := range []struct {
		name string
		diff string
		want []Bump
	}{
		{name: "a Go module", diff: file("go.mod", " module a", "-\tgithub.com/x/y v1.2.3", "+\tgithub.com/x/y v1.3.0", " )"),
			want: []Bump{{Path: "go.mod", Line: 2, Old: "v1.2.3", New: "v1.3.0", positions: [maxBumpTokens]int{2}}}},
		{name: "an image tag and its digest", diff: file("apps/x/hr.yaml", "-    tag: 1.2.3@sha256:"+sha("a"), "+    tag: 1.2.4@sha256:"+sha("b")),
			want: []Bump{{Path: "apps/x/hr.yaml", Line: 1, Old: "1.2.3@sha256:" + sha("a"), New: "1.2.4@sha256:" + sha("b"), positions: [maxBumpTokens]int{2}}}},
		{name: "a digest alone", diff: file("k.yaml", "-image: ghcr.io/x/y:latest@sha256:"+sha("a"), "+image: ghcr.io/x/y:latest@sha256:"+sha("b")),
			want: []Bump{{Path: "k.yaml", Line: 1, Old: "ghcr.io/x/y:latest@sha256:" + sha("a"), New: "ghcr.io/x/y:latest@sha256:" + sha("b"), positions: [maxBumpTokens]int{2}}}},
		{name: "a pinned action and its version comment", diff: file(".github/workflows/ci.yaml", "-      - uses: actions/checkout@abc1234 # v4.1.0", "+      - uses: actions/checkout@def5678 # v4.2.0"),
			want: []Bump{{Path: ".github/workflows/ci.yaml", Line: 1, Old: "actions/checkout@abc1234 v4.1.0", New: "actions/checkout@def5678 v4.2.0", positions: [maxBumpTokens]int{3, 5}}}},
		{name: "a chart version in quotes", diff: file("Chart.yaml", `-  version: "0.3.1"`, `+  version: "0.4.0"`),
			want: []Bump{{Path: "Chart.yaml", Line: 1, Old: "0.3.1", New: "0.4.0", positions: [maxBumpTokens]int{2}}}},
		{name: "a JSON dependency range", diff: file("package.json", `-    "left-pad": "^1.3.0",`, `+    "left-pad": "^2.0.0",`),
			want: []Bump{{Path: "package.json", Line: 1, Old: "^1.3.0", New: "^2.0.0", positions: [maxBumpTokens]int{3}}}},
		{name: "several lines in one block", diff: file("go.mod", "-\ta v1.0.0", "-\tb v2.0.0", "+\ta v1.0.1", "+\tb v2.0.1"),
			want: []Bump{{Path: "go.mod", Line: 1, Old: "v1.0.0", New: "v1.0.1", positions: [maxBumpTokens]int{2}}, {Path: "go.mod", Line: 2, Old: "v2.0.0", New: "v2.0.1", positions: [maxBumpTokens]int{2}}}},
		{name: "a lockfile is left out", diff: file("go.sum", "-github.com/x/y v1.2.3 h1:abc=", "+github.com/x/y v1.3.0 h1:def=") +
			file("pnpm-lock.yaml", "-  left-pad@1.3.0:", "+  left-pad@2.0.0:")},
		{name: "a plain number is no version", diff: file("config.yaml", "-retries: 3", "+retries: 4")},
		{name: "code that changes a number", diff: file("main.go", "-\tx := 1", "+\tx := 2")},
		{name: "a line that moves more than the version", diff: file("go.mod", "-\tgithub.com/x/y v1.2.3", "+\tgithub.com/x/z v1.2.3 // indirect")},
		{name: "a line that moves three tokens", diff: file("k.yaml", "-a: 1.0 b: 2.0 c: 3.0", "+a: 1.1 b: 2.1 c: 3.1")},
		{name: "unequal runs are not paired", diff: file("go.mod", "-\ta v1.0.0", "+\ta v1.0.1", "+\tb v2.0.0")},
		{name: "a deleted file", diff: "diff --git a/x.yaml b/x.yaml\ndeleted file mode 100644\n--- a/x.yaml\n+++ /dev/null\n@@ -1 +0,0 @@\n-tag: 1.2.3\n"},
		{name: "a last line without a newline", diff: file("x.yaml", "-tag: 1.2.3", "\\ No newline at end of file", "+tag: 2.0.0"),
			want: []Bump{{Path: "x.yaml", Line: 1, Old: "1.2.3", New: "2.0.0", positions: [maxBumpTokens]int{2}}}},
		{name: "a CRLF line", diff: file("x.yaml", "-tag: 1.2.3\r", "+tag: 1.2.4\r"),
			want: []Bump{{Path: "x.yaml", Line: 1, Old: "1.2.3", New: "1.2.4", positions: [maxBumpTokens]int{2}}}},
		{name: "a four-part version, or an address, is one", diff: file("x.yaml", "-tag: 1.2.3.4", "+tag: 1.2.3.5"),
			want: []Bump{{Path: "x.yaml", Line: 1, Old: "1.2.3.4", New: "1.2.3.5", positions: [maxBumpTokens]int{2}}}},
		{name: "the line the hunk puts it on", diff: "diff --git a/go.mod b/go.mod\n--- a/go.mod\n+++ b/go.mod\n@@ -40,3 +41,3 @@\n require (\n-\ta v1.0.0\n+\ta v1.0.1\n )\n",
			want: []Bump{{Path: "go.mod", Line: 42, Old: "v1.0.0", New: "v1.0.1", positions: [maxBumpTokens]int{2}}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := Bumps(tt.diff); !slices.Equal(got, tt.want) {
				t.Fatalf("Bumps = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// sha is a digest of one repeated hex digit.
func sha(c string) string {
	s := ""
	for range 64 {
		s += c
	}
	return s
}
