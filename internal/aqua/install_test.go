package aqua

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aquaproj/aqua/v2/pkg/config/registry"
	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
)

// entry is one entry of an archive a test builds.
type entry struct {
	name, body, link string
	typ              byte
	mode             int64
}

func file(name, body string) entry {
	return entry{name: name, body: body, typ: tar.TypeReg, mode: 0o755}
}

func tarball(t *testing.T, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Typeflag: e.typ, Mode: e.mode, Linkname: e.link, Size: int64(len(e.body))}
		if e.typ != tar.TypeReg && e.typ != tar.TypeCont {
			h.Size = 0
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Size > 0 {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func gzipped(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func zipped(t *testing.T, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		h.SetMode(fs.FileMode(e.mode))
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sha256Hex(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func sha512Hex(b []byte) string { s := sha512.Sum512(b); return hex.EncodeToString(s[:]) }

// forge serves what an install reads: a release's assets and digests under
// /repos, and files under /dl.
type forge struct {
	releases map[string]any
	files    map[string][]byte
}

func (f forge) serve(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if release, ok := f.releases[r.URL.Path]; ok {
			_ = json.NewEncoder(w).Encode(release)
			return
		}
		if b, ok := f.files[strings.TrimPrefix(r.URL.Path, "/dl/")]; ok {
			_, _ = w.Write(b)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func release(assets ...[2]string) map[string]any {
	list := make([]map[string]string, 0, len(assets))
	for _, a := range assets {
		list = append(list, map[string]string{"name": a[0], "digest": a[1]})
	}
	return map[string]any{"assets": list}
}

func installer(srv *httptest.Server) *Installer {
	return &Installer{Client: srv.Client(), APIURL: srv.URL}
}

// install installs p as a caller does: its checksum first, then the
// download checked against it.
func install(ctx context.Context, in *Installer, p *Package, dir string) (map[string]string, error) {
	sum, err := in.Checksum(ctx, p)
	if err != nil {
		return nil, err
	}
	return in.Install(ctx, p, sum, dir)
}

// leftovers are what an install left beside dir.
func leftovers(t *testing.T, dir string) []string {
	t.Helper()
	got, err := filepath.Glob(filepath.Join(filepath.Dir(dir), ".*-*"))
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestInstall(t *testing.T) {
	tgz := gzipped(t, tarball(t,
		entry{name: "tool_1/", typ: tar.TypeDir, mode: 0o755},
		entry{name: "tool_1/bin/", typ: tar.TypeDir, mode: 0o755},
		file("tool_1/bin/tool", "#!/bin/sh\necho tool\n"),
		entry{name: "tool_1/bin/toolctl", typ: tar.TypeSymlink, link: "tool"},
		entry{name: "tool_1/bin/tool-hard", typ: tar.TypeLink, link: "tool_1/bin/tool"},
		file("./tool_1/LICENSE", "MIT"),
	))
	raw := []byte("#!/bin/sh\necho raw\n")
	zipBody := zipped(t, entry{name: "z/zt", body: "zt", mode: 0o644})
	gz := gzipped(t, []byte("gzt"))
	tests := []struct {
		name  string
		forge forge
		pkg   Package
		want  map[string]string
	}{
		{
			name: "a release's digest",
			forge: forge{
				releases: map[string]any{"/repos/acme/tool/releases/tags/v1.0.0": release([2]string{"tool.tar.gz", "sha256:" + sha256Hex(tgz)})},
				files:    map[string][]byte{"tool.tar.gz": tgz},
			},
			pkg: Package{
				Name: "acme/tool", Version: "v1.0.0", Type: TypeGitHubRelease, Owner: "acme", Repo: "tool", Asset: "tool.tar.gz", Format: "tar.gz",
				Files: []File{{Name: "tool", Src: "tool_1/bin/tool"}, {Name: "toolctl", Src: "tool_1/bin/toolctl"}, {Name: "tool-hard", Src: "tool_1/bin/tool-hard"}},
			},
			want: map[string]string{"tool": "tool_1/bin/tool", "toolctl": "tool_1/bin/toolctl", "tool-hard": "tool_1/bin/tool-hard"},
		},
		{
			name: "a checksum file, the release keeping no digest",
			forge: forge{
				releases: map[string]any{"/repos/acme/raw/releases/tags/1.0.0": release([2]string{"raw_linux", ""})},
				files:    map[string][]byte{"raw_linux": raw, "checksums.txt": fmt.Appendf(nil, "%s  raw_linux\n%s  other\n", sha256Hex(raw), sha256Hex(nil))},
			},
			pkg: Package{
				Name: "acme/raw", Version: "1.0.0", Type: TypeGitHubRelease, Owner: "acme", Repo: "raw", Asset: "raw_linux", Format: FormatRaw,
				Files: []File{{Name: "raw", Src: "raw_linux"}}, ChecksumAlgorithm: "sha256",
			},
			want: map[string]string{"raw": "raw_linux"},
		},
		{
			name:  "http, a sha512 checksum file",
			forge: forge{files: map[string][]byte{"z.zip": zipBody, "z.zip.sha512": []byte(sha512Hex(zipBody) + "\n")}},
			pkg: Package{
				Name: "acme/z", Version: "1.0.0", Type: TypeHTTP, Asset: "z.zip", Format: "zip",
				Files: []File{{Name: "zt", Src: "z/zt"}}, ChecksumAlgorithm: "sha512",
			},
			want: map[string]string{"zt": "z/zt"},
		},
		{
			name: "a compressed file",
			forge: forge{
				releases: map[string]any{"/repos/acme/gzt/releases/tags/1.0.0": release([2]string{"gzt-linux.gz", "sha256:" + sha256Hex(gz)})},
				files:    map[string][]byte{"gzt-linux.gz": gz},
			},
			pkg: Package{
				Name: "acme/gzt", Version: "1.0.0", Type: TypeGitHubRelease, Owner: "acme", Repo: "gzt", Asset: "gzt-linux.gz", Format: "gz",
				Files: []File{{Name: "gzt", Src: "gzt-linux"}},
			},
			want: map[string]string{"gzt": "gzt-linux"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := tt.forge.serve(t)
			p := tt.pkg
			p.URL = srv.URL + "/dl/" + p.Asset
			if p.ChecksumAlgorithm != "" {
				p.checksum = &registry.Checksum{Algorithm: p.ChecksumAlgorithm}
				for name := range tt.forge.files {
					if name != p.Asset {
						p.ChecksumURL = srv.URL + "/dl/" + name
					}
				}
			}
			dir := filepath.Join(t.TempDir(), "pkg")
			got, err := install(t.Context(), installer(srv), &p, dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tt.want) {
				t.Errorf("Install = %v, want %v", got, tt.want)
			}
			for name, src := range tt.want {
				if got[name] != filepath.Join(dir, src) {
					t.Errorf("command %s = %s, want %s", name, got[name], filepath.Join(dir, src))
				}
				info, err := os.Stat(got[name])
				if err != nil || info.Mode()&0o111 == 0 {
					t.Errorf("command %s is not an executable: %v", name, err)
				}
			}
			if left := leftovers(t, dir); len(left) > 0 {
				t.Errorf("the install left %v behind", left)
			}
		})
	}
}

func TestInstallRefused(t *testing.T) {
	tgz := gzipped(t, tarball(t, file("tool", "tool")))
	sums := fmt.Appendf(nil, "%s  tool.tar.gz\n", sha256Hex(tgz))
	tests := []struct {
		name  string
		forge forge
		pkg   Package
		want  error
		says  string
	}{
		{
			name: "a download that does not match its digest",
			forge: forge{
				releases: map[string]any{"/repos/acme/tool/releases/tags/v1.0.0": release([2]string{"tool.tar.gz", "sha256:" + sha256Hex([]byte("other"))})},
				files:    map[string][]byte{"tool.tar.gz": tgz},
			},
			want: ErrChecksum,
			says: ErrDownload.Error(),
		},
		{
			name:  "a version with no release",
			forge: forge{files: map[string][]byte{"tool.tar.gz": tgz}},
			says:  "has no release tagged v1.0.0",
		},
		{
			name: "an asset the release does not have",
			forge: forge{
				releases: map[string]any{"/repos/acme/tool/releases/tags/v1.0.0": release([2]string{"other.tar.gz", "sha256:" + sha256Hex(tgz)})},
				files:    map[string][]byte{"tool.tar.gz": tgz},
			},
			says: "has no asset tool.tar.gz",
		},
		{
			name:  "a version with no release, a checksum file named",
			forge: forge{files: map[string][]byte{"tool.tar.gz": tgz, "checksums.txt": sums}},
			pkg:   Package{ChecksumAlgorithm: "sha256"},
			says:  "has no release tagged v1.0.0",
		},
		{
			name: "an asset the release does not have, a checksum file named",
			forge: forge{
				releases: map[string]any{"/repos/acme/tool/releases/tags/v1.0.0": release([2]string{"other.tar.gz", "sha256:" + sha256Hex(tgz)})},
				files:    map[string][]byte{"tool.tar.gz": tgz, "checksums.txt": sums},
			},
			pkg:  Package{ChecksumAlgorithm: "sha256"},
			says: "has no asset tool.tar.gz",
		},
		{
			name: "no checksum anywhere",
			forge: forge{
				releases: map[string]any{"/repos/acme/tool/releases/tags/v1.0.0": release([2]string{"tool.tar.gz", ""})},
				files:    map[string][]byte{"tool.tar.gz": tgz},
			},
			want: ErrNoChecksum,
		},
		{
			name: "a command the download does not hold",
			forge: forge{
				releases: map[string]any{"/repos/acme/tool/releases/tags/v1.0.0": release([2]string{"tool.tar.gz", "sha256:" + sha256Hex(tgz)})},
				files:    map[string][]byte{"tool.tar.gz": tgz},
			},
			pkg:  Package{Files: []File{{Name: "missing", Src: "bin/missing"}}},
			says: "bin/missing is not in the download",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := tt.forge.serve(t)
			p := Package{
				Name: "acme/tool", Version: "v1.0.0", Type: TypeGitHubRelease, Owner: "acme", Repo: "tool", Asset: "tool.tar.gz",
				URL: srv.URL + "/dl/tool.tar.gz", Format: "tar.gz", Files: []File{{Name: "tool", Src: "tool"}},
			}
			if tt.pkg.Files != nil {
				p.Files = tt.pkg.Files
			}
			if tt.pkg.ChecksumAlgorithm != "" {
				p.ChecksumAlgorithm, p.checksum = tt.pkg.ChecksumAlgorithm, &registry.Checksum{Algorithm: tt.pkg.ChecksumAlgorithm}
				p.ChecksumURL = srv.URL + "/dl/checksums.txt"
			}
			dir := filepath.Join(t.TempDir(), "pkg")
			_, err := install(t.Context(), installer(srv), &p, dir)
			if err == nil {
				t.Fatal("Install succeeded")
			}
			if tt.want != nil && !errors.Is(err, tt.want) {
				t.Errorf("Install error = %v, want %v", err, tt.want)
			}
			if !strings.Contains(err.Error(), tt.says) {
				t.Errorf("Install error = %q, want it to say %q", err, tt.says)
			}
			if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("a failed install left %s: %v", dir, err)
			}
			if left := leftovers(t, dir); len(left) > 0 {
				t.Errorf("a failed install left %v behind", left)
			}
		})
	}
}

// TestInstallRetry: an install that failed leaves nothing in the way of
// the next, here one whose download comes another way and is sound.
func TestInstallRetry(t *testing.T) {
	tgz := gzipped(t, tarball(t, file("tool", "tool")))
	srv := forge{
		releases: map[string]any{"/repos/acme/tool/releases/tags/v1.0.0": release([2]string{"tool.tar.gz", "sha256:" + sha256Hex(tgz)})},
		files:    map[string][]byte{"tool.tar.gz": tgz},
	}.serve(t)
	stale := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("stale")) }))
	t.Cleanup(stale.Close)
	p := &Package{
		Name: "acme/tool", Version: "v1.0.0", Type: TypeGitHubRelease, Owner: "acme", Repo: "tool", Asset: "tool.tar.gz",
		URL: srv.URL + "/dl/tool.tar.gz", Format: "tar.gz", Files: []File{{Name: "tool", Src: "tool"}},
	}
	in := installer(srv)
	sum, err := in.Checksum(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "pkg")
	cached := *in
	cached.Downloads = &http.Client{Transport: redirectTo(stale.URL)}
	if _, err := cached.Install(t.Context(), p, sum, dir); !errors.Is(err, ErrDownload) || !errors.Is(err, ErrChecksum) {
		t.Fatalf("Install from a stale source = %v, want a failed download", err)
	}
	got, err := in.Install(t.Context(), p, sum, dir)
	if err != nil {
		t.Fatalf("Install after a failed one: %v", err)
	}
	if got["tool"] != filepath.Join(dir, "tool") {
		t.Errorf("Install = %v", got)
	}
}

// redirectTo sends every request to its URL, an httptest server's, instead.
type redirectTo string

func (r redirectTo) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.URL.Scheme, req.URL.Host = "http", strings.TrimPrefix(string(r), "http://")
	return http.DefaultTransport.RoundTrip(req)
}

func TestUnpackFormats(t *testing.T) {
	tarBody := tarball(t, file("bin/tool", "tool"))
	var xzBody, zstBody bytes.Buffer
	xw, err := xz.NewWriter(&xzBody)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := xw.Write(tarBody); err != nil {
		t.Fatal(err)
	}
	if err := xw.Close(); err != nil {
		t.Fatal(err)
	}
	zw, err := zstd.NewWriter(&zstBody)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := zw.Write(tarBody); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	for format, body := range map[string][]byte{"tar": tarBody, "tgz": gzipped(t, tarBody), "tar.xz": xzBody.Bytes(), "tar.zst": zstBody.Bytes()} {
		t.Run(format, func(t *testing.T) {
			dir := t.TempDir()
			download := filepath.Join(dir, "download")
			if err := os.WriteFile(download, body, 0o644); err != nil {
				t.Fatal(err)
			}
			root := filepath.Join(dir, "root")
			if err := os.Mkdir(root, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := unpack(t.Context(), download, format, "tool."+format, root); err != nil {
				t.Fatal(err)
			}
			if b, err := os.ReadFile(filepath.Join(root, "bin", "tool")); err != nil || string(b) != "tool" {
				t.Errorf("bin/tool = %q, %v", b, err)
			}
		})
	}
}

func TestUnpackRefused(t *testing.T) {
	tests := []struct {
		name    string
		entries []entry
		says    string
	}{
		{"a path out of the download", []entry{file("../evil", "x")}, "not a path within"},
		{"an absolute path", []entry{file("/etc/evil", "x")}, "not a path within"},
		{"a link out of the download", []entry{{name: "out", typ: tar.TypeSymlink, link: "../../evil"}}, "leads out"},
		{"an absolute link", []entry{{name: "out", typ: tar.TypeSymlink, link: "/etc"}}, "leads out"},
		{
			"a write through a link",
			[]entry{{name: "sub/", typ: tar.TypeDir, mode: 0o755}, {name: "via", typ: tar.TypeSymlink, link: "sub"}, file("via/evil", "x")},
			"as a file and a directory",
		},
		{"a hard link to nothing written", []entry{{name: "hard", typ: tar.TypeLink, link: "missing"}}, "not a file earlier"},
		{"a file over a directory", []entry{{name: "d/", typ: tar.TypeDir, mode: 0o755}, file("d", "x")}, "as a directory and a file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			download := filepath.Join(dir, "download")
			if err := os.WriteFile(download, tarball(t, tt.entries...), 0o644); err != nil {
				t.Fatal(err)
			}
			root := filepath.Join(dir, "root")
			if err := os.Mkdir(root, 0o755); err != nil {
				t.Fatal(err)
			}
			err := unpack(t.Context(), download, "tar", "x.tar", root)
			if err == nil || !strings.Contains(err.Error(), tt.says) {
				t.Errorf("unpack error = %v, want it to say %q", err, tt.says)
			}
			if _, err := os.Lstat(filepath.Join(dir, "evil")); err == nil {
				t.Error("an entry was written out of the download")
			}
		})
	}
}

// TestCommandOutOfDownload refuses a command whose path resolves out of
// the download through links each of which, read alone, stays within it.
func TestCommandOutOfDownload(t *testing.T) {
	dir := t.TempDir()
	download := filepath.Join(dir, "download")
	entries := []entry{{name: "here", typ: tar.TypeSymlink, link: "."}, {name: "cmd", typ: tar.TypeSymlink, link: "here/../secret"}}
	if err := os.WriteFile(download, tarball(t, entries...), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secret"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(dir, "root")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := unpack(t.Context(), download, "tar", "x.tar", root); err != nil {
		t.Fatal(err)
	}
	if err := command(root, "cmd"); err == nil || !strings.Contains(err.Error(), "leads out") {
		t.Errorf("command error = %v, want it to lead out of the download", err)
	}
}

// TestUnpackBounded: what an archive decompresses to is bounded, the
// bodies of the entries it leaves out included.
func TestUnpackBounded(t *testing.T) {
	limit, most := maxUnpackedBytes, maxEntries
	t.Cleanup(func() { maxUnpackedBytes, maxEntries = limit, most })
	body := strings.Repeat("x", 4<<10)
	root := entry{name: "./", typ: tar.TypeDir, mode: 0o755}
	for _, tt := range []struct {
		name    string
		bytes   int64
		entries int
		archive []entry
		says    string
	}{
		{"a file", 1 << 10, most, []entry{file("big", body)}, "over 1024 bytes"},
		{"an entry left out", 1 << 10, most, []entry{{name: "contiguous", body: body, typ: tar.TypeCont, mode: 0o644}}, "over 1024 bytes"},
		{"the root, again", limit, 3, []entry{root, root, root, root}, "over 3 entries"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			maxUnpackedBytes, maxEntries = tt.bytes, tt.entries
			dir := t.TempDir()
			download := filepath.Join(dir, "download")
			if err := os.WriteFile(download, gzipped(t, tarball(t, tt.archive...)), 0o644); err != nil {
				t.Fatal(err)
			}
			root := filepath.Join(dir, "root")
			if err := os.Mkdir(root, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := unpack(t.Context(), download, "tar.gz", "x.tar.gz", root); err == nil || !strings.Contains(err.Error(), tt.says) {
				t.Errorf("unpack error = %v, want %q", err, tt.says)
			}
		})
	}
}

func TestUnpackCanceled(t *testing.T) {
	dir := t.TempDir()
	download := filepath.Join(dir, "download")
	if err := os.WriteFile(download, tarball(t, file("tool", "tool")), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := unpack(ctx, download, "tar", "x.tar", dir); !errors.Is(err, context.Canceled) {
		t.Errorf("unpack error = %v, want it canceled", err)
	}
}
