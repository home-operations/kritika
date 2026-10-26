package aqua

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// fixture is a registry in aqua's format with a package for each way
// Resolve renders one.
const fixture = `packages:
  - type: github_release
    repo_owner: acme
    repo_name: tool
    asset: tool_{{trimV .Version}}_{{.OS}}_{{.Arch}}.{{.Format}}
    format: tar.gz
    replacements:
      amd64: x86_64
    files:
      - name: tool
        src: tool_{{trimV .Version}}/bin/tool
      - name: toolctl
    checksum:
      type: github_release
      asset: "{{.Asset}}.sha256"
      algorithm: sha256
    version_constraint: semver(">= 2.0.0")
    version_overrides:
      - version_constraint: "true"
        asset: tool-{{.Version}}-{{.OS}}-{{.Arch}}.zip
        files:
          - name: tool
  - type: github_release
    repo_owner: acme
    repo_name: rawtool
    asset: rawtool_{{.OS}}_{{.Arch}}
    format: raw
    overrides:
      - goos: linux
        goarch: arm64
        asset: rawtool_linux_aarch64
  - type: github_release
    repo_owner: acme
    repo_name: gzipped
    asset: gzipped-{{.OS}}-{{.Arch}}.{{.Format}}
    format: gz
    files:
      - name: gzipped
        src: gzipped-{{.OS}}-{{.Arch}}
    checksum:
      type: github_release
      asset: checksums.txt
      algorithm: md5
  - type: github_release
    repo_owner: acme
    repo_name: appended
    asset: appended-{{.OS}}
    format: zip
  - type: github_release
    repo_owner: acme
    repo_name: libc
    asset: libc-{{.OS}}.tar.gz
    overrides:
      - goos: linux
        variants:
          - key: libc
            value: musl
        asset: libc-{{.OS}}-musl.tar.gz
  - type: http
    name: acme/web
    url: https://dl.acme.test/web/{{trimV .Version}}/web_{{.OS}}_{{.Arch}}.tar.xz
    checksum:
      type: http
      url: "{{.AssetURL}}.sha512"
      algorithm: sha512
  - type: http
    name: acme/plain
    url: http://dl.acme.test/plain_{{.OS}}
  - type: github_release
    repo_owner: acme
    repo_name: gone
    version_constraint: "false"
    version_overrides:
      - version_constraint: semver("< 1.0.0")
        error_message: too old
      - version_constraint: "true"
        no_asset: true
  - type: github_release
    repo_owner: acme
    repo_name: macos
    asset: macos.tar.gz
    supported_envs:
      - darwin
  - type: go_install
    path: example.com/acme/built
    name: acme/built
  - type: github_release
    repo_owner: acme
    repo_name: linked
    asset: linked.tar.gz
    files:
      - name: linked
        link: ../lib/linked
  - type: github_release
    repo_owner: acme
    repo_name: odd
    asset: odd_{{.OS}}.AppImage
  - type: github_release
    repo_owner: acme
    repo_name: disk
    asset: disk.dmg
  - type: github_release
    repo_owner: acme
    repo_name: escape
    asset: escape.tar.gz
    files:
      - name: escape
        src: ../escape
`

