package review

import (
	"fmt"
	"strings"
	"testing"
)

func TestBumpStep(t *testing.T) {
	for _, tt := range []struct {
		name, old, now string
		want           Step
		early          bool
	}{
		{name: "a major step", old: "1.2.3", now: "2.0.0", want: StepMajor},
		{name: "a minor step", old: "v1.2.3", now: "v1.3.0", want: StepMinor},
		{name: "a patch step", old: "1.2.3", now: "1.2.4", want: StepPatch},
		{name: "a fourth component", old: "1.2.3.4", now: "1.2.3.5", want: StepPatch},
		{name: "a shorter new version", old: "1.2", now: "1.2.1", want: StepPatch},
		{name: "a new digest of the same version", old: "1.2.3@sha256:aaaa", now: "1.2.3@sha256:bbbb", want: StepDigest},
		{name: "a new pre-release of the same version", old: "2.0.0-beta.1", now: "2.0.0-beta.2", want: StepDigest},
		{name: "a new build of the same version", old: "golang:1.22-alpine", now: "golang:1.22-bookworm", want: StepDigest},
		{name: "a tag and its digest", old: "ghcr.io/x/y:1.2.3@sha256:aaaa", now: "ghcr.io/x/y:1.2.4@sha256:bbbb", want: StepPatch},
		{name: "a registry port before the tag", old: "registry.example.com:5000/app:1.2.3", now: "registry.example.com:5000/app:1.3.0", want: StepMinor},
		{name: "an image tag with a build", old: "python:3.12-slim", now: "python:3.13-slim", want: StepMinor},
		{name: "a tag of one component", old: "node:20", now: "node:22", want: StepMajor},
		{name: "a range", old: "^1.2.3", now: "^1.3.0", want: StepMinor},
		{name: "a constraint", old: ">=1.2.3", now: ">=2.0.0", want: StepMajor},
		{name: "a commit and the version beside it", old: "actions/checkout@0ad4b8f v4.1.0", now: "actions/checkout@b4ffde6 v4.2.0", want: StepMinor},
		{name: "a commit alone", old: "actions/checkout@0ad4b8f", now: "actions/checkout@b4ffde6", want: StepUnknown},
		{name: "a commit that starts with digits", old: "actions/checkout@1a2b3c4", now: "actions/checkout@5d6e7f8", want: StepUnknown},
		{name: "a commit of digits alone", old: "actions/checkout@1234567890123456789012345678901234567890", now: "actions/checkout@9876543210987654321098765432109876543210", want: StepUnknown},
		{name: "a short commit of digits alone", old: "actions/checkout@1234567", now: "actions/checkout@7654321", want: StepUnknown},
		{name: "a commit of digits alone beside the version", old: "actions/checkout@1234567890123456789012345678901234567890 v4.1.0",
			now: "actions/checkout@9876543210987654321098765432109876543210 v4.2.0", want: StepMinor},
		{name: "a module version after an at sign", old: "golang.org/x/tools@v2", now: "golang.org/x/tools@v3", want: StepMajor},
		{name: "a dotted version after an at sign", old: "pkg@1.2.3", now: "pkg@1.3.0", want: StepMinor},
		{name: "a digest under latest", old: "ghcr.io/x/y:latest@sha256:aaaa", now: "ghcr.io/x/y:latest@sha256:bbbb", want: StepUnknown},
		{name: "a digest alone", old: "sha256:1234abcd", now: "sha256:5678efab", want: StepUnknown},
		{name: "under 1.0, the first non-zero component", old: "0.1.0", now: "0.2.0", want: StepMinor, early: true},
		{name: "under 1.0, the first non-zero third component", old: "0.0.1", now: "0.0.2", want: StepMinor, early: true},
		{name: "under 1.0, a component before the first non-zero one", old: "0.0.5", now: "0.1.0", want: StepMinor, early: true},
		{name: "under 1.0, a component after it", old: "0.1.2", now: "0.1.3", want: StepPatch},
		{name: "out of 0.x", old: "0.9.0", now: "1.0.0", want: StepMajor},
		{name: "a chart version", old: "v0.0.1", now: "v0.0.2", want: StepMinor, early: true},
		{name: "a path segment names no version", old: "https://example.com/v1/docs", now: "https://example.com/v2/docs", want: StepUnknown},
		{name: "an address as a registry", old: "192.168.1.10:5000/app:latest@sha256:aaaa", now: "192.168.1.10:5000/app:latest@sha256:bbbb", want: StepUnknown},
		{name: "a version in a repository path", old: "registry.example.com/v1/app:latest@sha256:aaaa", now: "registry.example.com/v1/app:latest@sha256:bbbb", want: StepUnknown},
		{name: "a module path and its version", old: "github.com/foo/bar/v2 v2.1.0", now: "github.com/foo/bar/v3 v3.0.0", want: StepMajor},
		{name: "a version in an XML element", old: "<version>1.2.3</version>", now: "<version>2.0.0</version>", want: StepMajor},
		{name: "an archive named by its version", old: "https://example.com/x/archive/v1.2.3.tar.gz", now: "https://example.com/x/archive/v1.3.0.tar.gz", want: StepMinor},
		{name: "a version in a namespaced XML element", old: "<v1:version>1.2.3</v1:version>", now: "<v1:version>2.0.0</v1:version>", want: StepMajor},
		{name: "a tag that names a version around no version", old: "<v1:version>latest</v1:version>", now: "<v1:version>stable</v1:version>", want: StepUnknown},
		{name: "a range's upper bound", old: "<2.0.0", now: "<3.0.0", want: StepMajor},
		{name: "a range's upper bound with a v", old: "<v2.0.0", now: "<v3.0.0", want: StepMajor},
		{name: "an archive named by its commit", old: "https://example.com/x/archive/1234567.tar.gz", now: "https://example.com/x/archive/7654321.tar.gz", want: StepUnknown},
		{name: "an npm major selector", old: "react@18", now: "react@19", want: StepMajor},
		{name: "a date tag", old: "ghcr.io/x/y:20241014", now: "ghcr.io/x/y:20241031", want: StepUnknown},
		{name: "a tag joined by a hyphen names no version", old: "ubuntu-22.04", now: "ubuntu-24.04", want: StepUnknown},
		{name: "an address", old: "10.0.0.1", now: "10.0.0.2", want: StepPatch},
		{name: "a kubernetes version", old: "registry.k8s.io/kube-apiserver:v1.30.0", now: "registry.k8s.io/kube-apiserver:v1.31.0", want: StepMinor},
		{name: "a component past the int range", old: "1.99999999999999999999", now: "1.99999999999999999998", want: StepMinor},
		{name: "leading zeros", old: "1.02.3", now: "1.2.3", want: StepDigest},
		{name: "a registry port before a tag that names no version", old: "registry.example.com:5000/app:latest@sha256:abcd", now: "registry.example.com:5000/app:latest@sha256:efab", want: StepUnknown},
		{name: "a digest of digits alone", old: "app:1.2.3@sha256:1234", now: "app:1.2.3@sha256:5678", want: StepDigest},
		{name: "a path with two versions", old: "https://x/v1/v2", now: "https://x/v1/v3", want: StepMajor},
		{name: "two tokens of equal steps, either under 1.0", old: "1.2.0 0.1.0", now: "1.3.0 0.2.0", want: StepMinor, early: true},
		{name: "two tokens of equal steps, the other under 1.0", old: "0.1.0 1.2.0", now: "0.2.0 1.3.0", want: StepMinor, early: true},
		{name: "two tokens, the bigger step wins", old: "0.1.0 1.2.0", now: "0.2.0 2.0.0", want: StepMajor},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if step, early := (Bump{Old: tt.old, New: tt.now}).Step(); step != tt.want || early != tt.early {
				t.Fatalf("Step(%q -> %q) = %s, %v; want %s, %v", tt.old, tt.now, step, early, tt.want, tt.early)
			}
		})
	}
}

