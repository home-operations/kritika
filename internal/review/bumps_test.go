package review

import (
	"slices"
	"testing"
)

// TestBumps: a line rewritten with only a version-like token or two changed
// is a bump, in any file but a lockfile. An address counts among these
// tokens, since its shape is a version's. A line that moves anything else, or
// a plain number, is not a bump.
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
			want: []Bump{{Path: "go.mod", Line: 2, Old: "v1.2.3", New: "v1.3.0", Key: "go.mod github.com/x/y *", positions: [maxBumpTokens]int{2}}}},
		{name: "an image tag and its digest", diff: file("apps/x/hr.yaml", "-    tag: 1.2.3@sha256:"+sha("a"), "+    tag: 1.2.4@sha256:"+sha("b")),
			want: []Bump{{Path: "apps/x/hr.yaml", Line: 1, Old: "1.2.3@sha256:" + sha("a"), New: "1.2.4@sha256:" + sha("b"), Key: "apps/x/hr.yaml tag: *", positions: [maxBumpTokens]int{2}}}},
		{name: "a digest alone", diff: file("k.yaml", "-image: ghcr.io/x/y:latest@sha256:"+sha("a"), "+image: ghcr.io/x/y:latest@sha256:"+sha("b")),
			want: []Bump{{Path: "k.yaml", Line: 1, Old: "ghcr.io/x/y:latest@sha256:" + sha("a"), New: "ghcr.io/x/y:latest@sha256:" + sha("b"), Key: "k.yaml image: ghcr.io/x/y:latest@sha256:*", positions: [maxBumpTokens]int{2}}}},
		{name: "a pinned action and its version comment", diff: file(".github/workflows/ci.yaml", "-      - uses: actions/checkout@abc1234 # v4.1.0", "+      - uses: actions/checkout@def5678 # v4.2.0"),
			want: []Bump{{Path: ".github/workflows/ci.yaml", Line: 1, Old: "actions/checkout@abc1234 v4.1.0", New: "actions/checkout@def5678 v4.2.0", Key: ".github/workflows/ci.yaml - uses: actions/checkout@* # *", positions: [maxBumpTokens]int{3, 5}}}},
		{name: "a chart version in quotes", diff: file("Chart.yaml", `-  version: "0.3.1"`, `+  version: "0.4.0"`),
			want: []Bump{{Path: "Chart.yaml", Line: 1, Old: "0.3.1", New: "0.4.0", Key: "Chart.yaml version: *", positions: [maxBumpTokens]int{2}}}},
		{name: "a JSON dependency range", diff: file("package.json", `-    "left-pad": "^1.3.0",`, `+    "left-pad": "^2.0.0",`),
			want: []Bump{{Path: "package.json", Line: 1, Old: "^1.3.0", New: "^2.0.0", Key: "package.json left-pad : ^*", positions: [maxBumpTokens]int{3}}}},
		{name: "several lines in one block", diff: file("go.mod", "-\ta v1.0.0", "-\tb v2.0.0", "+\ta v1.0.1", "+\tb v2.0.1"),
			want: []Bump{{Path: "go.mod", Line: 1, Old: "v1.0.0", New: "v1.0.1", Key: "go.mod a *", positions: [maxBumpTokens]int{2}}, {Path: "go.mod", Line: 2, Old: "v2.0.0", New: "v2.0.1", Key: "go.mod b *", positions: [maxBumpTokens]int{2}}}},
		{name: "a lockfile is left out", diff: file("go.sum", "-github.com/x/y v1.2.3 h1:abc=", "+github.com/x/y v1.3.0 h1:def=") +
			file("pnpm-lock.yaml", "-  left-pad@1.3.0:", "+  left-pad@2.0.0:")},
		{name: "a plain number is no version", diff: file("config.yaml", "-retries: 3", "+retries: 4")},
		{name: "code that changes a number", diff: file("main.go", "-\tx := 1", "+\tx := 2")},
		{name: "a line that moves more than the version", diff: file("go.mod", "-\tgithub.com/x/y v1.2.3", "+\tgithub.com/x/z v1.2.3 // indirect")},
		{name: "a line that moves three tokens", diff: file("k.yaml", "-a: 1.0 b: 2.0 c: 3.0", "+a: 1.1 b: 2.1 c: 3.1")},
		{name: "unequal runs are not paired", diff: file("go.mod", "-\ta v1.0.0", "+\ta v1.0.1", "+\tb v2.0.0")},
		{name: "a deleted file", diff: "diff --git a/x.yaml b/x.yaml\ndeleted file mode 100644\n--- a/x.yaml\n+++ /dev/null\n@@ -1 +0,0 @@\n-tag: 1.2.3\n"},
		{name: "a last line without a newline", diff: file("x.yaml", "-tag: 1.2.3", "\\ No newline at end of file", "+tag: 2.0.0"),
			want: []Bump{{Path: "x.yaml", Line: 1, Old: "1.2.3", New: "2.0.0", Key: "x.yaml tag: *", positions: [maxBumpTokens]int{2}}}},
		{name: "a CRLF line", diff: file("x.yaml", "-tag: 1.2.3\r", "+tag: 1.2.4\r"),
			want: []Bump{{Path: "x.yaml", Line: 1, Old: "1.2.3", New: "1.2.4", Key: "x.yaml tag: *", positions: [maxBumpTokens]int{2}}}},
		{name: "a four-part version, or an address, is one", diff: file("x.yaml", "-tag: 1.2.3.4", "+tag: 1.2.3.5"),
			want: []Bump{{Path: "x.yaml", Line: 1, Old: "1.2.3.4", New: "1.2.3.5", Key: "x.yaml tag: *", positions: [maxBumpTokens]int{2}}}},
		{name: "the line the hunk puts it on", diff: "diff --git a/go.mod b/go.mod\n--- a/go.mod\n+++ b/go.mod\n@@ -40,3 +41,3 @@\n require (\n-\ta v1.0.0\n+\ta v1.0.1\n )\n",
			want: []Bump{{Path: "go.mod", Line: 42, Old: "v1.0.0", New: "v1.0.1", Key: "go.mod a *", positions: [maxBumpTokens]int{2}}}},
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

