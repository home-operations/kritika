package aqua

import (
	"errors"
	"fmt"
	neturl "net/url"
	"path"
	"path/filepath"
	"strings"

	"github.com/aquaproj/aqua/v2/pkg/config/registry"
	"github.com/aquaproj/aqua/v2/pkg/runtime"
	"github.com/aquaproj/aqua/v2/pkg/template"
)

// FormatRaw is a download that is the executable itself.
const FormatRaw = "raw"

// formatExtensions are the archive and compression formats that aqua tells
// from a file name's extension, in the order it tries them. So tar.gz is
// found before gz (aqua's pkg/asset.RemoveExtFromAsset).
var formatExtensions = []string{
	"tar.br", "tar.bz2", "tar.gz", "tar.lz4", "tar.sz", "tar.xz", "tbr", "tbz", "tbz2", "tgz", "tlz4", "tsz", "txz",
	"tar.zst", "zip", "gz", "bz2", "lz4", "sz", "xz", "zst", "dmg", "pkg", "rar", "tar",
}

// The algorithms of a checksum that the runner makes sure a download matches.
const (
	algorithmSHA256 = "sha256"
	algorithmSHA512 = "sha512"
)

var checksumAlgorithms = []string{algorithmSHA256, algorithmSHA512}

// templater renders a package's templates for one version and platform as
// aqua's pkg/config does: the download, the commands' paths and the
// checksum file. kritika links aqua's registry and template packages but
// not pkg/config, for the archive libraries that come with it.
type templater struct {
	info    *registry.PackageInfo
	rt      *runtime.Runtime
	version string
}

// data is what every template reads, with replacements for the names of
// the OS and architecture.
func (t templater) data(replacements registry.Replacements) map[string]any {
	return map[string]any{
		"Version": t.version,
		"SemVer":  strings.TrimPrefix(t.version, t.info.VersionPrefix),
		"GOOS":    t.rt.GOOS,
		"GOARCH":  t.rt.GOARCH,
		"OS":      replace(t.rt.GOOS, replacements),
		"Arch":    t.arch(replacements),
		"Format":  t.info.GetFormat(),
	}
}

// arch is the architecture a template names: amd64's assets where the
// package runs under Rosetta 2 or Windows' emulation, as replaced.
func (t templater) arch(replacements registry.Replacements) string {
	if t.info.Rosetta2 && t.rt.GOOS == "darwin" && t.rt.GOARCH == "arm64" ||
		t.info.WindowsARMEmulation && t.rt.GOOS == "windows" && t.rt.GOARCH == "arm64" {
		return replace("amd64", replacements)
	}
	return replace(t.rt.GOARCH, replacements)
}

func replace(key string, replacements registry.Replacements) string {
	if r := replacements[key]; r != "" {
		return r
	}
	return key
}

// download is the asset's file name and the URL it downloads from.
func (t templater) download() (asset, url string, err error) {
	data := t.data(t.info.Replacements)
	switch t.info.Type {
	case TypeGitHubRelease:
		if asset, err = template.Execute(t.info.Asset, data); err != nil {
			return "", "", fmt.Errorf("asset: %w", err)
		}
		asset = t.appendExt(asset)
		if asset == "" || strings.Contains(asset, "/") {
			return "", "", fmt.Errorf("asset %q is not a file name", asset)
		}
		return asset, releaseURL(t.info.RepoOwner, t.info.RepoName, t.version, asset), nil
	default:
		if url, err = template.Execute(t.info.URL, data); err != nil {
			return "", "", fmt.Errorf("url: %w", err)
		}
		url = t.appendExt(url)
		u, err := httpsURL(url)
		if err != nil {
			return "", "", err
		}
		if asset = path.Base(u.Path); asset == "/" || asset == "." {
			return "", "", fmt.Errorf("url %s names no file", url)
		}
		return asset, url, nil
	}
}

