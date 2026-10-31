package aqua

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/aquaproj/aqua/v2/pkg/config/registry"
	"github.com/aquaproj/aqua/v2/pkg/template"
	"github.com/klauspost/compress/snappy"
	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/fulcio/certificate"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/verify"
)

// trustedRootJSON is Sigstore's public-good trusted root, which
// scripts/trustedroot fetches from Sigstore's TUF repository: what a
// Sigstore bundle is verified against, offline.
//
//go:embed trusted_root.json
var trustedRootJSON []byte

var sigstoreVerifier = sync.OnceValues(func() (*verify.Verifier, error) {
	tr, err := root.NewTrustedRootFromJSON(trustedRootJSON)
	if err != nil {
		return nil, err
	}
	return verify.NewVerifier(tr, verify.WithSignedCertificateTimestamps(1), verify.WithTransparencyLog(1), verify.WithObserverTimestamps(1))
})

// maxSignatureBytes bounds what a signature is read from: a bundle, a
// minisign signature, or the GitHub API's attestations of a download.
const maxSignatureBytes = 16 << 20

// ErrSignature is a download, or its checksum file, with a signature the
// registry declares that does not verify.
var ErrSignature = errors.New("the signature does not verify")

const (
	// githubIssuer is the OIDC issuer of GitHub Actions' workflow
	// identities.
	githubIssuer = "https://token.actions.githubusercontent.com"
	// slsaBuilder is what the identity of a slsa-github-generator builder
	// at a release's tag matches: the builders slsa-verifier trusts without
	// a builder ID, which aqua never passes it. A delegator is not one: the
	// provenance it signs names a builder of its caller's choosing.
	slsaBuilder = `^https://github\.com/slsa-framework/slsa-github-generator/\.github/workflows/` +
		`(generator_generic_slsa3|builder_go_slsa3|builder_container-based_slsa3)\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$`
	// attestationProvenance is the predicate type of a GitHub artifact
	// attestation unless the registry names another, as gh has it.
	attestationProvenance = "https://slsa.dev/provenance/v1"
	// bundleMediaType starts the media type of every Sigstore bundle.
	bundleMediaType = "application/vnd.dev.sigstore.bundle"
)

// slsaProvenance are the predicate types of the SLSA provenance
// slsa-verifier reads.
var slsaProvenance = []string{"https://slsa.dev/provenance/v0.2", attestationProvenance}

// signatures are the signatures the registry declares of one file: the
// download, or its checksum file.
type signatures struct {
	cosign       *registry.Cosign
	minisign     *registry.Minisign
	attestations *registry.GitHubArtifactAttestations
	slsa         *registry.SLSAProvenance
}

func (s signatures) declared() bool {
	return s.cosign.GetEnabled() || s.minisign.GetEnabled() || s.attestations.GetEnabled() || s.slsa.GetEnabled()
}

// subject is a file a signature is of: its name, its SHA-256, and how to
// read it.
type subject struct {
	name   string
	sha256 []byte
	open   func() (io.ReadCloser, error)
}

// verify makes sure that each signature that s declares of f is valid. It
// fails on the first that does not verify. A signature of a kind that the
// runner has no check for is passed over. That is a detached signature and
// certificate, a key-signed one, or provenance outside a Sigstore bundle. The
// log says so, and the checksum alone then stands for the file.
func (in *Installer) verify(ctx context.Context, p *Package, s signatures, f subject) error {
	for _, c := range []struct {
		kind    string
		enabled bool
		check   func() (skipped string, err error)
	}{
		{"cosign", s.cosign.GetEnabled(), func() (string, error) { return in.cosign(ctx, p, s.cosign, f) }},
		{"minisign", s.minisign.GetEnabled(), func() (string, error) { return in.minisign(ctx, p, s.minisign, f) }},
		{"GitHub artifact attestation", s.attestations.GetEnabled(), func() (string, error) {
			return in.attestations(ctx, p, s.attestations, f)
		}},
		{"SLSA provenance", s.slsa.GetEnabled(), func() (string, error) { return in.slsa(ctx, p, s.slsa, f) }},
	} {
		if !c.enabled {
			continue
		}
		skipped, err := c.check()
		switch {
		case err != nil:
			return fmt.Errorf("%s of %s: %w", c.kind, f.name, err)
		case skipped != "":
			in.logger().Warn("signature not checked", "package", p.Name, "version", p.Version, "file", f.name, "signature", c.kind,
				"reason", skipped)
		default:
			in.logger().Debug("signature checked", "package", p.Name, "version", p.Version, "file", f.name, "signature", c.kind)
		}
	}
	return nil
}

