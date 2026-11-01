package review

import (
	"slices"
	"testing"
)

// TestUncovered: a bump is covered by a source that holds the version it
// moves to, or a name its line or its path gives the dependency; the words
// any manifest uses name nothing.
func TestUncovered(t *testing.T) {
	diff := "diff --git a/kubernetes/apps/o11y/kube-prometheus-stack/app/ocirepository.yaml b/kubernetes/apps/o11y/kube-prometheus-stack/app/ocirepository.yaml\n" +
		"--- a/kubernetes/apps/o11y/kube-prometheus-stack/app/ocirepository.yaml\n+++ b/kubernetes/apps/o11y/kube-prometheus-stack/app/ocirepository.yaml\n" +
		"@@ -13 +13 @@\n-    tag: 92.13.1\n+    tag: 92.17.0\n" +
		"diff --git a/go.mod b/go.mod\n--- a/go.mod\n+++ b/go.mod\n@@ -9 +9 @@\n-\tgithub.com/x/y v1.2.3\n+\tgithub.com/x/y v1.3.0\n" +
		"diff --git a/k.yaml b/k.yaml\n--- a/k.yaml\n+++ b/k.yaml\n@@ -1 +1 @@\n-image: ghcr.io/home-operations/sonarr:5.0.0.2870@sha256:" + sha("a") +
		"\n+image: ghcr.io/home-operations/sonarr:5.0.0.2907@sha256:" + sha("b") + "\n"
	bumps := Bumps(diff)
	if len(bumps) != 3 {
		t.Fatalf("bumps = %+v, want 3", bumps)
	}
	paths := func(bumps []Bump) []string {
		out := make([]string, 0, len(bumps))
		for _, b := range bumps {
			out = append(out, b.Path)
		}
		return out
	}
	for _, tt := range []struct {
		name    string
		sources []string
		want    []string
	}{
		{name: "no sources", want: []string{"kubernetes/apps/o11y/kube-prometheus-stack/app/ocirepository.yaml", "go.mod", "k.yaml"}},
		{name: "no bumps uncovered is nil", sources: []string{"https://github.com/x/y/releases/tag/v1.3.0",
			"https://github.com/prometheus-community/helm-charts/compare/kube-prometheus-stack-92.13.1...kube-prometheus-stack-92.17.0", "https://github.com/Sonarr/Sonarr/releases"}, want: []string{}},
		{name: "the version of a module with no name", sources: []string{"https://github.com/x/y/releases/tag/v1.3.0"},
			want: []string{"kubernetes/apps/o11y/kube-prometheus-stack/app/ocirepository.yaml", "k.yaml"}},
		{name: "the version alone does not name a dependency that has a name", sources: []string{"https://github.com/other/thing/releases/tag/v5.0.0.2907"},
			want: []string{"kubernetes/apps/o11y/kube-prometheus-stack/app/ocirepository.yaml", "go.mod", "k.yaml"}},
		{name: "a longer number is not the version", sources: []string{"https://github.com/x/y/releases/tag/v11.3.0", "https://github.com/x/y/releases/tag/v1.3.0.4"},
			want: []string{"kubernetes/apps/o11y/kube-prometheus-stack/app/ocirepository.yaml", "go.mod", "k.yaml"}},
		{name: "a pre-release of the version is the version", sources: []string{"https://github.com/x/y/releases/tag/v1.3.0-rc.1"},
			want: []string{"kubernetes/apps/o11y/kube-prometheus-stack/app/ocirepository.yaml", "k.yaml"}},
		{name: "the name from the path", sources: []string{"https://github.com/prometheus-community/helm-charts/compare/kube-prometheus-stack-92.13.1...kube-prometheus-stack-92.17.0"},
			want: []string{"go.mod", "k.yaml"}},
		{name: "the name from the image", sources: []string{"https://github.com/Sonarr/Sonarr/releases"},
			want: []string{"kubernetes/apps/o11y/kube-prometheus-stack/app/ocirepository.yaml", "go.mod"}},
		{name: "an oci reference", sources: []string{"oci://ghcr.io/prometheus-community/charts/kube-prometheus-stack:92.17.0", "oci://ghcr.io/home-operations/sonarr:5.0.0.2907"},
			want: []string{"go.mod"}},
		{name: "generic words name nothing", sources: []string{"https://example.com/apps/tag/version/image/github.com/ocirepository.yaml"},
			want: []string{"kubernetes/apps/o11y/kube-prometheus-stack/app/ocirepository.yaml", "go.mod", "k.yaml"}},
		{name: "a digest names nothing", sources: []string{"https://example.com/" + sha("b")},
			want: []string{"kubernetes/apps/o11y/kube-prometheus-stack/app/ocirepository.yaml", "go.mod", "k.yaml"}},
		{name: "case does not matter", sources: []string{"https://GitHub.com/Prometheus-Community/helm-charts/releases/tag/KUBE-PROMETHEUS-STACK-92.17.0"},
			want: []string{"go.mod", "k.yaml"}},
		{name: "a name inside another's is not the name", sources: []string{"https://github.com/x/sonarr_exporter/releases", "https://github.com/x/kube-prometheus-stack2"},
			want: []string{"kubernetes/apps/o11y/kube-prometheus-stack/app/ocirepository.yaml", "go.mod", "k.yaml"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := paths(Uncovered(bumps, tt.sources)); !slices.Equal(got, tt.want) {
				t.Fatalf("Uncovered = %q, want %q", got, tt.want)
			}
		})
	}
	if got := Uncovered(nil, []string{"https://x"}); got != nil {
		t.Fatalf("Uncovered of none = %+v", got)
	}
}

