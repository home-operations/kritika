package repoconfig

import (
	"reflect"
	"strings"
	"testing"

	"github.com/home-operations/kritika/internal/configfile"
)

func TestParseTools_Invalid(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, yaml, want string }{
		{"no version", "tools:\n  aqua:cli/cli:\n", "a version is required"},
		{"a table", "tools:\n  aqua:cli/cli: { version: v2.83.0 }\n", "not a table or a list"},
		{"a short name", "tools:\n  helm: 4.3.1\n", "only aqua registry packages are offered"},
		{"another source", "tools:\n  \"github:home-operations/flate\": v0.6.5\n", "only aqua registry packages are offered"},
		{"no repository", "tools:\n  aqua:flate: v0.6.5\n", "not an aqua registry package's name"},
		{"a path up", "tools:\n  aqua:acme/../tool: v1.0.0\n", "not an aqua registry package's name"},
		{"a version suffix", "tools:\n  \"aqua:cli/cli@v2.83.0\": v2.83.0\n", "not an aqua registry package's name"},
		{"latest", "tools:\n  aqua:cli/cli: latest\n", "not pinned"},
		{"a word", "tools:\n  aqua:cli/cli: nightly\n", "not pinned"},
		{"a template", "tools:\n  aqua:cli/cli: \"{{.Version}}\"\n", "not pinned"},
		{"a space", "tools:\n  aqua:cli/cli: \"v2 83\"\n", "not pinned"},
		{"a path up in the version", "tools:\n  aqua:cli/cli: v2/../1\n", "not pinned"},
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
  aqua:home-operations/flate: v0.6.5
  aqua:BurntSushi/ripgrep: 15.1.0
  aqua:kubernetes-sigs/kustomize: kustomize/v5.8.3
  aqua:jqlang/jq: jq-1.8.1
  aqua:apple/pkl-go/pkl-gen-go: v0.12.1
`
	f, err := Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	want := Tools{
		"aqua:home-operations/flate":     "v0.6.5",
		"aqua:BurntSushi/ripgrep":        "15.1.0",
		"aqua:kubernetes-sigs/kustomize": "kustomize/v5.8.3",
		"aqua:jqlang/jq":                 "jq-1.8.1",
		"aqua:apple/pkl-go/pkl-gen-go":   "v0.12.1",
	}
	if !reflect.DeepEqual(f.Tools, want) {
		t.Fatalf("tools = %#v", f.Tools)
	}
}

func TestMergeTools(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, doc string
		want      Tools
	}{
		{"none", "ignore: [a]\n", nil},
		{"declared", "tools: { aqua:cli/cli: v2.83.0 }\n", Tools{"aqua:cli/cli": "v2.83.0"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m, err := Merge([]byte(tt.doc), configfile.Settings{})
			if err != nil || !reflect.DeepEqual(m.Tools, tt.want) {
				t.Fatalf("Merge = %v, %v; want %v", m.Tools, err, tt.want)
			}
		})
	}
}
