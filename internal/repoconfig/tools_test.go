package repoconfig

import (
	"reflect"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"

	"github.com/home-operations/kritika/internal/configfile"
)

func TestParseTools_Invalid(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, yaml, want string }{
		{"no version", "tools:\n  helm:\n", "a version"},
		{"a number for a table", "tools:\n  helm: [4]\n", "a version or a table"},
		{"a table without a version", "tools:\n  helm: { filter_bins: helm }\n", "a version string is required"},
		{"a version that is not a string", "tools:\n  helm: { version: 4 }\n", "a version string is required"},
		{"latest", "tools:\n  helm: latest\n", "not pinned"},
		{"a prefix request", "tools:\n  helm: \"prefix:4\"\n", "not pinned"},
		{"a templated version", "tools:\n  helm: \"{{ get_env(name='V') }}\"\n", "template"},
		{"a build backend", "tools:\n  \"go:golang.org/x/vuln/cmd/govulncheck\": \"v1.9.0\"\n", "go backend is not offered"},
		{"a plugin backend", "tools:\n  \"asdf:mise-plugins/asdf-yarn\": \"1.0.0\"\n", "asdf backend is not offered"},
		{"an install hook", "tools:\n  helm: { version: \"4.3.1\", postinstall: \"helm --help\" }\n", "option postinstall is not one of a download's"},
		{"a templated option", "tools:\n  \"http:tool\": { version: \"1.0.0\", url: \"https://x/{{ version }}\" }\n", "option url holds a template"},
		{"a table for an option", "tools:\n  helm: { version: \"4.3.1\", checksum: { a: b } }\n", "option checksum is not a string"},
		{"a list of tables", "tools:\n  helm: { version: \"4.3.1\", filter_bins: [{ a: b }] }\n", "not a list of strings"},
		{"a list for a string option", "tools:\n  helm: { version: \"4.3.1\", exe: [helm] }\n", "option exe is not a string"},
		{"a boolean for a list option", "tools:\n  helm: { version: \"4.3.1\", os: true }\n", "option os is not a string or a list"},
		{"a string for a boolean", "tools:\n  helm: { version: \"4.3.1\", no_app: \"yes\" }\n", "option no_app is not a boolean"},
		{"a string for an integer", "tools:\n  helm: { version: \"4.3.1\", strip_components: \"1\" }\n", "option strip_components is not a whole number"},
		{"a negative integer", "tools:\n  helm: { version: \"4.3.1\", size: -1 }\n", "option size is not a whole number"},
		{"a file URL", "tools:\n  \"http:tool\": { version: \"1.0.0\", url: \"file:///tmp/tool\" }\n", "option url is not an https:// URL"},
		{"a plain HTTP URL", "tools:\n  \"http:tool\": { version: \"1.0.0\", url: \"http://example.com/tool\" }\n", "option url is not an https:// URL"},
		{"a platform's file URL", "tools:\n  \"http:tool\": { version: \"1.0.0\", platforms: { linux-x64: { url: \"file:///tmp/tool\" } } }\n",
			"platform linux-x64: option url is not an https:// URL"},
		{"options in the name", "tools:\n  \"github:owner/repo[postinstall=id]\": \"1.0.0\"\n", "not a tool name"},
		{"a request in the name", "tools:\n  \"helm@4\": \"4.3.1\"\n", "not a tool name"},
		{"an lts alias", "tools:\n  node: lts/iron\n", "not pinned"},
		{"a hyphenated lts alias", "tools:\n  node: lts-iron\n", "not pinned"},
		{"a hyphenated ref", "tools:\n  helm: ref-main\n", "not pinned"},
		{"a tag request", "tools:\n  helm: \"tag:v4.3.1\"\n", "not pinned"},
		{"a branch request", "tools:\n  helm: branch-main\n", "not pinned"},
		{"a sub request", "tools:\n  helm: \"sub-1:latest\"\n", "not pinned"},
		{"a word", "tools:\n  helm: nightly\n", "not pinned"},
		{"platforms that are not a table", "tools:\n  helm: { version: \"4.3.1\", platforms: linux }\n", "platforms is not a table"},
		{"a platform that is not a table", "tools:\n  helm: { version: \"4.3.1\", platforms: { linux-x64: no } }\n", "platform linux-x64 is not a table"},
		{"a platform's hook", "tools:\n  helm: { version: \"4.3.1\", platforms: { linux-x64: { postinstall: x } } }\n",
			"platform linux-x64: option postinstall"},
		{"a platform's version", "tools:\n  helm: { version: \"4.3.1\", platforms: { linux-x64: { version: \"4.3.0\" } } }\n",
			"platform linux-x64: option version"},
		{"a name with a space", "tools:\n  \"a b\": \"1.0.0\"\n", "not a tool name"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			_, err := Parse([]byte(c.yaml))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("Parse(%q) = %v, want an error with %q", c.yaml, err, c.want)
			}
		})
	}
}

