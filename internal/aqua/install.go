package aqua

import (
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"maps"
	"net/http"
	neturl "net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/aquaproj/aqua/v2/pkg/checksum"
)

// What bounds an install: maxDownloadBytes a package's download, the
// largest the registry's tools ship being a few hundred MiB;
// maxChecksumFileBytes a checksum file, which lists a release's assets;
// and maxReleaseBytes the GitHub API's description of a release.
const (
	maxDownloadBytes     = 1 << 30
	maxChecksumFileBytes = 4 << 20
	maxReleaseBytes      = 16 << 20
)

var (
	// ErrNoChecksum is a download nothing apart from it gives a checksum
	// for: neither its GitHub release nor a checksum file the registry
	// names.
	ErrNoChecksum = errors.New("no checksum is published for the download")
	// ErrDownload is a download that could not be fetched, or, with
	// ErrChecksum, did not match its checksum: what another way to fetch
	// it may get right.
	ErrDownload = errors.New("the download failed")
	// ErrChecksum is a download that does not match its checksum.
	ErrChecksum = errors.New("the download does not match its checksum")
)

// Installer installs resolved packages.
type Installer struct {
	// Client fetches what a download is checked against: the GitHub REST
	// API's account of a release at APIURL, with Token as its credential
	// when set, and a checksum file.
	Client *http.Client
	APIURL string
	Token  string
	// Downloads fetches the download itself; nil takes Client.
	Downloads *http.Client
}

// Sum is a checksum: its algorithm and value, in hex.
type Sum struct {
	Algorithm, Hex string
}

// Install downloads p, checks the download against sum, its Checksum,
// before anything of it is unpacked, and unpacks it into dir, which must
// not exist. The download and the unpacking are kept beside dir until the
// package is whole, so dir is the package or nothing. It returns where
// the package's commands are, by name.
func (in *Installer) Install(ctx context.Context, p *Package, sum Sum, dir string) (map[string]string, error) {
	download, err := os.CreateTemp(filepath.Dir(dir), ".download-*")
	if err != nil {
		return nil, fmt.Errorf("aqua: %s: %w", p.Name, err)
	}
	defer func() { _ = os.Remove(download.Name()) }()
	err = in.download(ctx, p.URL, sum, download)
	if cerr := download.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, fmt.Errorf("aqua: %s: %w: %w", p.Name, ErrDownload, err)
	}
	staging, err := os.MkdirTemp(filepath.Dir(dir), ".unpack-*")
	if err != nil {
		return nil, fmt.Errorf("aqua: %s: %w", p.Name, err)
	}
	defer func() { _ = os.RemoveAll(staging) }()
	if err := unpack(ctx, download.Name(), p.Format, p.Asset, staging); err != nil {
		return nil, fmt.Errorf("aqua: %s: unpack %s: %w", p.Name, p.Asset, err)
	}
	for _, f := range p.Files {
		if err := command(staging, f.Src); err != nil {
			return nil, fmt.Errorf("aqua: %s: command %s: %w", p.Name, f.Name, err)
		}
	}
	if err := os.Chmod(staging, 0o755); err != nil {
		return nil, fmt.Errorf("aqua: %s: %w", p.Name, err)
	}
	if err := os.Rename(staging, dir); err != nil {
		return nil, fmt.Errorf("aqua: %s: %w", p.Name, err)
	}
	commands := make(map[string]string, len(p.Files))
	for _, f := range p.Files {
		commands[f.Name] = filepath.Join(dir, filepath.FromSlash(f.Src))
	}
	return commands, nil
}

// Checksum is what p's download must match: the digest GitHub keeps of a
// release's asset, else the one in the checksum file the registry names.
// Looking the release up also checks the version is one of its tags,
// which the registry cannot tell: it resolves any version. A release
// GitHub says it does not have, or one without the asset, fails though a
// checksum file is named; one that keeps no digest, or that GitHub could
// not be asked about, leaves the file to stand in.
func (in *Installer) Checksum(ctx context.Context, p *Package) (Sum, error) {
	var releaseErr error
	if p.Type == TypeGitHubRelease {
		sum, err := in.releaseDigest(ctx, p)
		_, missing := errors.AsType[unreleased](err)
		switch {
		case err == nil && sum.Hex != "":
			return sum, nil
		case missing || err != nil && p.ChecksumURL == "":
			return Sum{}, fmt.Errorf("aqua: %s: %w", p.Name, err)
		}
		releaseErr = err
	}
	if p.ChecksumURL == "" {
		return Sum{}, fmt.Errorf("aqua: %s: %w", p.Name, ErrNoChecksum)
	}
	content, err := get(ctx, in.Client, p.ChecksumURL, nil, maxChecksumFileBytes)
	if err != nil {
		return Sum{}, fmt.Errorf("aqua: %s: checksum file: %w", p.Name, errors.Join(err, releaseErr))
	}
	hexSum, err := checksum.GetChecksum(discard, p.Asset, string(content), p.checksum)
	if err != nil {
		return Sum{}, fmt.Errorf("aqua: %s: checksum file %s: %w", p.Name, p.ChecksumURL, err)
	}
	return Sum{Algorithm: p.ChecksumAlgorithm, Hex: strings.ToLower(hexSum)}, nil
}

