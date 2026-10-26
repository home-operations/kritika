package aqua

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/klauspost/compress/snappy"
)

// signedFixture is a registry with a package for each signature the
// runner checks, as aqua-registry declares them, whose files testdata
// holds as their releases published them.
const signedFixture = `packages:
  - type: github_release
    repo_owner: home-operations
    repo_name: flate
    asset: flate_{{trimV .Version}}_{{.OS}}_{{.Arch}}.tar.gz
    cosign:
      bundle:
        type: github_release
        asset: "{{.Asset}}.sigstore.json"
      opts:
        - --certificate-identity
        - https://github.com/home-operations/flate/.github/workflows/goreleaser.yaml@refs/tags/{{.Version}}
        - --certificate-oidc-issuer
        - https://token.actions.githubusercontent.com
        - --certificate-github-workflow-repository
        - home-operations/flate
        - --certificate-github-workflow-ref
        - refs/tags/{{.Version}}
  - type: github_release
    repo_owner: home-operations
    repo_name: flate-other-workflow
    asset: flate_{{trimV .Version}}_{{.OS}}_{{.Arch}}.tar.gz
    cosign:
      bundle:
        type: github_release
        repo_owner: home-operations
        repo_name: flate
        asset: "{{.Asset}}.sigstore.json"
      opts:
        - --certificate-identity
        - https://github.com/home-operations/flate/.github/workflows/release.yaml@refs/tags/{{.Version}}
        - --certificate-oidc-issuer
        - https://token.actions.githubusercontent.com
  - type: github_release
    repo_owner: acme
    repo_name: detached
    asset: detached.tar.gz
    cosign:
      opts:
        - --signature
        - https://github.com/acme/detached/releases/download/{{.Version}}/detached.tar.gz.sig
        - --certificate
        - https://github.com/acme/detached/releases/download/{{.Version}}/detached.tar.gz.pem
        - --certificate-identity
        - https://github.com/acme/detached/.github/workflows/release.yaml@refs/tags/{{.Version}}
        - --certificate-oidc-issuer
        - https://token.actions.githubusercontent.com
  - type: github_release
    repo_owner: acme
    repo_name: keyed
    asset: keyed.tar.gz
    cosign:
      key:
        type: github_release
        asset: cosign.pub
      signature:
        type: github_release
        asset: "{{.Asset}}.sig"
  - type: github_release
    repo_owner: FiloSottile
    repo_name: age
    asset: age-{{.Version}}-{{.OS}}-{{.Arch}}.tar.gz
    files:
      - name: age
        src: age/age
    github_artifact_attestations:
      signer_workflow: FiloSottile/age/.github/workflows/build.yml
  - type: github_release
    repo_owner: FiloSottile
    repo_name: age-other-workflow
    asset: age-{{.Version}}-{{.OS}}-{{.Arch}}.tar.gz
    github_artifact_attestations:
      repository: FiloSottile/age
      signer_workflow: FiloSottile/age/.github/workflows/release.yml
  - type: github_release
    repo_owner: FiloSottile
    repo_name: age-two-signers
    asset: age-{{.Version}}-{{.OS}}-{{.Arch}}.tar.gz
    github_artifact_attestations:
      repository: FiloSottile/age
      signer_repo: FiloSottile/age
      signer_workflow: FiloSottile/age/.github/workflows/build.yml
  - type: github_release
    repo_owner: FiloSottile
    repo_name: age-predicate-prefix
    asset: age-{{.Version}}-{{.OS}}-{{.Arch}}.tar.gz
    github_artifact_attestations:
      repository: FiloSottile/age
      predicate_type: https://slsa.dev/provenance/v
  - type: github_release
    repo_owner: fluxcd
    repo_name: flux2
    asset: flux_{{trimV .Version}}_{{.OS}}_{{.Arch}}.tar.gz
    files:
      - name: flux
    slsa_provenance:
      type: github_release
      asset: provenance.intoto.jsonl
  - type: github_release
    repo_owner: acme
    repo_name: flux2
    asset: flux_{{trimV .Version}}_{{.OS}}_{{.Arch}}.tar.gz
    slsa_provenance:
      type: github_release
      repo_owner: fluxcd
      asset: provenance.intoto.jsonl
  - type: github_release
    repo_owner: fluxcd
    repo_name: flux-cli
    asset: flux_{{trimV .Version}}_{{.OS}}_{{.Arch}}.tar.gz
    slsa_provenance:
      type: github_release
      repo_name: flux2
      asset: provenance.intoto.jsonl
  - type: github_release
    repo_owner: bufbuild
    repo_name: buf
    asset: buf-{{.OS}}-{{.Arch}}.tar.gz
    replacements:
      linux: Linux
      amd64: x86_64
    checksum:
      type: github_release
      asset: sha256.txt
      algorithm: sha256
      minisign:
        type: github_release
        asset: sha256.txt.minisig
        public_key: RWQ/i9xseZwBVE7pEniCNjlNOeeyp4BQgdZDLQcAohxEAH5Uj5DEKjv6
`