func TestStepRisk(t *testing.T) {
	for _, tt := range []struct {
		step  Step
		early bool
		reach Reach
		want  Risk
	}{
		{StepMajor, false, ReachStandalone, RiskMedium},
		{StepMajor, false, ReachShared, RiskHigh},
		{StepMajor, false, ReachCentral, RiskHigh},
		{StepMinor, false, ReachStandalone, RiskLow},
		{StepMinor, false, ReachShared, RiskMedium},
		{StepMinor, false, ReachCentral, RiskHigh},
		{StepMinor, true, ReachStandalone, RiskMedium},
		{StepMinor, true, ReachCentral, RiskHigh},
		{StepUnknown, false, ReachStandalone, RiskLow},
		{StepUnknown, false, ReachShared, RiskMedium},
		{StepPatch, false, ReachStandalone, RiskLow},
		{StepPatch, false, ReachShared, RiskLow},
		{StepPatch, false, ReachCentral, RiskMedium},
		{StepDigest, false, ReachCentral, RiskMedium},
		{StepMajor, false, ReachNone, RiskLow},
	} {
		name := fmt.Sprintf("%s step of a %s one", tt.step, tt.reach)
		if tt.early {
			name += " under 1.0"
		}
		t.Run(name, func(t *testing.T) {
			if got := stepRisk(tt.step, tt.early, tt.reach); got != tt.want {
				t.Fatalf("stepRisk(%s, %v, %s) = %s, want %s", tt.step, tt.early, tt.reach, got, tt.want)
			}
		})
	}
}