// releaseDigest is the digest GitHub keeps of p's asset, with no Hex when
// the release predates GitHub keeping them.
func (in *Installer) releaseDigest(ctx context.Context, p *Package) (Sum, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/releases/tags/%s", strings.TrimSuffix(in.APIURL, "/"),
		neturl.PathEscape(p.Owner), neturl.PathEscape(p.Repo), neturl.PathEscape(p.Version))
	header := http.Header{"Accept": {"application/vnd.github+json"}, "X-Github-Api-Version": {"2022-11-28"}}
	if in.Token != "" {
		header.Set("Authorization", "Bearer "+in.Token)
	}
	body, err := get(ctx, in.Client, url, header, maxReleaseBytes)
	if errors.Is(err, errNotFound) {
		return Sum{}, unreleased(fmt.Sprintf("%s/%s has no release tagged %s", p.Owner, p.Repo, p.Version))
	}
	if err != nil {
		return Sum{}, fmt.Errorf("release %s: %w", p.Version, err)
	}
	var release struct {
		Assets []struct {
			Name   string `json:"name"`
			Digest string `json:"digest"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(body, &release); err != nil {
		return Sum{}, fmt.Errorf("release %s: %w", p.Version, err)
	}
	for _, a := range release.Assets {
		if a.Name != p.Asset {
			continue
		}
		if algorithm, value, ok := strings.Cut(a.Digest, ":"); ok && algorithm == algorithmSHA256 && value != "" {
			return Sum{Algorithm: algorithm, Hex: strings.ToLower(value)}, nil
		}
		return Sum{}, nil
	}
	return Sum{}, unreleased(fmt.Sprintf("release %s of %s/%s has no asset %s", p.Version, p.Owner, p.Repo, p.Asset))
}

// unreleased is a version GitHub has no release of, or a release without
// the asset: GitHub's word on it, which no checksum file overrules.
type unreleased string

func (e unreleased) Error() string { return string(e) }

// errNotFound is a 404 from get.
var errNotFound = errors.New("not found")

// get is url's body, at most limit bytes of it, fetched with client.
func get(ctx context.Context, client *http.Client, url string, header http.Header, limit int64) ([]byte, error) {
	resp, err := request(ctx, client, url, header)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", url, err)
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("%s: over %d bytes", url, limit)
	}
	return body, nil
}

// request GETs url with client and header, failing on any status but 200.
func request(ctx context.Context, client *http.Client, url string, header http.Header) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	maps.Copy(req.Header, header)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound {
			return nil, fmt.Errorf("%s: %w", url, errNotFound)
		}
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	return resp, nil
}

// download writes url's body to w, at most maxDownloadBytes of it, and
// checks it matches sum.
func (in *Installer) download(ctx context.Context, url string, sum Sum, w io.Writer) error {
	h, err := hasher(sum.Algorithm)
	if err != nil {
		return err
	}
	client := in.Downloads
	if client == nil {
		client = in.Client
	}
	resp, err := request(ctx, client, url, nil)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	n, err := io.Copy(io.MultiWriter(w, h), io.LimitReader(resp.Body, maxDownloadBytes+1))
	switch {
	case err != nil:
		return fmt.Errorf("%s: %w", url, err)
	case n > maxDownloadBytes:
		return fmt.Errorf("%s: over %d bytes", url, maxDownloadBytes)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != sum.Hex {
		return fmt.Errorf("%s: %w: %s %s, want %s", url, ErrChecksum, sum.Algorithm, got, sum.Hex)
	}
	return nil
}

func hasher(algorithm string) (hash.Hash, error) {
	switch algorithm {
	case algorithmSHA256:
		return sha256.New(), nil
	case algorithmSHA512:
		return sha512.New(), nil
	}
	return nil, fmt.Errorf("checksum algorithm %q is not one the runner checks", algorithm)
}

// command makes the command at src in root, the unpacked download,
// executable: a regular file there, or a link that resolves to one within
// root, as it will once root moves, since it leads nowhere out of it.
func command(root, src string) error {
	resolved, err := filepath.EvalSymlinks(filepath.Join(root, filepath.FromSlash(src)))
	if err != nil {
		return fmt.Errorf("%s is not in the download", src)
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	if rel, err := filepath.Rel(realRoot, resolved); err != nil || !filepath.IsLocal(rel) {
		return fmt.Errorf("%s leads out of the download", src)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a file", src)
	}
	return os.Chmod(resolved, info.Mode().Perm()|0o555)
}