// The SHA-256 of downloads the fixtures sign.
const (
	flateDigest = "37eb0cd91a6752340754375743aa616c263cd8367ce594abd42ee955e1ec0099" // flate_0.6.5_linux_amd64.tar.gz
	ageDigest   = "cbe24006683f8eb669266162894b9a522a1af52f2665fbc63a4bb032ed26ac10" // age-v1.3.2-linux-amd64.tar.gz
	fluxDigest  = "30f6fd6a243fadc29624aa122a450ade567eb32db8bf3bafcb586c894b4ef1eb" // flux_2.9.6_linux_arm.tar.gz
)

func fixtureFile(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// signedForge serves the fixtures where their releases and the GitHub API
// have them, and files, by path, beside them.
func signedForge(t *testing.T, files map[string][]byte) (*Installer, *Registry) {
	t.Helper()
	served := map[string][]byte{
		"/home-operations/flate/releases/download/v0.6.5/flate_0.6.5_linux_amd64.tar.gz.sigstore.json": fixtureFile(t, "flate_0.6.5_linux_amd64.tar.gz.sigstore.json"),
		"/repos/FiloSottile/age/attestations/sha256:" + ageDigest:                                      fixtureFile(t, "age-v1.3.2-linux-amd64.attestations.json"),
		"/fluxcd/flux2/releases/download/v2.9.6/provenance.intoto.jsonl":                               fixtureFile(t, "flux2-v2.9.6-provenance.intoto.jsonl"),
		"/fluxcd/flux2/releases/download/v2.9.5/provenance.intoto.jsonl":                               fixtureFile(t, "flux2-v2.9.6-provenance.intoto.jsonl"),
		"/repos/bufbuild/buf/releases/tags/v1.75.0":                                                    []byte(`{"assets":[{"name":"buf-Linux-x86_64.tar.gz","digest":""}]}`),
		"/bufbuild/buf/releases/download/v1.75.0/sha256.txt":                                           fixtureFile(t, "buf-sha256.txt"),
		"/bufbuild/buf/releases/download/v1.75.0/sha256.txt.minisig":                                   fixtureFile(t, "buf-sha256.txt.minisig"),
	}
	maps.Copy(served, files)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, ok := served[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(b)
	}))
	t.Cleanup(srv.Close)
	registry, err := Parse([]byte(signedFixture))
	if err != nil {
		t.Fatal(err)
	}
	return &Installer{Client: &http.Client{Transport: redirectTo(srv.URL)}, APIURL: srv.URL}, registry
}