func fixtureRegistry(t *testing.T) *Registry {
	t.Helper()
	r, err := Parse([]byte(fixture))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

var linuxAMD64 = Platform{OS: "linux", Arch: "amd64", LibC: "glibc"}

func TestResolve(t *testing.T) {
	r := fixtureRegistry(t)
	tests := []struct {
		name, pkg, version string
		platform           Platform
		want               Package
	}{
		{
			name: "replacements, file src and a release checksum", pkg: "acme/tool", version: "v2.1.0", platform: linuxAMD64,
			want: Package{
				Name: "acme/tool", Version: "v2.1.0", Type: TypeGitHubRelease, Owner: "acme", Repo: "tool",
				Asset: "tool_2.1.0_linux_x86_64.tar.gz", URL: "https://github.com/acme/tool/releases/download/v2.1.0/tool_2.1.0_linux_x86_64.tar.gz",
				Format: "tar.gz", Files: []File{{Name: "tool", Src: "tool_2.1.0/bin/tool"}, {Name: "toolctl", Src: "toolctl"}},
				ChecksumURL:       "https://github.com/acme/tool/releases/download/v2.1.0/tool_2.1.0_linux_x86_64.tar.gz.sha256",
				ChecksumAlgorithm: "sha256",
			},
		},
		{
			name: "a version override, its extension's format over the declared one", pkg: "acme/tool", version: "v1.0.0", platform: linuxAMD64,
			want: Package{
				Name: "acme/tool", Version: "v1.0.0", Type: TypeGitHubRelease, Owner: "acme", Repo: "tool",
				Asset: "tool-v1.0.0-linux-x86_64.zip", URL: "https://github.com/acme/tool/releases/download/v1.0.0/tool-v1.0.0-linux-x86_64.zip",
				Format: "zip", Files: []File{{Name: "tool", Src: "tool"}},
				ChecksumURL:       "https://github.com/acme/tool/releases/download/v1.0.0/tool-v1.0.0-linux-x86_64.zip.sha256",
				ChecksumAlgorithm: "sha256",
			},
		},
		{
			name: "raw, its command the download", pkg: "acme/rawtool", version: "v1.0.0", platform: linuxAMD64,
			want: Package{
				Name: "acme/rawtool", Version: "v1.0.0", Type: TypeGitHubRelease, Owner: "acme", Repo: "rawtool",
				Asset: "rawtool_linux_amd64", URL: "https://github.com/acme/rawtool/releases/download/v1.0.0/rawtool_linux_amd64",
				Format: FormatRaw, Files: []File{{Name: "rawtool", Src: "rawtool_linux_amd64"}},
			},
		},
		{
			name: "a platform override", pkg: "acme/rawtool", version: "v1.0.0", platform: Platform{OS: "linux", Arch: "arm64", LibC: "glibc"},
			want: Package{
				Name: "acme/rawtool", Version: "v1.0.0", Type: TypeGitHubRelease, Owner: "acme", Repo: "rawtool",
				Asset: "rawtool_linux_aarch64", URL: "https://github.com/acme/rawtool/releases/download/v1.0.0/rawtool_linux_aarch64",
				Format: FormatRaw, Files: []File{{Name: "rawtool", Src: "rawtool_linux_aarch64"}},
			},
		},
		{
			name: "a compressed file, its md5 checksum file left unread", pkg: "acme/gzipped", version: "1.0.0", platform: linuxAMD64,
			want: Package{
				Name: "acme/gzipped", Version: "1.0.0", Type: TypeGitHubRelease, Owner: "acme", Repo: "gzipped",
				Asset: "gzipped-linux-amd64.gz", URL: "https://github.com/acme/gzipped/releases/download/1.0.0/gzipped-linux-amd64.gz",
				Format: "gz", Files: []File{{Name: "gzipped", Src: "gzipped-linux-amd64"}},
			},
		},
		{
			name: "the declared format's extension appended", pkg: "acme/appended", version: "1.0.0", platform: linuxAMD64,
			want: Package{
				Name: "acme/appended", Version: "1.0.0", Type: TypeGitHubRelease, Owner: "acme", Repo: "appended",
				Asset: "appended-linux.zip", URL: "https://github.com/acme/appended/releases/download/1.0.0/appended-linux.zip",
				Format: "zip", Files: []File{{Name: "appended", Src: "appended"}},
			},
		},
		{
			name: "a libc override", pkg: "acme/libc", version: "1.0.0", platform: Platform{OS: "linux", Arch: "amd64", LibC: "musl"},
			want: Package{
				Name: "acme/libc", Version: "1.0.0", Type: TypeGitHubRelease, Owner: "acme", Repo: "libc",
				Asset: "libc-linux-musl.tar.gz", URL: "https://github.com/acme/libc/releases/download/1.0.0/libc-linux-musl.tar.gz",
				Format: "tar.gz", Files: []File{{Name: "libc", Src: "libc"}},
			},
		},
		{
			name: "http, with a checksum URL from the asset's", pkg: "acme/web", version: "v3.2.1", platform: linuxAMD64,
			want: Package{
				Name: "acme/web", Version: "v3.2.1", Type: TypeHTTP,
				Asset: "web_linux_amd64.tar.xz", URL: "https://dl.acme.test/web/3.2.1/web_linux_amd64.tar.xz",
				Format: "tar.xz", Files: []File{{Name: "web", Src: "web"}},
				ChecksumURL: "https://dl.acme.test/web/3.2.1/web_linux_amd64.tar.xz.sha512", ChecksumAlgorithm: "sha512",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := r.Resolve(tt.pkg, tt.version, tt.platform)
			if err != nil {
				t.Fatal(err)
			}
			got.checksum, got.tmpl = nil, templater{}
			if !reflect.DeepEqual(*got, tt.want) {
				t.Errorf("Resolve =\n%+v\nwant\n%+v", *got, tt.want)
			}
		})
	}
}

func TestResolveRefused(t *testing.T) {
	r := fixtureRegistry(t)
	tests := []struct {
		name, pkg, version string
		want               error
		says               string
	}{
		{"not in the registry", "acme/missing", "1.0.0", ErrNotFound, ""},
		{"an error message for the version", "acme/gone", "0.1.0", ErrUnsupported, "too old"},
		{"no download for the version", "acme/gone", "1.0.0", ErrUnsupported, "no download"},
		{"another platform", "acme/macos", "1.0.0", ErrUnsupported, "supports darwin"},
		{"built from source", "acme/built", "v1.0.0", ErrUnsupported, "go_install"},
		{"a linked command", "acme/linked", "1.0.0", ErrUnsupported, "link"},
		{"an unknown extension", "acme/odd", "1.0.0", ErrUnsupported, "cannot tell"},
		{"a format the runner does not unpack", "acme/disk", "1.0.0", ErrUnsupported, "dmg"},
		{"a src out of the download", "acme/escape", "1.0.0", ErrUnsupported, "not a path within"},
		{"plain HTTP", "acme/plain", "1.0.0", ErrUnsupported, "https://"},
		{"Windows", "acme/rawtool", "1.0.0", ErrUnsupported, "Windows"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			platform := linuxAMD64
			if tt.name == "Windows" {
				platform = Platform{OS: "windows", Arch: "amd64"}
			}
			_, err := r.Resolve(tt.pkg, tt.version, platform)
			if !errors.Is(err, tt.want) {
				t.Fatalf("Resolve error = %v, want %v", err, tt.want)
			}
			if !strings.Contains(err.Error(), tt.says) {
				t.Errorf("Resolve error = %q, want it to say %q", err, tt.says)
			}
		})
	}
}