// cosign makes sure that f is valid against the Sigstore bundle that c names,
// held to the identity that its options name. A key-signed file, or a
// signature and certificate apart from a bundle, is passed over. An option
// that is none of those, or of the identity, fails it. Such an option can
// narrow the identity in a way that the runner does not.
func (in *Installer) cosign(ctx context.Context, p *Package, c *registry.Cosign, f subject) (string, error) {
	switch {
	case c.Key != nil:
		return "it is signed with a key", nil
	case c.Bundle == nil:
		return "its signature and certificate are apart, with no bundle", nil
	}
	opts, err := c.RenderOpts(p.tmpl.rt, p.artifact())
	if err != nil {
		return "", err
	}
	identity, skipped, err := cosignIdentity(opts)
	if err != nil || skipped != "" {
		return skipped, err
	}
	b, ok, err := in.bundle(ctx, p, c.Bundle)
	switch {
	case err != nil:
		return "", err
	case !ok:
		return "", fmt.Errorf("%w: its bundle is not a Sigstore bundle", ErrSignature)
	}
	return "", verifyBundle(b, f.sha256, identity, nil)
}

// cosignIdentity is the certificate identity that cosign's options, as
// `cosign verify-blob` takes them, hold a signature to. skipped says why they
// do not name a keyless signature in a bundle.
func cosignIdentity(opts []string) (_ verify.CertificateIdentity, skipped string, _ error) {
	var san, sanRegexp, issuer, issuerRegexp string
	var ext certificate.Extensions
	var ignoreTlog bool
	for i := 0; i < len(opts); i++ {
		flag, value, hasValue := strings.Cut(opts[i], "=")
		if flag == "--insecure-ignore-tlog" {
			ignoreTlog = true
			if hasValue {
				var err error
				if ignoreTlog, err = strconv.ParseBool(value); err != nil {
					return verify.CertificateIdentity{}, "", fmt.Errorf("cosign option %s: %w", opts[i], err)
				}
			}
			continue
		}
		if !hasValue && i+1 < len(opts) {
			i++
			value = opts[i]
		}
		switch flag {
		case "--certificate-identity":
			san = value
		case "--certificate-identity-regexp":
			sanRegexp = value
		case "--certificate-oidc-issuer":
			issuer = value
		case "--certificate-oidc-issuer-regexp":
			issuerRegexp = value
		case "--certificate-github-workflow-repository":
			ext.GithubWorkflowRepository = value
		case "--certificate-github-workflow-ref":
			ext.GithubWorkflowRef = value
		case "--certificate-github-workflow-name":
			ext.GithubWorkflowName = value
		case "--certificate-github-workflow-trigger":
			ext.GithubWorkflowTrigger = value
		case "--certificate-github-workflow-sha":
			ext.GithubWorkflowSHA = value
		case "--signature", "--certificate":
			skipped = "its signature and certificate are apart, with no bundle"
		case "--key":
			skipped = "it is signed with a key"
		default:
			return verify.CertificateIdentity{}, "", fmt.Errorf("cosign option %s is not one the runner checks", flag)
		}
	}
	switch {
	case skipped != "":
		return verify.CertificateIdentity{}, skipped, nil
	case ignoreTlog:
		return verify.CertificateIdentity{}, "its signature is not in a transparency log", nil
	case san == "" && sanRegexp == "":
		return verify.CertificateIdentity{}, "", errors.New("cosign's options name no certificate identity")
	case issuer == "" && issuerRegexp == "":
		return verify.CertificateIdentity{}, "", errors.New("cosign's options name no certificate issuer")
	}
	identity, err := newIdentity(san, sanRegexp, issuer, issuerRegexp, ext)
	return identity, "", err
}

func newIdentity(san, sanRegexp, issuer, issuerRegexp string, ext certificate.Extensions) (verify.CertificateIdentity, error) {
	sanMatcher, err := verify.NewSANMatcher(san, sanRegexp)
	if err != nil {
		return verify.CertificateIdentity{}, err
	}
	issuerMatcher, err := verify.NewIssuerMatcher(issuer, issuerRegexp)
	if err != nil {
		return verify.CertificateIdentity{}, err
	}
	return verify.NewCertificateIdentity(sanMatcher, issuerMatcher, ext)
}