// TestBumpsStep: the step reads from a bump as Bumps extracts it, with the
// line's punctuation parted from the version.
func TestBumpsStep(t *testing.T) {
	diff := "diff --git a/requirements.txt b/requirements.txt\n--- a/requirements.txt\n+++ b/requirements.txt\n@@ -1 +1 @@\n" +
		"-pkg==1.2.3;python_version>'3.8'\n+pkg==2.0.0;python_version>'3.8'\n" +
		"diff --git a/pom.xml b/pom.xml\n--- a/pom.xml\n+++ b/pom.xml\n@@ -1 +1 @@\n" +
		"-    <v1:version>1.2.3</v1:version>\n+    <v1:version>1.3.0</v1:version>\n"
	bumps := Bumps(diff)
	if len(bumps) != 2 || bumps[0].Old != "1.2.3" || bumps[0].New != "2.0.0" {
		t.Fatalf("bumps = %+v, want the requirement's version alone and the element", bumps)
	}
	for i, want := range []Step{StepMajor, StepMinor} {
		if step, _ := bumps[i].Step(); step != want {
			t.Errorf("%s: step = %s, want %s", bumps[i].Path, step, want)
		}
	}
}

// TestListedBumps: the judge is given the bumps its list holds, and the
// prompt counts the rest.
func TestListedBumps(t *testing.T) {
	bumps := make([]Bump, 60)
	for i := range bumps {
		bumps[i] = Bump{Path: "kubernetes/apps/default/app/helmrelease.yaml", Line: i + 1, Old: "1.2.3", New: "1.2.4"}
	}
	listed := ListedBumps(bumps)
	if len(listed) == 0 || len(listed) == len(bumps) {
		t.Fatalf("%d of %d bumps listed; want the first that fit the list alone", len(listed), len(bumps))
	}
	var b strings.Builder
	writeBumps(&b, bumps, nil)
	if more := fmt.Sprintf("- and %d more\n", len(bumps)-len(listed)); !strings.HasSuffix(b.String(), more) ||
		strings.Count(b.String(), "\n- ") != len(listed)+1 {
		t.Fatalf("the list does not end with %q after %d items:\n%s", more, len(listed), b.String())
	}
	if few := ListedBumps(bumps[:3]); len(few) != 3 {
		t.Fatalf("%d of 3 listed", len(few))
	}
}

func TestStepFloor(t *testing.T) {
	bumps := []Bump{
		{Path: "charts/app/Chart.yaml", Line: 4, Old: "1.2.3", New: "1.2.4"},
		{Path: "kubernetes/app/helmrelease.yaml", Line: 12, Old: "0.1.0", New: "0.2.0"},
		{Path: "README.md", Line: 3, Old: "v1.0", New: "v2.0"},
		{Path: "Dockerfile", Line: 1, Old: "golang:1.22-alpine", New: "golang:1.23-alpine"},
	}
	for _, tt := range []struct {
		name    string
		bumps   []Bump
		reaches []Reach
		want    Risk
		at      string
	}{
		{name: "no bumps", want: RiskLow},
		{name: "a patch of a standalone one", bumps: bumps[:1], reaches: []Reach{ReachStandalone}, want: RiskLow},
		{name: "a patch of a central one", bumps: bumps[:1], reaches: []Reach{ReachCentral}, want: RiskMedium, at: "patch step at charts/app/Chart.yaml:4"},
		{name: "a chart under 1.0 rates at least medium", bumps: bumps[1:2], reaches: []Reach{ReachStandalone},
			want: RiskMedium, at: "minor step at kubernetes/app/helmrelease.yaml:12"},
		{name: "a reach of none takes a line out", bumps: bumps[2:3], reaches: []Reach{ReachNone}, want: RiskLow},
		{name: "a step the judge gave no reach is standalone", bumps: bumps[3:], want: RiskLow},
		{name: "a step the judge gave no reach is standalone, under 1.0", bumps: bumps[1:2], want: RiskMedium, at: "minor step at kubernetes/app/helmrelease.yaml:12"},
		{name: "the highest holds, the first of equals", bumps: bumps, reaches: []Reach{ReachCentral, ReachShared, ReachNone, ReachCentral},
			want: RiskHigh, at: "minor step at Dockerfile:1"},
		{name: "a reach that is none of them is standalone", bumps: bumps[3:], reaches: []Reach{"everything"}, want: RiskLow},
		{name: "a digest under latest of a shared one", bumps: []Bump{{Path: "a.yaml", Line: 1, Old: "x:latest@sha256:aaaa", New: "x:latest@sha256:bbbb"}},
			reaches: []Reach{ReachShared}, want: RiskMedium, at: "step of unknown size at a.yaml:1"},
		{name: "a new digest of a central one", bumps: []Bump{{Path: "a.yaml", Line: 1, Old: "x:1.2@sha256:aaaa", New: "x:1.2@sha256:bbbb"}},
			reaches: []Reach{ReachCentral}, want: RiskMedium, at: "new digest at a.yaml:1"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if floor, at := StepFloor(tt.bumps, tt.reaches); floor != tt.want || at != tt.at {
				t.Fatalf("StepFloor = %s at %q, want %s at %q", floor, at, tt.want, tt.at)
			}
		})
	}
}