// TestResolveLeavesRegistry: resolving a package for one platform leaves
// the registry as it was for the next, a package with no
// version_constraint included, whose overrides aqua applies in place, and
// resolutions may run at once.
func TestResolveLeavesRegistry(t *testing.T) {
	r := fixtureRegistry(t)
	arm := Platform{OS: "linux", Arch: "arm64", LibC: "glibc"}
	resolve := func(p Platform) string {
		pkg, err := r.Resolve("acme/rawtool", "v1.0.0", p)
		if err != nil {
			t.Error(err)
			return ""
		}
		return pkg.Asset
	}
	if got := resolve(arm); got != "rawtool_linux_aarch64" {
		t.Fatalf("arm64 asset = %s", got)
	}
	if got := resolve(linuxAMD64); got != "rawtool_linux_amd64" {
		t.Fatalf("amd64 asset after arm64's = %s", got)
	}
	var wg sync.WaitGroup
	for i := range 8 {
		p, want := linuxAMD64, "rawtool_linux_amd64"
		if i%2 == 1 {
			p, want = arm, "rawtool_linux_aarch64"
		}
		wg.Go(func() {
			if got := resolve(p); got != want {
				t.Errorf("%s asset = %s, want %s", p.Arch, got, want)
			}
		})
	}
	wg.Wait()
}

func TestFetchRegistry(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer run-token" {
			http.Error(w, "the run token is not valid", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(fixture))
	}))
	t.Cleanup(srv.Close)
	r, err := FetchRegistry(t.Context(), srv.Client(), srv.URL, http.Header{"Authorization": {"Bearer run-token"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Resolve("acme/rawtool", "v1.0.0", linuxAMD64); err != nil {
		t.Error(err)
	}
	if _, err := FetchRegistry(t.Context(), srv.Client(), srv.URL, nil); err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("FetchRegistry without the token = %v, want the 401", err)
	}
}