// corrupt is the bundle b without its verification material.
func corrupt(t *testing.T, b []byte) []byte {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	delete(m, "verificationMaterial")
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func digestBytes(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestVerifySignatures(t *testing.T) {
	const (
		flateBundle = "/home-operations/flate/releases/download/v0.6.5/flate_0.6.5_linux_amd64.tar.gz.sigstore.json"
		ageList     = "/repos/FiloSottile/age/attestations/sha256:" + ageDigest
		fluxBundle  = "/fluxcd/flux2/releases/download/v2.9.6/provenance.intoto.jsonl"
	)
	var ages struct {
		Attestations []struct {
			Bundle json.RawMessage `json:"bundle"`
		} `json:"attestations"`
	}
	if err := json.Unmarshal(fixtureFile(t, "age-v1.3.2-linux-amd64.attestations.json"), &ages); err != nil {
		t.Fatal(err)
	}
	byURL := snappy.Encode(nil, ages.Attestations[0].Bundle)
	for i := range ages.Attestations {
		ages.Attestations[i].Bundle = corrupt(t, ages.Attestations[i].Bundle)
	}
	corruptAges, err := json.Marshal(ages)
	if err != nil {
		t.Fatal(err)
	}
	notABundle := []byte(`{"payloadType":"application/vnd.in-toto+json","payload":"e30=","signatures":[]}`)
	flux := fixtureFile(t, "flux2-v2.9.6-provenance.intoto.jsonl")
	truncated := flux[:bytes.Index(flux, []byte(`"mediaType"`))+80]
	tests := []struct {
		name, pkg, version string
		platform           Platform
		digest             string
		files              map[string][]byte
		want               error
		says               string
	}{
		{name: "a cosign bundle", pkg: "home-operations/flate", version: "v0.6.5", platform: linuxAMD64, digest: flateDigest},
		{
			name: "a cosign bundle of another file", pkg: "home-operations/flate", version: "v0.6.5", platform: linuxAMD64,
			digest: ageDigest, want: ErrSignature,
		},
		{
			name: "a cosign bundle by another workflow", pkg: "home-operations/flate-other-workflow", version: "v0.6.5", platform: linuxAMD64,
			digest: flateDigest, want: ErrSignature,
		},
		{
			name: "a cosign bundle missing", pkg: "home-operations/flate", version: "v0.6.4", platform: linuxAMD64,
			digest: flateDigest, says: "not found",
		},
		{
			name: "a corrupted cosign bundle", pkg: "home-operations/flate", version: "v0.6.5", platform: linuxAMD64,
			digest: flateDigest, files: map[string][]byte{flateBundle: corrupt(t, fixtureFile(t, "flate_0.6.5_linux_amd64.tar.gz.sigstore.json"))},
			want: ErrSignature,
		},
		{
			name: "a cosign bundle that is not one", pkg: "home-operations/flate", version: "v0.6.5", platform: linuxAMD64,
			digest: flateDigest, files: map[string][]byte{flateBundle: notABundle}, want: ErrSignature,
		},
		{name: "a detached signature, passed over", pkg: "acme/detached", version: "v1.0.0", platform: linuxAMD64, digest: flateDigest},
		{name: "a key, passed over", pkg: "acme/keyed", version: "v1.0.0", platform: linuxAMD64, digest: flateDigest},
		{name: "a GitHub artifact attestation", pkg: "FiloSottile/age", version: "v1.3.2", platform: linuxAMD64, digest: ageDigest},
		{
			name: "an attestation by another workflow", pkg: "FiloSottile/age-other-workflow", version: "v1.3.2", platform: linuxAMD64,
			digest: ageDigest, want: ErrSignature,
		},
		{
			name: "no attestation", pkg: "FiloSottile/age", version: "v1.3.2", platform: linuxAMD64,
			digest: flateDigest, want: ErrSignature, says: "has no attestation",
		},
		{
			name: "no attestation listed", pkg: "FiloSottile/age", version: "v1.3.2", platform: linuxAMD64,
			digest: ageDigest, files: map[string][]byte{ageList: []byte(`{"attestations":[]}`)}, want: ErrSignature, says: "has no attestation",
		},
		{
			name: "an attestation by URL alone", pkg: "FiloSottile/age", version: "v1.3.2", platform: linuxAMD64, digest: ageDigest,
			files: map[string][]byte{ageList: []byte(`{"attestations":[{"bundle":null,"bundle_url":"https://blob.example/a.json.sn"}]}`), "/a.json.sn": byURL},
		},
		{
			name: "an attestation by URL, not snappy-compressed", pkg: "FiloSottile/age", version: "v1.3.2", platform: linuxAMD64, digest: ageDigest,
			files: map[string][]byte{
				ageList: []byte(`{"attestations":[{"bundle":null,"bundle_url":"https://blob.example/a.json.sn"}]}`), "/a.json.sn": []byte("\xff\xff\xff\xff\xff\xff"),
			},
			want: ErrSignature,
		},
		{
			name: "an attestation with no bundle", pkg: "FiloSottile/age", version: "v1.3.2", platform: linuxAMD64,
			digest: ageDigest, files: map[string][]byte{ageList: []byte(`{"attestations":[{"bundle":null}]}`)}, want: ErrSignature, says: "has no bundle",
		},
		{
			name: "corrupted attestations", pkg: "FiloSottile/age", version: "v1.3.2", platform: linuxAMD64,
			digest: ageDigest, files: map[string][]byte{ageList: corruptAges}, want: ErrSignature,
		},
		{
			name: "an attestation of a type the registry names a prefix of", pkg: "FiloSottile/age-predicate-prefix", version: "v1.3.2",
			platform: linuxAMD64, digest: ageDigest, want: ErrSignature, says: "attests no",
		},
		{
			name: "a signer repository and workflow both", pkg: "FiloSottile/age-two-signers", version: "v1.3.2", platform: linuxAMD64,
			digest: ageDigest, says: "both",
		},
		{
			name: "SLSA provenance", pkg: "fluxcd/flux2", version: "v2.9.6", platform: Platform{OS: "linux", Arch: "arm", LibC: "glibc"},
			digest: fluxDigest,
		},
		{
			name: "SLSA provenance of another tag", pkg: "fluxcd/flux2", version: "v2.9.5", platform: Platform{OS: "linux", Arch: "arm", LibC: "glibc"},
			digest: fluxDigest, want: ErrSignature,
		},
		{
			name: "SLSA provenance outside a bundle, passed over", pkg: "fluxcd/flux2", version: "v2.9.4",
			platform: Platform{OS: "linux", Arch: "arm", LibC: "glibc"}, digest: fluxDigest,
			files: map[string][]byte{"/fluxcd/flux2/releases/download/v2.9.4/provenance.intoto.jsonl": notABundle},
		},
		{
			name: "SLSA provenance, its owner named alone", pkg: "acme/flux2", version: "v2.9.6",
			platform: Platform{OS: "linux", Arch: "arm", LibC: "glibc"}, digest: fluxDigest,
		},
		{
			name: "SLSA provenance, its repository named alone", pkg: "fluxcd/flux-cli", version: "v2.9.6",
			platform: Platform{OS: "linux", Arch: "arm", LibC: "glibc"}, digest: fluxDigest,
		},
		{
			name: "SLSA provenance in several lines, passed over", pkg: "fluxcd/flux2", version: "v2.9.4",
			platform: Platform{OS: "linux", Arch: "arm", LibC: "glibc"}, digest: fluxDigest,
			files: map[string][]byte{"/fluxcd/flux2/releases/download/v2.9.4/provenance.intoto.jsonl": slices.Concat(notABundle, []byte("\n"), notABundle)},
		},
		{
			name: "truncated SLSA provenance", pkg: "fluxcd/flux2", version: "v2.9.6", platform: Platform{OS: "linux", Arch: "arm", LibC: "glibc"},
			digest: fluxDigest, files: map[string][]byte{fluxBundle: truncated}, want: ErrSignature,
		},
		{
			name: "corrupted SLSA provenance", pkg: "fluxcd/flux2", version: "v2.9.6", platform: Platform{OS: "linux", Arch: "arm", LibC: "glibc"},
			digest: fluxDigest, files: map[string][]byte{fluxBundle: corrupt(t, fixtureFile(t, "flux2-v2.9.6-provenance.intoto.jsonl"))}, want: ErrSignature,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in, registry := signedForge(t, tt.files)
			p, err := registry.Resolve(tt.pkg, tt.version, tt.platform)
			if err != nil {
				t.Fatal(err)
			}
			err = in.verify(context.Background(), p, p.assetSignatures(), subject{name: p.Asset, sha256: digestBytes(t, tt.digest)})
			switch {
			case tt.want == nil && tt.says == "" && err != nil:
				t.Fatalf("verify: %v", err)
			case tt.want != nil && !errors.Is(err, tt.want):
				t.Fatalf("verify error = %v, want %v", err, tt.want)
			case tt.says != "" && (err == nil || !strings.Contains(err.Error(), tt.says)):
				t.Fatalf("verify error = %v, want it to say %q", err, tt.says)
			}
		})
	}
}

// TestChecksumFileSigned: a checksum file the registry declares a minisign
// signature of is read and checked before its checksum is taken, and one
// that does not match its signature is refused.
func TestChecksumFileSigned(t *testing.T) {
	sum := fixtureFile(t, "buf-sha256.txt")
	for _, tt := range []struct {
		name string
		file []byte
		want error
	}{
		{"as signed", sum, nil},
		{"tampered", append([]byte("0000000000000000000000000000000000000000000000000000000000000000  buf-Linux-x86_64.tar.gz\n"), sum...), ErrSignature},
	} {
		t.Run(tt.name, func(t *testing.T) {
			in, registry := signedForge(t, map[string][]byte{"/bufbuild/buf/releases/download/v1.75.0/sha256.txt": tt.file})
			p, err := registry.Resolve("bufbuild/buf", "v1.75.0", linuxAMD64)
			if err != nil {
				t.Fatal(err)
			}
			got, err := in.Checksum(context.Background(), p)
			if !errors.Is(err, tt.want) {
				t.Fatalf("Checksum error = %v, want %v", err, tt.want)
			}
			if tt.want == nil && got.Hex != "7168a06e29bd5eb8d1530848f242264f7a86b9b8af3b2e811570fbea0c929a30" {
				t.Errorf("Checksum = %+v", got)
			}
		})
	}
}

// TestChecksumFileSignedNeedsTheRelease: a signed checksum file does not
// stand in for a version GitHub has no release of.
func TestChecksumFileSignedNeedsTheRelease(t *testing.T) {
	in, registry := signedForge(t, map[string][]byte{
		"/bufbuild/buf/releases/download/v1.74.0/sha256.txt":         fixtureFile(t, "buf-sha256.txt"),
		"/bufbuild/buf/releases/download/v1.74.0/sha256.txt.minisig": fixtureFile(t, "buf-sha256.txt.minisig"),
	})
	p, err := registry.Resolve("bufbuild/buf", "v1.74.0", linuxAMD64)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := in.Checksum(context.Background(), p); err == nil || !strings.Contains(err.Error(), "has no release tagged v1.74.0") {
		t.Fatalf("Checksum = %v, want the missing release", err)
	}
}

// TestInstallRefusesUnsigned: a download whose signature does not verify
// is not unpacked, though it matches its checksum.
func TestInstallRefusesUnsigned(t *testing.T) {
	body := gzipped(t, tarball(t, file("flate", "flate")))
	in, registry := signedForge(t, map[string][]byte{"/home-operations/flate/releases/download/v0.6.5/flate_0.6.5_linux_amd64.tar.gz": body})
	p, err := registry.Resolve("home-operations/flate", "v0.6.5", linuxAMD64)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "pkg")
	_, err = in.Install(context.Background(), p, Sum{Algorithm: "sha256", Hex: sha256Hex(body)}, dir)
	if !errors.Is(err, ErrSignature) {
		t.Fatalf("Install error = %v, want %v", err, ErrSignature)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a download whose signature failed was unpacked: %v", err)
	}
}

