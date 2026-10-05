package repoconfig

import (
	"encoding/json"
	"maps"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/home-operations/kritika/internal/configfile"
)

// TestSchemaMatchesFile keeps the published JSON Schema's keys in step
// with the types .kritika.yaml decodes into, object by object.
func TestSchemaMatchesFile(t *testing.T) {
	raw, err := os.ReadFile("../../docs/kritika.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		path []string
		want []string
	}{
		{"the file", nil, yamlKeys[File]()},
		{"review", []string{"properties", "review"}, yamlKeys[Review]()},
		{"confidence", []string{"properties", "confidence"}, yamlKeys[Confidence]()},
		{"trigger", []string{"properties", "trigger"}, yamlKeys[Trigger]()},
		{"include", []string{"properties", "trigger", "properties", "include", "items"}, yamlKeys[configfile.Filter]()},
		{"exclude", []string{"properties", "trigger", "properties", "exclude", "items"}, yamlKeys[configfile.Filter]()},
		{"comments", []string{"properties", "comments"}, yamlKeys[Comments]()},
		{"context", []string{"properties", "context", "items"}, yamlKeys[configfile.ContextFile]()},
		{"rules", []string{"properties", "rules", "items"}, yamlKeys[configfile.Rule]()},
		{"rule when", []string{"properties", "rules", "items", "properties", "when", "items"}, yamlKeys[configfile.When]()},
		{"skills", []string{"properties", "skills"}, yamlKeys[configfile.SkillsSpec]()},
		{"skill scope", []string{"properties", "skills", "properties", "scope", "additionalProperties"}, yamlKeys[configfile.SkillScope]()},
		{"skill scope when", []string{"properties", "skills", "properties", "scope", "additionalProperties", "properties", "when", "items"},
			yamlKeys[configfile.When]()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			node := any(schema)
			for _, k := range tt.path {
				switch n := node.(type) {
				case map[string]any:
					node = n[k]
				case []any:
					i, _ := strconv.Atoi(k)
					node = n[i]
				}
			}
			if ref, ok := node.(map[string]any)["$ref"].(string); ok {
				node = schema["$defs"].(map[string]any)[strings.TrimPrefix(ref, "#/$defs/")]
			}
			props, _ := node.(map[string]any)["properties"].(map[string]any)
			got := slices.Sorted(maps.Keys(props))
			slices.Sort(tt.want)
			if !slices.Equal(got, tt.want) {
				t.Fatalf("schema keys %v, want the decoder's %v", got, tt.want)
			}
		})
	}
}

// yamlKeys lists the keys T decodes from.
func yamlKeys[T any]() []string { return leafKeys(reflect.TypeFor[T](), "", false) }

// TestFileKeysAreSettings checks the keys .kritika.yaml takes are settings
// an admin writes too, spelled the same.
func TestFileKeysAreSettings(t *testing.T) {
	keys := make([]string, 0, len(configfile.Policies))
	for _, p := range configfile.Policies {
		keys = append(keys, p.Key)
	}
	for _, key := range leafKeys(reflect.TypeFor[File](), "", true) {
		if !slices.ContainsFunc(keys, func(k string) bool { return key == k || strings.HasPrefix(key, k+".") }) {
			t.Errorf("the file takes %s, which is no setting an admin writes", key)
		}
		if _, ok := configfile.SpecValue(&configfile.Overrides{}, key); !ok {
			t.Errorf("the file takes %s, which an admin's entry has no key for", key)
		}
	}
}

// leafKeys lists the keys t decodes from, an inline block's among them:
// dotted down to the settings when nested, not looking into lists, and t's
// own otherwise.
func leafKeys(t reflect.Type, prefix string, nested bool) []string {
	var out []string
	for f := range t.Fields() {
		name, opts, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		if opts == "inline" {
			out = append(out, leafKeys(f.Type, prefix, nested)...)
			continue
		}
		if name == "" || name == "-" {
			continue
		}
		if nested && f.Type.Kind() == reflect.Struct {
			out = append(out, leafKeys(f.Type, prefix+name+".", nested)...)
			continue
		}
		out = append(out, prefix+name)
	}
	return out
}
