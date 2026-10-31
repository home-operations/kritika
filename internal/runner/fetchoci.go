package runner

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/registry"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"

	"github.com/home-operations/kritika/internal/agent"
	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/runspec"
	"github.com/home-operations/kritika/internal/textcut"
	"github.com/home-operations/kritika/internal/upstream"
)

// What fetch_oci can take: a run inspects at most the spec's fetches, as
// many as fetch_repo can fetch, and lists tags at most listMax times. One
// call takes at most ociWireBytes from the registry and ociTimeout of wall
// time. It reads a manifest or config of at most ociMetadataBytes. It shows a
// config that is no image's cut to ociConfigShown, and a tag listing at most
// ociTagPages pages long.
const (
	ociWireBytes     = 16 << 20
	ociTimeout       = time.Minute
	ociMetadataBytes = 4 << 20
	ociConfigShown   = 8 << 10
	ociTagPages      = 50
)

// dockerMediaTypes are the Docker media types an image's manifest list,
// manifest and config come under, beside the OCI ones.
const (
	dockerManifestList = "application/vnd.docker.distribution.manifest.list.v2+json"
	dockerManifest     = "application/vnd.docker.distribution.manifest.v2+json"
	dockerConfig       = "application/vnd.docker.container.image.v1+json"
)

var fetchOCISchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"image": {
			"type": "string",
			"description": "registry/repository with a tag or digest, such as ghcr.io/home-operations/postgres:17.5 or postgres:17@sha256:…"
		},
		"tags": {
			"type": "string",
			"description": "Instead of inspecting: list the repository's tags whose names contain this, such as 17. or -alpine; empty for all."
		}
	},
	"required": ["image"],
	"additionalProperties": false
}`)

// fetchOCITool is fetch_oci: it inspects an image or artifact in a
// registry without pulling it, through the gateway and anonymously as
// fetch_repo fetches, and lists a repository's tags. It is offered beside
// fetch_repo, under the same condition.
type fetchOCITool struct {
	// transport carries the reads. nil is upstream's default, through the
	// pod's proxy.
	transport http.RoundTripper
	// fetchMax is how many inspections the review can make.
	fetchMax int
	// hosts name what the gateway lets a read reach, for the description.
	// Empty leaves them unnamed.
	hosts []string

	fetches  int
	listings int
	sources  []string
}

// newFetchOCITool is the tool for a run of p.
func newFetchOCITool(p runspec.Spec) *fetchOCITool {
	return &fetchOCITool{fetchMax: cmp.Or(p.Agent.Fetches, configfile.DefaultFetches), hosts: p.Agent.Hosts}
}

func (t *fetchOCITool) Def() model.ToolDef {
	desc := "Inspect a container image or an OCI artifact in a registry without pulling it: the digest its tag resolves to, " +
		"its media type and platforms, and its config: the labels and annotations, such as org.opencontainers.image.source, " +
		"the repository an image was built from, and .version and .revision, the user, entrypoint, command, ports, " +
		"environment, created time, layer count and size. For a Helm chart or another artifact, its config document. " +
		"With tags instead, it lists the repository's tags whose names contain the text, to find what a tag moved to " +
		"rather than guess. An index that names no platform, or a manifest of another kind, is described by its digest " +
		"and media type alone. " +
		fmt.Sprintf("A review may inspect %d images and list tags %d times.", t.fetchMax, listMax) +
		` For example, {"image": "ghcr.io/home-operations/postgres:17.5"} shows what that tag is, and ` +
		`{"image": "ghcr.io/home-operations/postgres", "tags": "17."} lists the tags with 17. in their names.` +
		" The read is anonymous: it reads a public registry, never a private one."
	if len(t.hosts) > 0 {
		desc += " The read goes through the gateway, which allows only " + agent.AllowedHosts(t.hosts) +
			"; a registry's token endpoint and blob storage must be allowed beside it."
	}
	return model.ToolDef{Name: "fetch_oci", Description: desc, InputSchema: fetchOCISchema}
}

func (t *fetchOCITool) Run(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Image string `json:"image"`
		// Tags is set, to "" for every tag, when the call lists tags.
		Tags *string `json:"tags"`
	}
	if len(input) > 0 {
		if err := json.Unmarshal(input, &in); err != nil {
			return "", fmt.Errorf("agent: fetch_oci: %w", err)
		}
	}
	ref, err := registry.ParseReference(dockerHubName(strings.TrimSpace(in.Image)))
	if err != nil {
		return "", fmt.Errorf("agent: fetch_oci: %w: give registry/repository with a tag or digest", err)
	}
	// A model that sends every property sends tags as "" beside a tag or
	// digest, which asks for nothing, as fetch_repo reads it.
	switch {
	case in.Tags != nil && ref.Reference == "":
		return t.listTags(ctx, ref, *in.Tags)
	case in.Tags != nil && *in.Tags != "":
		return "", errors.New("agent: fetch_oci: give a tag or digest to inspect, or tags to list the repository's tags, not both")
	case ref.Reference == "":
		return "", errors.New("agent: fetch_oci: give a tag or digest to inspect, or tags to list the repository's tags")
	}
	if t.fetches >= t.fetchMax {
		return "", fmt.Errorf("agent: fetch_oci: this review has inspected its %d images", t.fetchMax)
	}
	t.fetches++
	rctx, cancel := context.WithTimeout(ctx, ociTimeout)
	defer cancel()
	repo, done := t.repository(ref)
	defer done()
	out, err := inspect(rctx, repo, ref)
	if err != nil {
		return "", fmt.Errorf("agent: fetch_oci: %s: %w", ref, err)
	}
	t.record(ref)
	return out, nil
}

// repository is the client for ref's repository: anonymous, so the
// registry's challenge is answered with a token it grants anyone, through
// the capped transport.
func (t *fetchOCITool) repository(ref registry.Reference) (*remote.Repository, func()) {
	wire, done := upstream.CappedTransport(t.transport, ociWireBytes)
	// The cache keeps the token the registry grants for the repository, so
	// the index, the manifest and the config are read on one.
	repo := &remote.Repository{
		Reference: ref, Client: &auth.Client{Client: &http.Client{Transport: wire}, Cache: auth.NewCache()},
		MaxMetadataBytes: ociMetadataBytes, TagListMaxPages: ociTagPages,
	}
	return repo, done
}

// dockerHubName is image with the registry and namespace a bare Docker
// Hub name leaves out: postgres:17 is docker.io/library/postgres:17 and
// bitnami/redis is docker.io/bitnami/redis. A first part with a dot or a
// colon, or localhost, is a registry already.
func dockerHubName(image string) string {
	first, rest, found := strings.Cut(image, "/")
	switch {
	case !found:
		return "docker.io/library/" + image
	case strings.ContainsAny(first, ".:") || first == "localhost":
		return image
	}
	return "docker.io/" + first + "/" + rest
}

// Sources are the images the tool inspected, as oci:// references.
func (t *fetchOCITool) Sources() []string { return append([]string{}, t.sources...) }

func (t *fetchOCITool) record(ref registry.Reference) {
	if s := "oci://" + ref.String(); !slices.Contains(t.sources, s) {
		t.sources = append(t.sources, s)
	}
}

// listTags answers a call that lists the tags of ref's repository whose
// names contain match, as the registry orders them.
func (t *fetchOCITool) listTags(ctx context.Context, ref registry.Reference, match string) (string, error) {
	if t.listings >= listMax {
		return "", fmt.Errorf("agent: fetch_oci: this review has listed tags %d times", listMax)
	}
	t.listings++
	lctx, cancel := context.WithTimeout(ctx, ociTimeout)
	defer cancel()
	repo, done := t.repository(ref)
	defer done()
	var tags []string
	err := repo.Tags(lctx, "", func(page []string) error {
		tags = append(tags, page...)
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("agent: fetch_oci: %s: %w", ref.Registry+"/"+ref.Repository, err)
	}
	return tagList(ref.Registry+"/"+ref.Repository, match, tags, "as the registry lists them"), nil
}

// inspect resolves ref and describes what it names: an index by its
// platforms and the config of its first image, a manifest by its config,
// and an artifact by its config document.
func inspect(ctx context.Context, repo *remote.Repository, ref registry.Reference) (string, error) {
	desc, raw, err := repo.Manifests().FetchReference(ctx, ref.Reference)
	if err != nil {
		return "", err
	}
	body, err := readManifest(raw, desc)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s is %s, %s", ref, desc.Digest, desc.MediaType)
	var manifest ocispec.Manifest
	switch desc.MediaType {
	case ocispec.MediaTypeImageIndex, dockerManifestList:
		var index ocispec.Index
		if err := json.Unmarshal(body, &index); err != nil {
			return "", fmt.Errorf("decode index: %w", err)
		}
		var platforms []string
		for _, m := range index.Manifests {
			if p := platformName(m.Platform); p != "" && !slices.Contains(platforms, p) {
				platforms = append(platforms, p)
			}
		}
		fmt.Fprintf(&b, ", for %s.", orNone(strings.Join(platforms, ", "), "no platform it names"))
		writeAnnotations(&b, index.Annotations)
		// A nested index or an attestation is no image: the first image
		// manifest with a platform stands for the index.
		first := slices.IndexFunc(index.Manifests, func(m ocispec.Descriptor) bool {
			return platformName(m.Platform) != "" && (m.MediaType == ocispec.MediaTypeImageManifest || m.MediaType == dockerManifest)
		})
		if first < 0 {
			return b.String(), nil
		}
		chosen := index.Manifests[first]
		raw, err := repo.Manifests().Fetch(ctx, chosen)
		if err != nil {
			return "", err
		}
		if body, err = readManifest(raw, chosen); err != nil {
			return "", err
		}
		if err := json.Unmarshal(body, &manifest); err != nil {
			return "", fmt.Errorf("decode manifest %s: %w", chosen.Digest, err)
		}
		fmt.Fprintf(&b, "\nIts %s image, %s", platformName(chosen.Platform), chosen.Digest)
		// The descriptor's annotations and the manifest's own both carry
		// provenance. The manifest's win where they differ.
		annotations := maps.Clone(chosen.Annotations)
		if annotations == nil {
			annotations = map[string]string{}
		}
		maps.Copy(annotations, manifest.Annotations)
		if len(annotations) > 0 {
			b.WriteString(",")
			writeAnnotations(&b, annotations)
			b.WriteString("\nIt")
		}
	case ocispec.MediaTypeImageManifest, dockerManifest:
		if err := json.Unmarshal(body, &manifest); err != nil {
			return "", fmt.Errorf("decode manifest: %w", err)
		}
		if manifest.ArtifactType != "" {
			fmt.Fprintf(&b, ", artifact type %s", manifest.ArtifactType)
		}
		b.WriteString(".")
		writeAnnotations(&b, manifest.Annotations)
		b.WriteString("\nIt")
	default:
		b.WriteString(".")
		return b.String(), nil
	}
	var size int64
	for _, l := range manifest.Layers {
		size += l.Size
	}
	fmt.Fprintf(&b, " has %d layers, %d MiB, and a config of type %s", len(manifest.Layers), (size+(1<<20)-1)>>20, manifest.Config.MediaType)
	if manifest.Config.Size > ociMetadataBytes {
		fmt.Fprintf(&b, " of %d bytes, too large to read.", manifest.Config.Size)
		return b.String(), nil
	}
	config, err := content.FetchAll(ctx, repo.Blobs(), manifest.Config)
	if err != nil {
		return "", fmt.Errorf("read config %s: %w", manifest.Config.Digest, err)
	}
	b.WriteString(":")
	if manifest.Config.MediaType != ocispec.MediaTypeImageConfig && manifest.Config.MediaType != dockerConfig {
		fmt.Fprintf(&b, "\n%s", textcut.Truncate(string(config), ociConfigShown))
		return b.String(), nil
	}
	var image ocispec.Image
	if err := json.Unmarshal(config, &image); err != nil {
		return "", fmt.Errorf("decode config %s: %w", manifest.Config.Digest, err)
	}
	writeImageConfig(&b, image)
	return b.String(), nil
}

// readManifest reads the manifest rc holds whole, against desc's size and
// digest, and closes it. A manifest over ociMetadataBytes is refused unread,
// since the registry's size is trusted only that far.
func readManifest(rc io.ReadCloser, desc ocispec.Descriptor) ([]byte, error) {
	defer func() { _ = rc.Close() }()
	if desc.Size > ociMetadataBytes {
		return nil, fmt.Errorf("manifest %s is %d bytes, over the %d MiB read", desc.Digest, desc.Size, ociMetadataBytes>>20)
	}
	return content.ReadAll(rc, desc)
}

// writeImageConfig describes an image's config: what it runs as and how,
// and its labels.
func writeImageConfig(b *strings.Builder, image ocispec.Image) {
	if image.Created != nil {
		fmt.Fprintf(b, "\n- created: %s", image.Created.UTC().Format(time.RFC3339))
	}
	if p := platformName(&image.Platform); p != "" {
		fmt.Fprintf(b, "\n- platform: %s", p)
	}
	fmt.Fprintf(b, "\n- user: %s", orNone(image.Config.User, "root (unset)"))
	fmt.Fprintf(b, "\n- entrypoint: %s", orNone(strings.Join(image.Config.Entrypoint, " "), "none"))
	fmt.Fprintf(b, "\n- cmd: %s", orNone(strings.Join(image.Config.Cmd, " "), "none"))
	if image.Config.WorkingDir != "" {
		fmt.Fprintf(b, "\n- workdir: %s", image.Config.WorkingDir)
	}
	fmt.Fprintf(b, "\n- ports: %s", orNone(strings.Join(sortedKeys(image.Config.ExposedPorts), ", "), "none"))
	fmt.Fprintf(b, "\n- volumes: %s", orNone(strings.Join(sortedKeys(image.Config.Volumes), ", "), "none"))
	if len(image.Config.Env) > 0 {
		fmt.Fprintf(b, "\n- env:\n  %s", strings.Join(image.Config.Env, "\n  "))
	}
	if len(image.Config.Labels) > 0 {
		b.WriteString("\n- labels:")
		for _, k := range sortedKeys(image.Config.Labels) {
			fmt.Fprintf(b, "\n  %s: %s", k, strings.Join(strings.Fields(image.Config.Labels[k]), " "))
		}
	}
}

// writeAnnotations lists a manifest's or index's annotations, where a
// registry's build tools often put the source and revision.
func writeAnnotations(b *strings.Builder, annotations map[string]string) {
	if len(annotations) == 0 {
		return
	}
	b.WriteString("\nAnnotations:")
	for _, k := range sortedKeys(annotations) {
		fmt.Fprintf(b, "\n  %s: %s", k, strings.Join(strings.Fields(annotations[k]), " "))
	}
}

// platformName is os/architecture[/variant], "" for a platform not given
// or unknown, as an attestation manifest's is.
func platformName(p *ocispec.Platform) string {
	if p == nil || p.OS == "" || p.OS == "unknown" {
		return ""
	}
	name := p.OS + "/" + p.Architecture
	if p.Variant != "" {
		name += "/" + p.Variant
	}
	return name
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func orNone(s, none string) string {
	if s == "" {
		return none
	}
	return s
}
