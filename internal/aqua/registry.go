// Package aqua resolves packages of the aqua registry
// (github.com/aquaproj/aqua-registry) at the release RegistryVersion
// names, as aqua itself would for the platform the runner runs on, and
// installs them: it downloads a package, checks the download against a
// checksum published apart from it, and unpacks it, running nothing of
// aqua's or the package's.
package aqua

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/aquaproj/aqua/v2/pkg/config/registry"
	"github.com/aquaproj/aqua/v2/pkg/runtime"
	"go.yaml.in/yaml/v4"
)

// maxRegistryBytes bounds the registry's fetch, many times its size.
const maxRegistryBytes = 64 << 20

// registryURL is where RegistryVersion's registry.yaml is, and
// registrySum the SHA-256 it must have; variables for the tests.
var (
	registryURL = "https://raw.githubusercontent.com/aquaproj/aqua-registry/" + RegistryVersion + "/registry.yaml"
	registrySum = registrySHA256
)

// discard takes aqua's own logging, which is for its CLI.
var discard = slog.New(slog.DiscardHandler)

var (
	// ErrNotFound is a package the registry does not have.
	ErrNotFound = errors.New("not in the aqua registry")
	// ErrUnsupported is a package the runner does not install: one that
	// builds from source, whose download it cannot unpack or lay out, or
	// that has no download for the version or platform.
	ErrUnsupported = errors.New("not installable")
)

// The package types the runner installs: a GitHub release's asset and a
// file at a URL. The others build from source (go_install, go_build,
// cargo), take a repository's own files (github_content, github_archive)
// or are on another forge.
const (
	TypeGitHubRelease = "github_release"
	TypeHTTP          = "http"
)

// Registry is an aqua registry, parsed.
type Registry struct {
	config *registry.Config
}

// FetchRegistry fetches RegistryVersion's registry with client, checked
// against the SHA-256 recorded with the release.
func FetchRegistry(ctx context.Context, client *http.Client) (*Registry, error) {
	data, err := get(ctx, client, registryURL, nil, maxRegistryBytes)
	if err != nil {
		return nil, fmt.Errorf("aqua: registry %s: %w", RegistryVersion, err)
	}
	if sum := sha256.Sum256(data); hex.EncodeToString(sum[:]) != registrySum {
		return nil, fmt.Errorf("aqua: registry %s: %w: sha256 %x, want %s", RegistryVersion, ErrChecksum, sum, registrySum)
	}
	return Parse(data)
}

// Parse reads data as an aqua registry.
func Parse(data []byte) (*Registry, error) {
	r := &Registry{config: &registry.Config{}}
	if err := yaml.Unmarshal(data, r.config); err != nil {
		return nil, fmt.Errorf("aqua: registry: %w", err)
	}
	// The index by name is built on first use and kept; building it here
	// leaves lookups only reading it, so they may run at once.
	r.config.Packages(discard)
	return r, nil
}

// Platform is what a package is resolved for: GOOS and GOARCH, and the C
// library on Linux, "glibc" or "musl" as aqua names them. Windows is not
// one: aqua adds .exe to its names, which Resolve does not mirror, since a
// runner never runs there.
type Platform struct {
	OS, Arch, LibC string
}

// Package is a registry package resolved for one version and platform:
// what to download, how it is packed and the commands it holds.
type Package struct {
	// Name is the package's name in the registry, and Version the release
	// it is resolved at, as the release's tag reads.
	Name, Version string
	// Type is TypeGitHubRelease or TypeHTTP.
	Type string
	// Owner and Repo are the GitHub repository whose release a
	// TypeGitHubRelease package is an asset of.
	Owner, Repo string
	// Asset is the download's file name, and URL where it is.
	Asset, URL string
	// Format is how the download is packed: FormatRaw for the executable
	// itself, else one of the archive or compression formats Unpack reads.
	Format string
	// Files are the package's commands, each with its path in the unpacked
	// download.
	Files []File
	// ChecksumURL is where the registry has the download's checksum
	// published apart from it, and ChecksumAlgorithm what it is; "" when
	// the registry names none the runner reads.
	ChecksumURL, ChecksumAlgorithm string
	checksum                       *registry.Checksum
}