// appendExt adds the declared format's extension to s, an asset or URL
// that has none of an archive's, as aqua does unless append_ext is off.
func (t templater) appendExt(s string) string {
	f := t.info.Format
	if !t.info.GetAppendExt() || f == "" || f == FormatRaw || extFormat(s) != "" || strings.HasSuffix(s, "."+f) {
		return s
	}
	return s + "." + f
}

// extFormat is the format asset's extension names, "" for none.
func extFormat(asset string) string {
	for _, f := range formatExtensions {
		if strings.HasSuffix(asset, "."+f) {
			return f
		}
	}
	return ""
}

// format is how asset is packed, as aqua tells: raw when declared so,
// else what its extension names, else the declared format, else raw for
// a name with no extension. aqua reads an archive by its name and
// content, so a declared format only says whether there is one.
func format(declared, asset string) (string, error) {
	f := declared
	if ext := extFormat(asset); ext != "" && f != FormatRaw {
		f = ext
	}
	if f == "" {
		if filepath.Ext(asset) != "" {
			return "", fmt.Errorf("its download %s is packed in a way the runner cannot tell", asset)
		}
		f = FormatRaw
	}
	if !unpackable(f) {
		return "", fmt.Errorf("its download is packed as %s, which the runner does not unpack", f)
	}
	return f, nil
}

// file is f, one of the package's commands, with its path in the unpacked
// download: the download itself when raw, else its src, rendered, or its
// name.
func (t templater) file(f *registry.File, asset, format string) (File, error) {
	switch {
	case !commandName.MatchString(f.Name):
		return File{}, fmt.Errorf("%q is not a command name", f.Name)
	case f.Dir != "" || f.Link != "" || f.Hard:
		return File{}, errors.New("it is laid out by a link or a directory, which the runner does not do")
	case format == FormatRaw:
		return File{Name: f.Name, Src: asset}, nil
	case f.Src == "":
		return File{Name: f.Name, Src: f.Name}, nil
	}
	data := t.data(t.info.Replacements)
	data["FileName"], data["Asset"] = f.Name, asset
	data["AssetWithoutExt"] = strings.TrimSuffix(asset, "."+extFormat(asset))
	src, err := template.Execute(f.Src, data)
	if err != nil {
		return File{}, fmt.Errorf("src: %w", err)
	}
	if !relativeSrc(src) {
		return File{}, fmt.Errorf("src %q is not a path within the download", src)
	}
	return File{Name: f.Name, Src: path.Clean(src)}, nil
}

// checksumURL is where the checksum file that the registry names for asset,
// downloaded from url, is. It is "" when the registry names one of a type
// that the runner does not fetch.
func (t templater) checksumURL(asset, url string) (string, error) {
	c := t.info.Checksum
	data := t.data(t.info.GetChecksumReplacements())
	switch c.Type {
	case TypeGitHubRelease:
		data["Asset"] = asset
		name, err := template.Execute(c.Asset, data)
		if err != nil {
			return "", err
		}
		if name == "" || strings.Contains(name, "/") {
			return "", fmt.Errorf("asset %q is not a file name", name)
		}
		return releaseURL(t.info.RepoOwner, t.info.RepoName, t.version, name), nil
	case TypeHTTP:
		if t.info.Type == TypeHTTP {
			data["AssetURL"] = url
		}
		u, err := template.Execute(c.URL, data)
		if err != nil {
			return "", err
		}
		if _, err := httpsURL(u); err != nil {
			return "", err
		}
		return u, nil
	}
	return "", nil
}

// httpsURL is s parsed, when it is an https:// URL with a host.
func httpsURL(s string) (*neturl.URL, error) {
	u, err := neturl.Parse(s)
	if err != nil {
		return nil, fmt.Errorf("url: %w", err)
	}
	if u.Scheme != "https" || u.Host == "" {
		return nil, fmt.Errorf("url %s is not an https:// URL", s)
	}
	return u, nil
}