// TestBumpKey: a bump's key is the line with every token cut where its
// version, commit or digest starts, so the same line moving to another
// version, digest or commit keeps its key, whichever of its tokens moved,
// and what a token names before its version stays.
func TestBumpKey(t *testing.T) {
	for _, tt := range []struct {
		name, line string
		positions  [maxBumpTokens]int
		want       string
	}{
		{name: "a tag", line: "    tag: 92.17.0", want: "tag: *"},
		{name: "an image with its tag", line: "image: ghcr.io/home-operations/sonarr:5.0.0.2907", want: "image: ghcr.io/home-operations/sonarr:*"},
		{name: "a digest beside the tag goes with it", line: "tag: 1.2.3@sha256:" + sha("a"), want: "tag: *"},
		{name: "a digest alone", line: "image: x:latest@sha256:" + sha("a"), want: "image: x:latest@sha256:*"},
		{name: "a commit and its comment, whichever moved", line: "uses: actions/checkout@abc1234 # v4.1.0", want: "uses: actions/checkout@* # *"},
		{name: "a registry's port and the repository stay", line: "image: registry:5000/x/y:1.2", want: "image: registry:5000/x/y:*"},
		{name: "a name's digit stays apart from the version", line: "image: redis7:7.2", want: "image: redis7:*"},
		{name: "a range operator stays", line: "left-pad : ^1.3.0", want: "left-pad : ^*"},
		{name: "a v goes with the version", line: "github.com/x/y v1.2.3", want: "github.com/x/y *"},
		{name: "a word of hex letters is no digest", line: "defaced: 1.0", want: "defaced: *"},
		{name: "a token with no version stays", line: "golang:1.22-alpine as build2", want: "golang:* as build2"},
		{name: "a registry's numeric account is no commit", line: "image: 123456789012.dkr.ecr.us-east-1.amazonaws.com/frontend:1.2.4",
			want: "image: 123456789012.dkr.ecr.us-east-1.amazonaws.com/frontend:*"},
		{name: "a commit alone", line: "ref: abc1234def", want: "ref: *"},
		{name: "two packages on one line key by the moved one", line: "RUN pip install frontend==1.2.3 database==2.0.0",
			positions: [maxBumpTokens]int{5}, want: "RUN pip install frontend * database * @5"},
		{name: "a version comment is no version of its own", line: "uses: actions/checkout@abc1234 # v4.1.0",
			positions: [maxBumpTokens]int{2}, want: "uses: actions/checkout@* # *"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := bumpKey(tt.line, tt.positions); got != tt.want {
				t.Fatalf("bumpKey = %q, want %q", got, tt.want)
			}
		})
	}
	for _, tt := range []struct {
		name, a, b string
		at, bt     [maxBumpTokens]int
	}{
		{name: "a commit alone, then with its comment", a: "uses: actions/checkout@abc1234 # v4.1.0", at: [maxBumpTokens]int{2},
			b: "uses: actions/checkout@def5678 # v4.2.0", bt: [maxBumpTokens]int{2, 4}},
		{name: "a tag, then its digest", a: "tag: 1.2.3@sha256:" + sha("a"), at: [maxBumpTokens]int{2}, b: "tag: 1.2.3@sha256:" + sha("b"), bt: [maxBumpTokens]int{2}},
		{name: "the same package of two, twice", a: "RUN pip install frontend==1.2.3 database==2.0.0", at: [maxBumpTokens]int{7},
			b: "RUN pip install frontend==1.2.4 database==2.0.1", bt: [maxBumpTokens]int{7}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if a, b := bumpKey(tt.a, tt.at), bumpKey(tt.b, tt.bt); a != b {
				t.Fatalf("keys differ: %q and %q", a, b)
			}
		})
	}
	one := [maxBumpTokens]int{2}
	if a, b := bumpKey("image: redis6:1.0", one), bumpKey("image: redis7:1.0", one); a == b {
		t.Fatalf("two names that differ by a digit share the key %q", a)
	}
	line := "RUN pip install frontend==1.2.4 database==3.0.0"
	if a, b := bumpKey(line, [maxBumpTokens]int{5}), bumpKey(line, [maxBumpTokens]int{7}); a == b {
		t.Fatalf("two packages of one line share the key %q", a)
	}
}