// File is one of a package's commands.
type File struct {
	// Name is the command.
	Name string
	// Src is its path in the unpacked download, slash-separated.
	Src string
}

// commandName is what a command's name may be: a file name, as PATH finds
// it.
var commandName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*$`)

// Resolve resolves the package name at version for p.
func (r *Registry) Resolve(name, version string, p Platform) (*Package, error) {
	info := r.config.Package(discard, name)
	if info == nil {
		return nil, fmt.Errorf("aqua: %s: %w", name, ErrNotFound)
	}
	unsupported := func(format string, args ...any) error {
		return fmt.Errorf("aqua: %s@%s: %w: %s", name, version, ErrUnsupported, fmt.Sprintf(format, args...))
	}
	if p.OS == "windows" {
		return nil, unsupported("the runner does not install for Windows")
	}
	rt := &runtime.Runtime{GOOS: p.OS, GOARCH: p.Arch, LibC: p.LibC}
	// Overriding by the platform sets fields of the package it is given,
	// which for a package with no version_constraint is the registry's own.
	info, err := info.Copy().Override(discard, version, rt)
	if err != nil {
		return nil, fmt.Errorf("aqua: %s@%s: %w", name, version, err)
	}
	switch {
	case info.ErrorMessage != "":
		return nil, unsupported("%s", info.ErrorMessage)
	case info.NoAsset:
		return nil, unsupported("the version has no download")
	case !info.CheckSupportedEnvs(rt.GOOS, rt.GOARCH, rt.Env()):
		return nil, unsupported("it supports %s, not %s", strings.Join(info.SupportedEnvs, ", "), rt.Env())
	case info.Type != TypeGitHubRelease && info.Type != TypeHTTP:
		return nil, unsupported("its %s type is not one the runner installs", info.Type)
	case len(info.Vars) > 0:
		return nil, unsupported("it takes variables")
	}
	if err := info.Validate(); err != nil {
		return nil, unsupported("%v", err)
	}
	pkg := &Package{Name: name, Version: version, Type: info.Type, Owner: info.RepoOwner, Repo: info.RepoName, checksum: info.Checksum}
	t := templater{info: info, rt: rt, version: version}
	if pkg.Asset, pkg.URL, err = t.download(); err != nil {
		return nil, unsupported("%v", err)
	}
	if pkg.Format, err = format(info.GetFormat(), pkg.Asset); err != nil {
		return nil, unsupported("%v", err)
	}
	for _, f := range info.GetFiles() {
		file, err := t.file(f, pkg.Asset, pkg.Format)
		if err != nil {
			return nil, unsupported("command %s: %v", f.Name, err)
		}
		pkg.Files = append(pkg.Files, file)
	}
	if len(pkg.Files) == 0 {
		return nil, unsupported("it names no command")
	}
	if info.Checksum.GetEnabled() && slices.Contains(checksumAlgorithms, info.Checksum.GetAlgorithm()) {
		if pkg.ChecksumURL, err = t.checksumURL(pkg.Asset, pkg.URL); err != nil {
			return nil, unsupported("checksum: %v", err)
		}
		if pkg.ChecksumURL != "" {
			pkg.ChecksumAlgorithm = info.Checksum.GetAlgorithm()
		}
	}
	return pkg, nil
}

// releaseURL is where a GitHub release's asset downloads from, as aqua
// builds it: the tag as it reads, slashes included.
func releaseURL(owner, repo, tag, asset string) string {
	return fmt.Sprintf("https://github.com/%s/%s/releases/download/%s/%s", owner, repo, tag, asset)
}

// relativeSrc reports whether src stays within the directory it is
// relative to.
func relativeSrc(src string) bool {
	clean := path.Clean(src)
	return src != "" && !path.IsAbs(clean) && clean != ".." && !strings.HasPrefix(clean, "../")
}