// bundle fetches the file d names, and whether it is a Sigstore bundle.
func (in *Installer) bundle(ctx context.Context, p *Package, d *registry.DownloadedFile) (*bundle.Bundle, bool, error) {
	url, err := p.fileURL(d)
	if err != nil {
		return nil, false, err
	}
	data, err := get(ctx, in.Client, url, nil, maxSignatureBytes)
	if err != nil {
		return nil, false, err
	}
	return parseBundle(bytes.TrimSpace(data))
}

// parseBundle reads data as a Sigstore bundle, and whether it is one, by the
// media type of the JSON value it starts with. So provenance of several JSON
// lines reads as a form that the runner passes over. Data that is no JSON at
// all, or a bundle that does not parse, fails. A corrupted bundle is never
// taken for a form that the runner passes over.
func parseBundle(data []byte) (*bundle.Bundle, bool, error) {
	var probe struct {
		MediaType string `json:"mediaType"`
	}
	if err := json.NewDecoder(bytes.NewReader(data)).Decode(&probe); err != nil {
		return nil, false, fmt.Errorf("%w: not JSON: %v", ErrSignature, err)
	}
	if !strings.HasPrefix(probe.MediaType, bundleMediaType) {
		return nil, false, nil
	}
	b := &bundle.Bundle{}
	if err := b.UnmarshalJSON(data); err != nil {
		return nil, true, fmt.Errorf("%w: %v", ErrSignature, err)
	}
	return b, true, nil
}

// verifyBundle makes sure that b is a signature of the file whose SHA-256 is
// digest, by identity. When predicateTypes are given, it also makes sure that
// b attests a statement of one of them.
func verifyBundle(b *bundle.Bundle, digest []byte, identity verify.CertificateIdentity, predicateTypes []string) error {
	v, err := sigstoreVerifier()
	if err != nil {
		return err
	}
	result, err := v.Verify(b, verify.NewPolicy(verify.WithArtifactDigest("sha256", digest), verify.WithCertificateIdentity(identity)))
	if err != nil {
		return fmt.Errorf("%w: %v", ErrSignature, err)
	}
	if predicateTypes != nil && (result.Statement == nil || !slices.Contains(predicateTypes, result.Statement.GetPredicateType())) {
		return fmt.Errorf("%w: it attests no %s statement", ErrSignature, strings.Join(predicateTypes, " or "))
	}
	return nil
}

// minisign makes sure that f is valid against the minisign signature that m
// names, with the public key that the registry pins.
func (in *Installer) minisign(ctx context.Context, p *Package, m *registry.Minisign, f subject) (string, error) {
	if m.PublicKey == "" {
		return "", errors.New("the registry names no public key")
	}
	url, err := p.fileURL(m.ToDownloadedFile())
	if err != nil {
		return "", err
	}
	sig, err := get(ctx, in.Client, url, nil, maxSignatureBytes)
	if err != nil {
		return "", err
	}
	r, err := f.open()
	if err != nil {
		return "", err
	}
	defer func() { _ = r.Close() }()
	if err := verifyMinisign(m.PublicKey, sig, r); err != nil {
		return "", fmt.Errorf("%w: %v", ErrSignature, err)
	}
	return "", nil
}

// signerWorkflowRef is what follows a workflow's path in the identity of
// a signer it ran as, as gh matches it.
const signerWorkflowRef = `@(refs/.*|[0-9a-fA-F]+)`

// workflowHost tells a signer workflow that names its host, as gh does.
var workflowHost = regexp.MustCompile(`^[a-zA-Z0-9-]+\.[a-zA-Z0-9-]+.*$`)

