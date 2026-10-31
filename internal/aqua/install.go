package aqua

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"log/slog"
	"maps"
	"net/http"
	neturl "net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/aquaproj/aqua/v2/pkg/checksum"
)

// What bounds an install. maxDownloadBytes bounds a package's download: the
// largest that the registry's tools ship is a few hundred MiB.
// maxChecksumFileBytes bounds a checksum file, which lists a release's
// assets. maxReleaseBytes bounds the GitHub API's description of a release.
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
	// ErrDownload is a download that failed to fetch, or, with ErrChecksum,
	// did not match its checksum. That is what another way to fetch it can
	// get right.
	ErrDownload = errors.New("the download failed")
	// ErrChecksum is a download that does not match its checksum.
	ErrChecksum = errors.New("the download does not match its checksum")
)

// Installer installs resolved packages.
type Installer struct {
	// Client fetches what the checks of a download use. That is the GitHub
	// REST API's account of a release and its attestations at APIURL, with
	// Token as its credential when set. It is also a checksum file and a
	// signature.
	Client *http.Client
	APIURL string
	Token  string
	// Downloads fetches the download itself. nil takes Client.
	Downloads *http.Client
	// Logger, when set, logs the signature checks that ran and the signatures
	// passed over.
	Logger *slog.Logger
}

func (in *Installer) logger() *slog.Logger {
	if in.Logger == nil {
		return discard
	}
	return in.Logger
}

// Sum is a checksum: its algorithm and value, in hex.
type Sum struct {
	Algorithm, Hex string
}

// Install downloads p. Before it unpacks anything of p, it makes sure that
// the download matches sum, its Checksum, and the signatures that the
// registry declares of it. It unpacks p into dir, which must not exist. The
// download and the unpacking are kept beside dir until the package is whole,
// so dir is the package or nothing. It returns where the package's commands
// are, by name.
func (in *Installer) Install(ctx context.Context, p *Package, sum Sum, dir string) (map[string]string, error) {
	download, err := os.CreateTemp(filepath.Dir(dir), ".download-*")
	if err != nil {
		return nil, fmt.Errorf("aqua: %s: %w", p.Name, err)
	}
	defer func() { _ = os.Remove(download.Name()) }()
	digest, err := in.download(ctx, p.URL, sum, download)
	if cerr := download.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, fmt.Errorf("aqua: %s: %w: %w", p.Name, ErrDownload, err)
	}
	f := subject{name: p.Asset, sha256: digest, open: func() (io.ReadCloser, error) { return os.Open(download.Name()) }}
	if err := in.verify(ctx, p, p.assetSignatures(), f); err != nil {
		return nil, fmt.Errorf("aqua: %s: %w", p.Name, err)
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

// Checksum is what p's download must match: the digest that GitHub keeps of a
// release's asset, else the one in the checksum file that the registry names.
// Looking the release up also makes sure that the version is one of its tags,
// which the registry cannot tell: it resolves any version. A release that
// GitHub says it does not have, or one without the asset, fails though a
// checksum file is named. One that keeps no digest, or that the Installer
// failed to ask GitHub about, leaves the file to stand in. A checksum file
// that the registry declares a signature of is read in place of the digest,
// as aqua reads it. Checksum makes sure that its signature is valid.
func (in *Installer) Checksum(ctx context.Context, p *Package) (Sum, error) {
	var released Sum
	var releaseErr error
	if p.Type == TypeGitHubRelease {
		sum, err := in.releaseDigest(ctx, p)
		if _, missing := errors.AsType[unreleased](err); missing || err != nil && p.ChecksumURL == "" {
			return Sum{}, fmt.Errorf("aqua: %s: %w", p.Name, err)
		}
		released, releaseErr = sum, err
	}
	if s := p.checksumSignatures(); p.ChecksumURL != "" && s.declared() {
		return in.checksumFile(ctx, p, s, nil)
	}
	switch {
	case released.Hex != "":
		return released, nil
	case p.ChecksumURL == "":
		return Sum{}, fmt.Errorf("aqua: %s: %w", p.Name, ErrNoChecksum)
	}
	return in.checksumFile(ctx, p, signatures{}, releaseErr)
}

// checksumFile is the checksum of p's download that its checksum file holds,
// once s, the signatures declared of the file, pass. releaseErr is why the
// release gave none, joined to the file's own failure.
func (in *Installer) checksumFile(ctx context.Context, p *Package, s signatures, releaseErr error) (Sum, error) {
	content, err := get(ctx, in.Client, p.ChecksumURL, nil, maxChecksumFileBytes)
	if err != nil {
		return Sum{}, fmt.Errorf("aqua: %s: checksum file: %w", p.Name, errors.Join(err, releaseErr))
	}
	digest := sha256.Sum256(content)
	f := subject{name: path.Base(p.ChecksumURL), sha256: digest[:], open: func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(content)), nil
	}}
	if err := in.verify(ctx, p, s, f); err != nil {
		return Sum{}, fmt.Errorf("aqua: %s: %w", p.Name, err)
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

// request GETs url with client and header. It fails on any status but 200.
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

// download writes url's body to w, at most maxDownloadBytes of it. It makes
// sure that the body matches sum, and returns its SHA-256, which a signature
// is of.
func (in *Installer) download(ctx context.Context, url string, sum Sum, w io.Writer) ([]byte, error) {
	h, err := hasher(sum.Algorithm)
	if err != nil {
		return nil, err
	}
	client := in.Downloads
	if client == nil {
		client = in.Client
	}
	resp, err := request(ctx, client, url, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	digest := sha256.New()
	n, err := io.Copy(io.MultiWriter(w, h, digest), io.LimitReader(resp.Body, maxDownloadBytes+1))
	switch {
	case err != nil:
		return nil, fmt.Errorf("%s: %w", url, err)
	case n > maxDownloadBytes:
		return nil, fmt.Errorf("%s: over %d bytes", url, maxDownloadBytes)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != sum.Hex {
		return nil, fmt.Errorf("%s: %w: %s %s, want %s", url, ErrChecksum, sum.Algorithm, got, sum.Hex)
	}
	return digest.Sum(nil), nil
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
// executable. The command is a regular file there, or a link that resolves to
// one within root. The link resolves the same way once root moves, since it
// leads nowhere out of root.
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