func TestParseTools_Valid(t *testing.T) {
	t.Parallel()
	doc := `tools:
  shellcheck: "0.11.0"
  cosign: 3.1.3
  "github:home-operations/flate": { version: "0.6.5", filter_bins: flate }
  "http:tool":
    version: "1.0.0"
    url: https://example.com/tool.tar.gz
    strip_components: 1
    extract_all: true
    bin: [tool, tool-helper]
    platforms:
      linux-x64: { checksum: "sha256:ab", size: 12 }
      linux-arm64: { checksum: "sha256:cd" }
`
	f, err := Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	want := Tools{
		"shellcheck":                   {Version: "0.11.0"},
		"cosign":                       {Version: "3.1.3"},
		"github:home-operations/flate": {Version: "0.6.5", Options: map[string]any{"filter_bins": "flate"}},
		"http:tool": {Version: "1.0.0", Options: map[string]any{
			"url": "https://example.com/tool.tar.gz", "strip_components": 1, "extract_all": true, "bin": []any{"tool", "tool-helper"},
			"platforms": map[string]any{
				"linux-x64": map[string]any{"checksum": "sha256:ab", "size": 12}, "linux-arm64": map[string]any{"checksum": "sha256:cd"},
			},
		}},
	}
	if !reflect.DeepEqual(f.Tools, want) {
		t.Fatalf("tools = %#v", f.Tools)
	}
	out, err := f.Tools.TOML()
	if err != nil {
		t.Fatal(err)
	}
	// What mise reads back is the table .kritika.yaml declared, the
	// version among each table's options and the integers whole.
	var back struct {
		Tools map[string]any `toml:"tools"`
	}
	if err := toml.Unmarshal([]byte(out), &back); err != nil {
		t.Fatalf("%v:\n%s", err, out)
	}
	wantTable := map[string]any{
		"shellcheck":                   "0.11.0",
		"cosign":                       "3.1.3",
		"github:home-operations/flate": map[string]any{"version": "0.6.5", "filter_bins": "flate"},
		"http:tool": map[string]any{
			"version": "1.0.0", "url": "https://example.com/tool.tar.gz", "strip_components": int64(1), "extract_all": true,
			"bin": []any{"tool", "tool-helper"},
			"platforms": map[string]any{
				"linux-x64": map[string]any{"checksum": "sha256:ab", "size": int64(12)}, "linux-arm64": map[string]any{"checksum": "sha256:cd"},
			},
		},
	}
	if !reflect.DeepEqual(back.Tools, wantTable) {
		t.Fatalf("rendered:\n%s\nread back as %#v", out, back.Tools)
	}
	if none, err := (Tools{}).TOML(); err != nil || none != "" {
		t.Fatalf("no tools render as %q, %v", none, err)
	}
}

func TestMergeTools(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, doc, want string
	}{
		{"none", "ignore: [a]\n", ""},
		{"declared", "tools: { helm: \"4.3.1\" }\n", "[tools]\nhelm = '4.3.1'\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m, err := Merge([]byte(tt.doc), configfile.Settings{})
			if err != nil || m.Tools != tt.want {
				t.Fatalf("Merge = %q, %v; want %q", m.Tools, err, tt.want)
			}
		})
	}
}