func TestSLSABuilder(t *testing.T) {
	const workflows = "https://github.com/slsa-framework/slsa-github-generator/.github/workflows/"
	builder := regexp.MustCompile(slsaBuilder)
	for identity, want := range map[string]bool{
		workflows + "generator_generic_slsa3.yml@refs/tags/v2.1.0":          true,
		workflows + "builder_go_slsa3.yml@refs/tags/v2.1.0":                 true,
		workflows + "builder_container-based_slsa3.yml@refs/tags/v2.1.0":    true,
		workflows + "delegator_generic_slsa3.yml@refs/tags/v2.1.0":          false,
		workflows + "delegator_lowperms-generic_slsa3.yml@refs/tags/v2.1.0": false,
		workflows + "generator_generic_slsa3.yml@refs/heads/main":           false,
	} {
		if got := builder.MatchString(identity); got != want {
			t.Errorf("%s: trusted = %v, want %v", identity, got, want)
		}
	}
}

func TestCosignIdentity(t *testing.T) {
	identity := []string{"--certificate-identity", "https://github.com/acme/tool/.github/workflows/release.yaml@refs/tags/v1", "--certificate-oidc-issuer",
		"https://token.actions.githubusercontent.com"}
	for _, tt := range []struct {
		name          string
		opts          []string
		skipped, fail string
	}{
		{name: "an identity", opts: identity},
		{name: "a regexp, with =", opts: []string{"--certificate-identity-regexp=^https://github\\.com/acme/", "--certificate-oidc-issuer=https://token.actions.githubusercontent.com"}},
		{name: "a workflow too", opts: append(append([]string{}, identity...), "--certificate-github-workflow-repository", "acme/tool")},
		{name: "a detached signature", opts: append(append([]string{}, identity...), "--signature", "https://x/sig"), skipped: "apart"},
		{name: "a key", opts: []string{"--key", "https://x/cosign.pub"}, skipped: "key"},
		{name: "no transparency log", opts: append(append([]string{}, identity...), "--insecure-ignore-tlog"), skipped: "transparency log"},
		{name: "no transparency log, with =true", opts: append(append([]string{}, identity...), "--insecure-ignore-tlog=true"), skipped: "transparency log"},
		{name: "a transparency log, with =false", opts: append(append([]string{}, identity...), "--insecure-ignore-tlog=false")},
		{name: "a transparency log, false last", opts: append(append([]string{}, identity...), "--insecure-ignore-tlog", "--insecure-ignore-tlog=false")},
		{name: "a transparency log option that is not a bool", opts: append(append([]string{}, identity...), "--insecure-ignore-tlog=maybe"), fail: "invalid syntax"},
		{name: "an option the runner does not check", opts: append(append([]string{}, identity...), "--rekor-url", "https://x"), fail: "not one the runner checks"},
		{name: "no identity", opts: []string{"--certificate-oidc-issuer", "https://token.actions.githubusercontent.com"}, fail: "no certificate identity"},
		{name: "no issuer", opts: identity[:2], fail: "no certificate issuer"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, skipped, err := cosignIdentity(tt.opts)
			switch {
			case tt.fail != "" && (err == nil || !strings.Contains(err.Error(), tt.fail)):
				t.Fatalf("cosignIdentity error = %v, want it to say %q", err, tt.fail)
			case tt.fail == "" && err != nil:
				t.Fatalf("cosignIdentity: %v", err)
			case !strings.Contains(skipped, tt.skipped) || (tt.skipped == "" && skipped != ""):
				t.Fatalf("skipped = %q, want %q", skipped, tt.skipped)
			}
		})
	}
}

func TestVerifyMinisign(t *testing.T) {
	sum, sig := fixtureFile(t, "buf-sha256.txt"), fixtureFile(t, "buf-sha256.txt.minisig")
	const key = "RWQ/i9xseZwBVE7pEniCNjlNOeeyp4BQgdZDLQcAohxEAH5Uj5DEKjv6"
	for _, tt := range []struct {
		name, key string
		sig       []byte
		message   []byte
		fails     bool
	}{
		{"as signed", key, sig, sum, false},
		{"another message", key, sig, append([]byte("x"), sum...), true},
		{"another key", "RWQf6LRCGA9i53mlYecO4IzT51TGPpvWucNSCh1CBM0QTaLn73Y7GFO3", sig, sum, true},
		{"a forged trusted comment", key, []byte(strings.Replace(string(sig), "filename:sha256.txt", "filename:other.txt", 1)), sum, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := verifyMinisign(tt.key, tt.sig, io.NopCloser(strings.NewReader(string(tt.message))))
			if (err != nil) != tt.fails {
				t.Fatalf("verifyMinisign error = %v, want a failure: %v", err, tt.fails)
			}
		})
	}
}