// TestBumpKeyNameAbove: a version on a line of its own takes the name from
// the name line above it that the diff's context shows, so two entries of
// one file keep their histories apart.
func TestBumpKeyNameAbove(t *testing.T) {
	diff := "diff --git a/Chart.yaml b/Chart.yaml\n--- a/Chart.yaml\n+++ b/Chart.yaml\n@@ -5,7 +5,7 @@\n" +
		" dependencies:\n   - name: frontend\n-    version: 1.2.3\n+    version: 1.2.4\n     repository: oci://x\n   - name: database\n     version: 5.6.8\n" +
		"@@ -20,3 +20,3 @@\n     repository: oci://x\n   - name: database\n-    version: 5.6.8\n+    version: 5.6.9\n" +
		"@@ -30,3 +30,3 @@\n   - name: cache\n     condition: cache.enabled\n     repository: oci://x\n     tags: []\n-    version: 1.0.0\n+    version: 1.0.1\n"
	bumps := Bumps(diff)
	want := []string{"Chart.yaml name: frontend version: *", "Chart.yaml name: database version: *", "Chart.yaml version: *"}
	if len(bumps) != len(want) {
		t.Fatalf("bumps = %+v, want %d", bumps, len(want))
	}
	for i, b := range bumps {
		if b.Key != want[i] {
			t.Errorf("key %d = %q, want %q", i, b.Key, want[i])
		}
	}
}

// TestOneBump: a change that moves one version and nothing else, outside a
// lockfile, is one the judge's risk can be carried from.
func TestOneBump(t *testing.T) {
	file := func(path string, lines ...string) string {
		diff := "diff --git a/" + path + " b/" + path + "\n--- a/" + path + "\n+++ b/" + path + "\n@@ -1,8 +1,8 @@\n"
		for _, l := range lines {
			diff += l + "\n"
		}
		return diff
	}
	for _, tt := range []struct {
		name, diff string
		want       bool
	}{
		{name: "a bump alone", diff: file("x.yaml", " a: b", "-tag: 1.2.3", "+tag: 1.2.4"), want: true},
		{name: "a bump and its lockfile", diff: file("go.mod", "-\ta v1.0.0", "+\ta v1.0.1") + file("go.sum", "-a v1.0.0 h1:x=", "+a v1.0.1 h1:y="), want: true},
		{name: "two bumps", diff: file("go.mod", "-\ta v1.0.0", "-\tb v2.0.0", "+\ta v1.0.1", "+\tb v2.0.1")},
		{name: "a bump of two tokens", diff: file("Dockerfile", "-RUN pip install frontend==1.2.3 database==2.0.0", "+RUN pip install frontend==1.2.4 database==3.0.0")},
		{name: "a bump and a mode change", diff: file("x.yaml", "-tag: 1.2.3", "+tag: 1.2.4") +
			"diff --git a/run.sh b/run.sh\nold mode 100644\nnew mode 100755\n"},
		{name: "a bump and a binary", diff: file("x.yaml", "-tag: 1.2.3", "+tag: 1.2.4") +
			"diff --git a/db.sqlite b/db.sqlite\nindex 1111111..2222222 100644\nBinary files a/db.sqlite and b/db.sqlite differ\n"},
		{name: "a bump and a rename", diff: file("x.yaml", "-tag: 1.2.3", "+tag: 1.2.4") +
			"diff --git a/.github/workflows/ci.yaml b/ci.yaml\nsimilarity index 100%\nrename from .github/workflows/ci.yaml\nrename to ci.yaml\n"},
		{name: "a bump in a file whose mode changes", diff: "diff --git a/x.yaml b/x.yaml\nold mode 100644\nnew mode 100755\n--- a/x.yaml\n+++ b/x.yaml\n@@ -1 +1 @@\n-tag: 1.2.3\n+tag: 1.2.4\n"},
		{name: "a bump and a line of code", diff: file("go.mod", "-\ta v1.0.0", "+\ta v1.0.1") + file("main.go", "-\tx := 1", "+\tx := 2")},
		{name: "a bump and an added line", diff: file("x.yaml", "-tag: 1.2.3", "+tag: 1.2.4", "+env: FOO=1")},
		{name: "no bump", diff: file("main.go", "-\tx := 1", "+\tx := 2")},
		{name: "nothing", diff: ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := OneBump(tt.diff); got != tt.want {
				t.Fatalf("OneBump = %v, want %v", got, tt.want)
			}
		})
	}
}