// TestNeedles: what a source is matched on for a bump: the version; the
// dependency's own name, with its scope or before its major suffix, and the
// nearest directory of the path that is no word every path has; and the
// weak names, the repository or the scope it is under. Never a host, an
// owner or a directory above the nearest.
func TestNeedles(t *testing.T) {
	for _, tt := range []struct {
		name string
		bump Bump
		want needles
	}{
		{name: "an image with a digest", bump: Bump{Path: "k.yaml", New: "ghcr.io/home-operations/sonarr:5.0.0.2907@sha256:" + sha("b"), Key: "k.yaml image: ghcr.io/home-operations/sonarr:*"},
			want: needles{versions: []string{"5.0.0.2907"}, names: []string{"sonarr"}}},
		{name: "a module", bump: Bump{Path: "go.mod", New: "v1.3.0", Key: "go.mod github.com/x/y *"}, want: needles{versions: []string{"1.3.0"}}},
		{name: "a module with a name", bump: Bump{Path: "go.mod", New: "v1.3.0", Key: "go.mod github.com/x/widgets *"},
			want: needles{versions: []string{"1.3.0"}, names: []string{"widgets"}}},
		{name: "a module with a major suffix", bump: Bump{Path: "go.mod", New: "v3.21.0", Key: "go.mod github.com/coreos/go-oidc/* * @2"},
			want: needles{versions: []string{"3.21.0"}, names: []string{"go-oidc"}}},
		{name: "a scoped package", bump: Bump{Path: "package.json", New: "^7.24.0", Key: "package.json @babel/core : ^*"},
			want: needles{versions: []string{"7.24.0"}, names: []string{"babel/core"}, weak: []string{"babel"}}},
		{name: "a module in a repository's tree", bump: Bump{Path: "go.mod", New: "v1.0.1", Key: "go.mod github.com/knadh/koanf/providers/confmap *"},
			want: needles{versions: []string{"1.0.1"}, names: []string{"confmap"}, weak: []string{"koanf"}}},
		{name: "an image named as its directory", bump: Bump{Path: "kubernetes/apps/network/ingress-nginx/app/helmrelease.yaml", New: "ghcr.io/x/ingress-nginx:4.12.0",
			Key: "kubernetes/apps/network/ingress-nginx/app/helmrelease.yaml image: ghcr.io/x/ingress-nginx:*"},
			want: needles{versions: []string{"4.12.0"}, names: []string{"ingress-nginx"}}},
		{name: "a pinned action", bump: Bump{Path: ".github/workflows/ci.yaml", New: "actions/checkout@def5678 v4.2.0", Key: ".github/workflows/ci.yaml - uses: actions/checkout@* # *"},
			want: needles{versions: []string{"4.2.0"}, names: []string{"checkout"}}},
		{name: "a tag under a named path", bump: Bump{Path: "kubernetes/apps/network/ingress-nginx/app/ocirepository.yaml", New: "4.12.0",
			Key: "kubernetes/apps/network/ingress-nginx/app/ocirepository.yaml tag: *"}, want: needles{versions: []string{"4.12.0"}, names: []string{"ingress-nginx"}}},
		{name: "a name line", bump: Bump{Path: "Chart.yaml", New: "1.2.4", Key: `Chart.yaml version: * "frontend"`},
			want: needles{versions: []string{"1.2.4"}, names: []string{"frontend"}}},
		{name: "a name line and inline names", bump: Bump{Path: "Chart.yaml", New: "2.0.1", Key: `Chart.yaml dependencies: name: frontend version: * name: database version: * @9 "stack"`},
			want: needles{versions: []string{"2.0.1"}, names: []string{"stack", "database"}}},
		{name: "a moved token with its own name", bump: Bump{Path: "Dockerfile", New: "database@2.0.1", Key: "Dockerfile RUN npm install frontend@* database@* @5"},
			want: needles{versions: []string{"2.0.1"}, names: []string{"database"}}},
		{name: "a range placeholder is bare", bump: Bump{Path: "package.json", New: "^2.0.1", Key: "package.json frontend : ^* database : ^* @6"},
			want: needles{versions: []string{"2.0.1"}, names: []string{"database"}}},
		{name: "a short image and a short version", bump: Bump{Path: "k.yaml", New: "ghcr.io/x/y:2", Key: "k.yaml image: ghcr.io/x/y:*"},
			want: needles{versions: []string{"2"}}},
		{name: "one package of two on a line", bump: Bump{Path: "Dockerfile", New: "2.0.1", Key: "Dockerfile RUN pip install frontend * database * @7"},
			want: needles{versions: []string{"2.0.1"}, names: []string{"database"}}},
		{name: "an address", bump: Bump{Path: "config.yaml", New: "192.168.1.3", Key: "config.yaml server: *"},
			want: needles{versions: []string{"192.168.1.3"}, names: []string{"server"}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.bump.needles()
			if !slices.Equal(got.versions, tt.want.versions) || !slices.Equal(got.names, tt.want.names) || !slices.Equal(got.weak, tt.want.weak) {
				t.Fatalf("needles = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestUncoveredNamesWhatMoved: a source names the dependency that moved,
// not another on its line, and a short version is matched whole.
func TestUncoveredNamesWhatMoved(t *testing.T) {
	diff := "diff --git a/Dockerfile b/Dockerfile\n--- a/Dockerfile\n+++ b/Dockerfile\n@@ -1 +1 @@\n" +
		"-RUN pip install frontend==1.2.3 database==2.0.0\n+RUN pip install frontend==1.2.3 database==2.0.1\n" +
		"diff --git a/k.yaml b/k.yaml\n--- a/k.yaml\n+++ b/k.yaml\n@@ -1 +1 @@\n-image: ghcr.io/x/y:1\n+image: ghcr.io/x/y:2\n"
	bumps := Bumps(diff)
	if len(bumps) != 2 {
		t.Fatalf("bumps = %+v, want 2", bumps)
	}
	for _, tt := range []struct {
		name    string
		sources []string
		want    int
	}{
		{name: "the unchanged package's source", sources: []string{"https://example.com/frontend/releases"}, want: 2},
		{name: "the moved package's source", sources: []string{"https://example.com/database/releases"}, want: 1},
		{name: "the short image's reference", sources: []string{"oci://ghcr.io/x/y:2"}, want: 1},
		{name: "a longer number is not the short version", sources: []string{"oci://ghcr.io/x/y:22"}, want: 2},
		{name: "a digest does not hold the short version", sources: []string{"oci://ghcr.io/other/thing@sha256:ab2c" + sha("d")[4:]}, want: 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := Uncovered(bumps, tt.sources); len(got) != tt.want {
				t.Fatalf("Uncovered = %+v, want %d", got, tt.want)
			}
		})
	}
}

// TestWeakNames: a scope or a repository covers a bump beside its version
// only, since another dependency can be under them.
func TestWeakNames(t *testing.T) {
	diff := "diff --git a/package.json b/package.json\n--- a/package.json\n+++ b/package.json\n@@ -1,2 +1,2 @@\n" +
		`-    "@types/node": "22.0.0",` + "\n" + `-    "@babel/core": "7.25.0",` + "\n" + `+    "@types/node": "22.1.0",` + "\n" + `+    "@babel/core": "7.26.0",` + "\n" +
		"diff --git a/go.mod b/go.mod\n--- a/go.mod\n+++ b/go.mod\n@@ -1 +1 @@\n-\tgithub.com/knadh/koanf/providers/confmap v1.0.0\n+\tgithub.com/knadh/koanf/providers/confmap v1.0.1\n"
	bumps := Bumps(diff)
	if len(bumps) != 3 {
		t.Fatalf("bumps = %+v, want 3", bumps)
	}
	for _, tt := range []struct {
		name    string
		sources []string
		want    int
	}{
		{name: "another package under the scope", sources: []string{"https://www.npmjs.com/package/@types/react"}, want: 3},
		{name: "the scope's repository with the version", sources: []string{"https://github.com/babel/babel/releases/tag/v7.26.0"}, want: 2},
		{name: "the repository's tree without the version", sources: []string{"https://github.com/knadh/koanf/tree/master"}, want: 3},
		{name: "the repository's tag with the version", sources: []string{"https://github.com/knadh/koanf/releases/tag/providers/confmap/v1.0.1"}, want: 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := Uncovered(bumps, tt.sources); len(got) != tt.want {
				t.Fatalf("Uncovered = %+v, want %d", got, tt.want)
			}
		})
	}
}

// TestHoldsName: a name is held whole, with a hyphen, a dot or a slash
// beside it but no letter, digit or underscore.
func TestHoldsName(t *testing.T) {
	for _, tt := range []struct {
		s, name string
		want    bool
	}{
		{"https://github.com/sonarr/sonarr/releases", "sonarr", true},
		{"https://github.com/x/redis_exporter/releases", "redis", false},
		{"https://github.com/x/redis-ha/releases", "redis", false},
		{"https://github.com/remix-run/react-router/releases/tag/react-router@7.0.0", "react", false},
		{"https://github.com/x/react/releases/tag/react-18.3.0", "react", true},
		{"https://github.com/x/react/releases/tag/react-v18.3.0", "react", true},
		{"https://x/compare/kube-prometheus-stack-92.13.1...kube-prometheus-stack-92.17.0", "kube-prometheus-stack", true},
		{"https://github.com/x/kube-prometheus-stack2", "kube-prometheus-stack", false},
		{"https://github.com/knadh/koanf/tree/master", "koanf", true},
		{"https://npmjs.com/package/@babel/core", "babel/core", true},
		{"", "x", false},
	} {
		t.Run(tt.s, func(t *testing.T) {
			if got := holdsName(tt.s, tt.name); got != tt.want {
				t.Fatalf("holdsName = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestHoldsVersion: a version is held whole, not as part of a longer
// number, and with a suffix after it.
func TestHoldsVersion(t *testing.T) {
	for _, tt := range []struct {
		s, version string
		want       bool
	}{
		{"https://x/releases/tag/v1.3.0", "1.3.0", true},
		{"https://x/releases/tag/v11.3.0", "1.3.0", false},
		{"https://x/releases/tag/v1.3.0.4", "1.3.0", false},
		{"https://x/releases/tag/v1.3.0-rc.1", "1.3.0", true},
		{"https://x/compare/a-1.2.0...a-1.3.0", "1.3.0", true},
		{"https://x/compare/a-1.3.0.1...a-1.3.0", "1.3.0", true},
		{"https://x/sha256:ab2c0000", "2", false},
		{"https://x/y:2", "2", true},
		{"https://x/releases/tag/V2", "2", true},
		{"https://x/dev2", "2", false},
		{"https://x/2alpha", "2", false},
		{"1.3.0", "1.3.0", true},
		{"", "1.3.0", false},
	} {
		t.Run(tt.s, func(t *testing.T) {
			if got := holdsVersion(tt.s, tt.version); got != tt.want {
				t.Fatalf("holdsVersion = %v, want %v", got, tt.want)
			}
		})
	}
}