// attestations makes sure that f is valid against GitHub's artifact
// attestations of it, held to the identity that gh holds them to. That
// identity is the signer workflow or repository that the registry names, else
// any workflow of the attestations' repository. It also has GitHub Actions'
// issuer and the source that the registry names. One that verifies is enough.
// They are passed over only when every one is by GitHub's own Sigstore
// instance.
func (in *Installer) attestations(ctx context.Context, p *Package, a *registry.GitHubArtifactAttestations, f subject) (string, error) {
	if a.SignerRepo != "" && a.SignerWorkflow() != "" {
		return "", errors.New("the registry names both a signer repository and a signer workflow, which gh refuses")
	}
	repo := a.Repository
	if repo == "" {
		repo = p.Owner + "/" + p.Repo
	}
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" {
		return "", fmt.Errorf("%q is not a repository", repo)
	}
	url := fmt.Sprintf("%s/repos/%s/%s/attestations/sha256:%x", strings.TrimSuffix(in.APIURL, "/"),
		neturl.PathEscape(owner), neturl.PathEscape(name), f.sha256)
	header := http.Header{"Accept": {"application/vnd.github+json"}, "X-Github-Api-Version": {"2022-11-28"}}
	if in.Token != "" {
		header.Set("Authorization", "Bearer "+in.Token)
	}
	body, err := get(ctx, in.Client, url, header, maxSignatureBytes)
	if errors.Is(err, errNotFound) {
		return "", fmt.Errorf("%w: %s has no attestation of it", ErrSignature, repo)
	}
	if err != nil {
		return "", err
	}
	var list struct {
		Attestations []struct {
			Bundle    json.RawMessage `json:"bundle"`
			BundleURL string          `json:"bundle_url"`
		} `json:"attestations"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return "", fmt.Errorf("attestations: %w", err)
	}
	if len(list.Attestations) == 0 {
		return "", fmt.Errorf("%w: %s has no attestation of it", ErrSignature, repo)
	}
	sanRegexp := "(?i)^" + regexp.QuoteMeta("https://github.com/"+repo+"/")
	switch workflow := a.SignerWorkflow(); {
	case a.SignerRepo != "":
		sanRegexp = "(?i)^" + regexp.QuoteMeta("https://github.com/"+a.SignerRepo+"/")
	case workflow != "":
		if !workflowHost.MatchString(workflow) {
			workflow = "github.com/" + workflow
		}
		sanRegexp = "^" + regexp.QuoteMeta("https://"+workflow)
		if regexp.MustCompile(signerWorkflowRef + `$`).MatchString(workflow) {
			sanRegexp += "$"
		} else {
			sanRegexp += "(" + signerWorkflowRef + ")?$"
		}
	}
	issuer := a.CertOIDCIssuer
	if issuer == "" {
		issuer = githubIssuer
	}
	ext := certificate.Extensions{
		SourceRepositoryURI: "https://github.com/" + repo, SourceRepositoryOwnerURI: "https://github.com/" + owner,
		SourceRepositoryRef: a.SourceRef, SourceRepositoryDigest: a.SourceDigest,
	}
	if a.DenySelfHostedRunners {
		ext.RunnerEnvironment = "github-hosted"
	}
	identity, err := newIdentity("", sanRegexp, issuer, "", ext)
	if err != nil {
		return "", err
	}
	predicateType := a.PredicateType
	if predicateType == "" {
		predicateType = attestationProvenance
	}
	var errs []error
	for _, att := range list.Attestations {
		b, ok, err := in.attestationBundle(ctx, att.Bundle, att.BundleURL)
		switch {
		case err != nil:
			errs = append(errs, err)
			continue
		case !ok:
			errs = append(errs, fmt.Errorf("%w: an attestation has no bundle", ErrSignature))
			continue
		case githubInstance(b):
			continue
		}
		err = verifyBundle(b, f.sha256, identity, []string{predicateType})
		if err == nil {
			return "", nil
		}
		errs = append(errs, err)
	}
	if len(errs) == 0 {
		return "its attestations are all by GitHub's own Sigstore instance", nil
	}
	return "", errors.Join(errs...)
}

// attestationBundle is an attestation's bundle, and whether it has one:
// the one the GitHub API returns inline, else the one at its bundle URL,
// which GitHub keeps snappy-compressed. The URL carries its own grant, so
// the request carries no token.
func (in *Installer) attestationBundle(ctx context.Context, inline json.RawMessage, url string) (*bundle.Bundle, bool, error) {
	if inline := bytes.TrimSpace(inline); len(inline) > 0 && !bytes.Equal(inline, []byte("null")) {
		return parseBundle(inline)
	}
	if url == "" {
		return nil, false, nil
	}
	if _, err := httpsURL(url); err != nil {
		return nil, false, err
	}
	compressed, err := get(ctx, in.Client, url, nil, maxSignatureBytes)
	if err != nil {
		return nil, false, fmt.Errorf("attestation bundle: %w", err)
	}
	if n, err := snappy.DecodedLen(compressed); err != nil || n > maxSignatureBytes {
		return nil, true, fmt.Errorf("%w: an attestation's bundle is not snappy-compressed, or too large", ErrSignature)
	}
	data, err := snappy.Decode(nil, compressed)
	if err != nil {
		return nil, true, fmt.Errorf("%w: %v", ErrSignature, err)
	}
	return parseBundle(data)
}

// githubInstance reports whether b was signed by GitHub's own Sigstore
// instance, as a private repository's attestations are, whose trusted root
// the runner does not carry: its certificate's issuer is GitHub's.
func githubInstance(b *bundle.Bundle) bool {
	content, err := b.VerificationContent()
	if err != nil {
		return false
	}
	cert := content.Certificate()
	return cert != nil && slices.Contains(cert.Issuer.Organization, "GitHub, Inc.")
}

// slsa makes sure that f is valid against the SLSA provenance that s names, a
// Sigstore bundle that slsa-github-generator signed. The provenance is held
// to a builder at a release's tag, GitHub Actions' issuer, and the source
// repository and tag that the registry names. Those are the package's own and
// its version, unless the registry names others. The check skips a tag of
// "-". Provenance outside a Sigstore bundle is passed over.
func (in *Installer) slsa(ctx context.Context, p *Package, s *registry.SLSAProvenance, f subject) (string, error) {
	file := s.ToDownloadedFile()
	if file.RepoOwner == "" {
		file.RepoOwner = p.Owner
	}
	if file.RepoName == "" {
		file.RepoName = p.Repo
	}
	b, ok, err := in.bundle(ctx, p, file)
	switch {
	case err != nil:
		return "", err
	case !ok:
		return "it is not in a Sigstore bundle", nil
	}
	source := "github.com/" + file.RepoOwner + "/" + file.RepoName
	if s.SourceURI != nil {
		source = *s.SourceURI
	}
	ext := certificate.Extensions{SourceRepositoryURI: "https://" + strings.TrimPrefix(source, "https://")}
	switch tag := s.SourceTag; tag {
	case "-":
	case "":
		ext.SourceRepositoryRef = "refs/tags/" + p.Version
	default:
		ext.SourceRepositoryRef = "refs/tags/" + tag
	}
	identity, err := newIdentity("", slsaBuilder, githubIssuer, "", ext)
	if err != nil {
		return "", err
	}
	return "", verifyBundle(b, f.sha256, identity, slsaProvenance)
}

// artifact is what a template of p's renders: the version, platform and
// download.
func (p *Package) artifact() *template.Artifact {
	t := p.tmpl
	return &template.Artifact{
		Version:         t.version,
		SemVer:          strings.TrimPrefix(t.version, t.info.VersionPrefix),
		OS:              replace(t.rt.GOOS, t.info.Replacements),
		Arch:            t.arch(t.info.Replacements),
		Format:          t.info.GetFormat(),
		Asset:           p.Asset,
		AssetWithoutExt: strings.TrimSuffix(p.Asset, "."+extFormat(p.Asset)),
	}
}

// fileURL is where the file d names is: a release's asset, by default
// p's release's, or a URL.
func (p *Package) fileURL(d *registry.DownloadedFile) (string, error) {
	switch d.Type {
	case TypeGitHubRelease:
		owner, repo := d.RepoOwner, d.RepoName
		if owner == "" {
			owner = p.Owner
		}
		if repo == "" {
			repo = p.Repo
		}
		if d.Asset == nil {
			return "", errors.New("a release's file names no asset")
		}
		name, err := template.Render(*d.Asset, p.artifact(), p.tmpl.rt)
		if err != nil {
			return "", err
		}
		if name == "" || strings.Contains(name, "/") {
			return "", fmt.Errorf("asset %q is not a file name", name)
		}
		return releaseURL(owner, repo, p.Version, name), nil
	case TypeHTTP:
		if d.URL == nil {
			return "", errors.New("a file at a URL names no URL")
		}
		url, err := template.Render(*d.URL, p.artifact(), p.tmpl.rt)
		if err != nil {
			return "", err
		}
		if _, err := httpsURL(url); err != nil {
			return "", err
		}
		return url, nil
	}
	return "", fmt.Errorf("a file of type %s is not one the runner fetches", d.Type)
}

// assetSignatures are the signatures the registry declares of p's download.
func (p *Package) assetSignatures() signatures {
	info := p.tmpl.info
	if info == nil {
		return signatures{}
	}
	return signatures{cosign: info.Cosign, minisign: info.Minisign, attestations: info.GitHubArtifactAttestations, slsa: info.SLSAProvenance}
}

// checksumSignatures are the signatures the registry declares of p's
// checksum file.
func (p *Package) checksumSignatures() signatures {
	c := p.checksum
	if c == nil {
		return signatures{}
	}
	return signatures{cosign: c.Cosign, minisign: c.Minisign, attestations: c.GitHubArtifactAttestations}
}
