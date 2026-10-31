package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// fakeRegistry serves one repository, "acme/app", over the distribution
// API as a registry that grants anyone a token does: a tag that is an
// index of two platforms, a tag that is a chart, and the tag list. Every
// read is counted by kind, and a blob is served through a redirect, as
// registries hand blobs to their storage.
type fakeRegistry struct {
	srv   *httptest.Server
	blobs map[string][]byte
	// manifests maps a tag or digest to the manifest and its media type.
	manifests map[string]struct {
		body      []byte
		mediaType string
	}
	tokens, redirects int
}

func digestOf(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func newFakeRegistry(t *testing.T) *fakeRegistry {
	t.Helper()
	r := &fakeRegistry{blobs: map[string][]byte{}, manifests: map[string]struct {
		body      []byte
		mediaType string
	}{}}
	blob := func(mediaType string, v any) map[string]any {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		d := digestOf(b)
		r.blobs[d] = b
		return map[string]any{"mediaType": mediaType, "digest": d, "size": len(b)}
	}
	manifest := func(key, mediaType string, v any) map[string]any {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		d := digestOf(b)
		m := struct {
			body      []byte
			mediaType string
		}{b, mediaType}
		r.manifests[key], r.manifests[d] = m, m
		return map[string]any{"mediaType": mediaType, "digest": d, "size": len(b)}
	}
	config := blob("application/vnd.oci.image.config.v1+json", map[string]any{
		"created": "2026-10-01T12:00:00Z", "architecture": "amd64", "os": "linux",
		"config": map[string]any{
			"User": "65534", "Entrypoint": []string{"/usr/bin/app"}, "Cmd": []string{"serve"}, "ExposedPorts": map[string]any{"8080/tcp": map[string]any{}},
			"Env":    []string{"PATH=/usr/bin", "APP_MODE=prod"},
			"Labels": map[string]string{"org.opencontainers.image.source": "https://github.com/acme/app", "org.opencontainers.image.version": "1.2.3"},
		},
	})
	layer := blob("application/vnd.oci.image.layer.v1.tar+gzip", map[string]any{"layer": strings.Repeat("x", 3000)})
	amd64 := manifest("amd64", "application/vnd.oci.image.manifest.v1+json", map[string]any{
		"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json", "config": config, "layers": []any{layer, layer},
	})
	amd64["platform"] = map[string]string{"os": "linux", "architecture": "amd64"}
	arm64 := manifest("arm64", "application/vnd.oci.image.manifest.v1+json", map[string]any{
		"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json", "config": config, "layers": []any{layer},
	})
	arm64["platform"] = map[string]string{"os": "linux", "architecture": "arm64", "variant": "v8"}
	amd64["annotations"] = map[string]string{"org.opencontainers.image.source": "https://github.com/acme/app"}
	attestation := map[string]any{"mediaType": "application/vnd.oci.image.manifest.v1+json", "digest": digestOf([]byte("att")), "size": 3,
		"platform": map[string]string{"os": "unknown", "architecture": "unknown"}}
	nested := map[string]any{"mediaType": "application/vnd.oci.image.index.v1+json", "digest": digestOf([]byte("nested")), "size": 6,
		"platform": map[string]string{"os": "linux", "architecture": "amd64"}}
	manifest("1.2.3", "application/vnd.oci.image.index.v1+json", map[string]any{
		"schemaVersion": 2, "mediaType": "application/vnd.oci.image.index.v1+json", "manifests": []any{nested, amd64, arm64, attestation},
		"annotations": map[string]string{"org.opencontainers.image.revision": "abc1234"},
	})
	chart := blob("application/vnd.cncf.helm.config.v1+json", map[string]any{"name": "app", "version": "0.4.0", "sources": []string{"https://github.com/acme/app"}})
	manifest("chart-0.4.0", "application/vnd.oci.image.manifest.v1+json", map[string]any{
		"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json", "config": chart,
		"layers": []any{blob("application/vnd.cncf.helm.chart.content.v1.tar+gzip", map[string]any{"chart": "tgz"})},
	})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /token", func(w http.ResponseWriter, req *http.Request) {
		r.tokens++
		_, _ = fmt.Fprint(w, `{"token":"anyone"}`)
	})
	challenge := func(w http.ResponseWriter, req *http.Request) bool {
		if req.Header.Get("Authorization") == "Bearer anyone" {
			return false
		}
		w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm="%s/token",service="fake",scope="repository:acme/app:pull"`, r.srv.URL))
		w.WriteHeader(http.StatusUnauthorized)
		return true
	}
	mux.HandleFunc("GET /v2/", func(w http.ResponseWriter, req *http.Request) {
		if !challenge(w, req) {
			w.WriteHeader(http.StatusOK)
		}
	})
	mux.HandleFunc("GET /v2/acme/app/manifests/{ref}", func(w http.ResponseWriter, req *http.Request) {
		if challenge(w, req) {
			return
		}
		m, ok := r.manifests[req.PathValue("ref")]
		if !ok {
			http.Error(w, `{"errors":[{"code":"MANIFEST_UNKNOWN"}]}`, http.StatusNotFound)
			return
		}
		// A registry sizes a manifest in the header, which a HEAD and the
		// refusal of a large manifest read. Otherwise, Go sends a large body
		// chunked.
		w.Header().Set("Content-Type", m.mediaType)
		w.Header().Set("Content-Length", strconv.Itoa(len(m.body)))
		w.Header().Set("Docker-Content-Digest", digestOf(m.body))
		_, _ = w.Write(m.body)
	})
	mux.HandleFunc("GET /v2/acme/app/blobs/{digest}", func(w http.ResponseWriter, req *http.Request) {
		if challenge(w, req) {
			return
		}
		r.redirects++
		http.Redirect(w, req, r.srv.URL+"/storage/"+req.PathValue("digest"), http.StatusTemporaryRedirect)
	})
	mux.HandleFunc("GET /storage/{digest}", func(w http.ResponseWriter, req *http.Request) {
		b, ok := r.blobs[req.PathValue("digest")]
		if !ok {
			http.NotFound(w, req)
			return
		}
		_, _ = w.Write(b)
	})
	mux.HandleFunc("GET /v2/acme/app/tags/list", func(w http.ResponseWriter, req *http.Request) {
		if challenge(w, req) {
			return
		}
		_, _ = fmt.Fprint(w, `{"name":"acme/app","tags":["1.2.2","1.2.3","chart-0.4.0"]}`)
	})
	r.srv = httptest.NewTLSServer(mux)
	t.Cleanup(r.srv.Close)
	return r
}

// host is the registry as a reference names it.
func (r *fakeRegistry) host() string { return strings.TrimPrefix(r.srv.URL, "https://") }

func (r *fakeRegistry) tool() *fetchOCITool {
	return &fetchOCITool{transport: r.srv.Client().Transport, fetchMax: 2}
}

// TestFetchOCIInspect: an index is described by its platforms, the
// attestation's unknown platform left out. It is also described by the config
// of its first image manifest, a nested index passed over, read through the
// registry's redirect. The token that the registry grants anyone is fetched
// once and reused. The image is a source.
func TestFetchOCIInspect(t *testing.T) {
	reg := newFakeRegistry(t)
	tool := reg.tool()
	out, err := tool.Run(t.Context(), fetchInput(t, map[string]any{"image": reg.host() + "/acme/app:1.2.3"}))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		reg.host() + "/acme/app:1.2.3 is sha256:",
		"application/vnd.oci.image.index.v1+json, for linux/amd64, linux/arm64/v8.",
		"\nAnnotations:\n  org.opencontainers.image.revision: abc1234",
		"\nIts linux/amd64 image, sha256:",
		",\nAnnotations:\n  org.opencontainers.image.source: https://github.com/acme/app\nIt has 2 layers",
		" has 2 layers, 1 MiB, and a config of type application/vnd.oci.image.config.v1+json:",
		"\n- created: 2026-10-01T12:00:00Z", "\n- platform: linux/amd64", "\n- user: 65534", "\n- entrypoint: /usr/bin/app", "\n- cmd: serve",
		"\n- ports: 8080/tcp", "\n- volumes: none", "\n- env:\n  PATH=/usr/bin\n  APP_MODE=prod",
		"\n- labels:\n  org.opencontainers.image.source: https://github.com/acme/app\n  org.opencontainers.image.version: 1.2.3",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("answer lacks %q:\n%s", want, out)
		}
	}
	if reg.tokens != 1 || reg.redirects != 1 {
		t.Fatalf("tokens fetched %d, blob redirects %d; want one each", reg.tokens, reg.redirects)
	}
	if got := tool.Sources(); len(got) != 1 || got[0] != "oci://"+reg.host()+"/acme/app:1.2.3" {
		t.Fatalf("sources = %v", got)
	}
}

// TestFetchOCIArtifact: a chart's config document is shown as it is, and a
// digest reads the same as its tag.
func TestFetchOCIArtifact(t *testing.T) {
	reg := newFakeRegistry(t)
	out, err := reg.tool().Run(t.Context(), fetchInput(t, map[string]any{"image": reg.host() + "/acme/app:chart-0.4.0"}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "application/vnd.oci.image.manifest.v1+json.\nIt has 1 layers, 1 MiB, and a config of type "+
		"application/vnd.cncf.helm.config.v1+json:\n{\"name\":\"app\",\"sources\":[\"https://github.com/acme/app\"],\"version\":\"0.4.0\"}") {
		t.Fatalf("chart answer:\n%s", out)
	}
	d := digestOf(reg.manifests["chart-0.4.0"].body)
	byDigest, err := reg.tool().Run(t.Context(), fetchInput(t, map[string]any{"image": reg.host() + "/acme/app@" + d}))
	if err != nil || !strings.Contains(byDigest, "is "+d+", ") {
		t.Fatalf("by digest: %v\n%s", err, byDigest)
	}
}

// TestFetchOCIErrors: a missing tag, a reference without one, a reference
// that is none, and the review's inspections spent each give a readable
// error. A failed inspection counts among those spent. A Docker Hub name
// without a registry is filled in.
func TestFetchOCIErrors(t *testing.T) {
	reg := newFakeRegistry(t)
	tool := reg.tool()
	for _, tt := range []struct{ name, image, want string }{
		{"a missing tag", reg.host() + "/acme/app:9.9.9", "not found"},
		{"no tag", reg.host() + "/acme/app", "give a tag or digest"},
		{"a reference that is none", "ghcr.io/acme/app:1 2", "invalid reference"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tool.Run(t.Context(), fetchInput(t, map[string]any{"image": tt.image}))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tt.want)
			}
		})
	}
	if _, err := tool.Run(t.Context(), fetchInput(t, map[string]any{"image": reg.host() + "/acme/app:1.2.3"})); err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Run(t.Context(), fetchInput(t, map[string]any{"image": reg.host() + "/acme/app:1.2.3"})); err == nil ||
		!strings.Contains(err.Error(), "inspected its 2 images") {
		t.Fatalf("over the limit: %v", err)
	}
	for in, want := range map[string]string{
		"postgres:17": "docker.io/library/postgres:17", "bitnami/redis:7": "docker.io/bitnami/redis:7",
		"ghcr.io/acme/app:1": "ghcr.io/acme/app:1", "localhost:5000/app:1": "localhost:5000/app:1", "registry:2": "docker.io/library/registry:2",
	} {
		if got := dockerHubName(in); got != want {
			t.Errorf("dockerHubName(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestFetchOCITags: tags are listed as the registry orders them, the ones
// with the text. The listings are counted apart from the inspections. An
// empty tags beside a tag asks for nothing, and a text beside one is refused.
func TestFetchOCITags(t *testing.T) {
	reg := newFakeRegistry(t)
	tool := reg.tool()
	out, err := tool.Run(t.Context(), fetchInput(t, map[string]any{"image": reg.host() + "/acme/app", "tags": "1.2"}))
	if err != nil {
		t.Fatal(err)
	}
	if want := "2 of the 3 tags of " + reg.host() + "/acme/app contain \"1.2\", as the registry lists them:\n1.2.2\n1.2.3"; out != want {
		t.Fatalf("tags = %q, want %q", out, want)
	}
	if tool.fetches != 0 || tool.listings != 1 || len(tool.Sources()) != 0 {
		t.Fatalf("fetches %d, listings %d, sources %v", tool.fetches, tool.listings, tool.Sources())
	}
	if out, err := tool.Run(t.Context(), fetchInput(t, map[string]any{"image": reg.host() + "/acme/app:1.2.3", "tags": ""})); err != nil ||
		!strings.Contains(out, "is sha256:") || tool.fetches != 1 || tool.listings != 1 {
		t.Fatalf("an empty tags beside a tag inspects: %v, fetches %d, listings %d\n%s", err, tool.fetches, tool.listings, out)
	}
	if _, err := tool.Run(t.Context(), fetchInput(t, map[string]any{"image": reg.host() + "/acme/app:1.2.3", "tags": "1."})); err == nil ||
		!strings.Contains(err.Error(), "not both") {
		t.Fatalf("a tag beside tags: %v", err)
	}
}

// TestFetchOCILimits: a manifest that the registry sizes over the read is
// refused unread. A manifest of a kind that the tool does not read is
// described by its digest and media type alone.
func TestFetchOCILimits(t *testing.T) {
	reg := newFakeRegistry(t)
	huge, err := json.Marshal(map[string]any{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json",
		"config": map[string]any{}, "layers": []any{}, "pad": strings.Repeat("x", ociMetadataBytes)})
	if err != nil {
		t.Fatal(err)
	}
	reg.manifests["huge"] = struct {
		body      []byte
		mediaType string
	}{huge, "application/vnd.oci.image.manifest.v1+json"}
	reg.manifests["odd"] = struct {
		body      []byte
		mediaType string
	}{[]byte(`{"schemaVersion":1}`), "application/vnd.docker.distribution.manifest.v1+json"}
	tool := &fetchOCITool{transport: reg.srv.Client().Transport, fetchMax: 4}
	if _, err := tool.Run(t.Context(), fetchInput(t, map[string]any{"image": reg.host() + "/acme/app:huge"})); err == nil ||
		!strings.Contains(err.Error(), "over the 4 MiB read") {
		t.Fatalf("huge manifest: %v", err)
	}
	out, err := tool.Run(t.Context(), fetchInput(t, map[string]any{"image": reg.host() + "/acme/app:odd"}))
	if err != nil || !strings.HasSuffix(out, "application/vnd.docker.distribution.manifest.v1+json.") {
		t.Fatalf("odd manifest: %v\n%s", err, out)
	}
}

// TestFetchOCIDef: the description counts what a review can do and names
// the hosts the gateway allows.
func TestFetchOCIDef(t *testing.T) {
	def := (&fetchOCITool{fetchMax: 8, hosts: []string{"ghcr.io"}}).Def()
	if def.Name != "fetch_oci" || !strings.Contains(def.Description, "A review may inspect 8 images and list tags 8 times.") ||
		!strings.Contains(def.Description, "allows only these hosts: ghcr.io; a registry's token endpoint and blob storage must be allowed beside it.") {
		t.Fatalf("def = %+v", def)
	}
	if strings.Contains((&fetchOCITool{fetchMax: 8}).Def().Description, "gateway") {
		t.Fatal("no hosts known, yet the gateway is named")
	}
}
