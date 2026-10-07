package runner

import (
	"encoding/json"
	"maps"
	"os/exec"
	"slices"
	"testing"
	"time"

	"github.com/home-operations/kritika/internal/agent"
	"github.com/home-operations/kritika/internal/repoconfig"
)

// TestToolsTakeZeroValuesAsLeftOut: a model may send every property of a
// tool's input, the ones it does not mean as zero values, so an optional
// property given its zero value does what leaving it out does.
func TestToolsTakeZeroValuesAsLeftOut(t *testing.T) {
	head := tree(t, map[string]string{
		"main.go":                    "package main\n",
		".agents/skills/go/SKILL.md": skillDoc("description: Go.", "Wrap errors.\n"),
	})
	url, served := servedRepo(t)
	trueBin, err := exec.LookPath("true")
	if err != nil {
		t.Fatal(err)
	}
	const limit = 4096
	for _, tt := range []struct {
		name string
		base map[string]any
		tool func() agent.Tool
	}{
		{"read_file", map[string]any{"path": "main.go"}, func() agent.Tool { return agent.ReadFileTool(agent.NewTree(head, nil), limit) }},
		{"grep", map[string]any{"pattern": "package"}, func() agent.Tool { return agent.GrepTool(agent.NewTree(head, nil), limit) }},
		{"list_files", map[string]any{}, func() agent.Tool { return agent.ListFilesTool(agent.NewTree(head, nil), limit) }},
		{"read_description", map[string]any{}, func() agent.Tool {
			return agent.ReadDescriptionTool("The description.", map[int]string{7: "The issue."}, limit)
		}},
		{"run", map[string]any{"command": "true"}, func() agent.Tool {
			return agent.NewRunTool(agent.RunConfig{
				Dir: t.TempDir(), Commands: map[string]string{"true": trueBin}, Timeout: 10 * time.Second, MaxOutputBytes: limit,
			})
		}},
		{"fetch_repo fetching", map[string]any{"url": url, "ref": "v2"}, func() agent.Tool {
			return &fetchRepoTool{dir: t.TempDir(), transport: served.transport}
		}},
		{"fetch_repo listing tags", map[string]any{"url": url, "tags": "v"}, func() agent.Tool {
			return &fetchRepoTool{dir: t.TempDir(), transport: served.transport}
		}},
		{"load_skill", map[string]any{"name": "go"}, func() agent.Tool {
			return &skillTool{base: head, maxBytes: limit, skills: []repoconfig.Skill{{Name: "go", Description: "Go.", Dir: ".agents/skills/go"}}}
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			full := withZeroOptionals(t, tt.tool().Def().InputSchema, tt.base)
			want, wantErr := tt.tool().Run(t.Context(), fetchInput(t, tt.base))
			if wantErr != nil {
				t.Fatalf("with the optional properties left out: %v", wantErr)
			}
			got, err := tt.tool().Run(t.Context(), fetchInput(t, full))
			if err != nil || got != want {
				t.Fatalf("with them as zero values (%v) = %q, %v; left out = %q", full, got, err, want)
			}
		})
	}
}

// withZeroOptionals is base with every optional property of schema that it
// lacks set to its type's zero value.
func withZeroOptionals(t *testing.T, schema json.RawMessage, base map[string]any) map[string]any {
	t.Helper()
	var s struct {
		Properties map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(schema, &s); err != nil {
		t.Fatal(err)
	}
	out := make(map[string]any, len(s.Properties))
	for name, p := range s.Properties {
		if _, ok := base[name]; ok || slices.Contains(s.Required, name) {
			continue
		}
		switch p.Type {
		case "string":
			out[name] = ""
		case "integer", "number":
			out[name] = 0
		case "boolean":
			out[name] = false
		case "array":
			out[name] = []any{}
		case "object":
			out[name] = map[string]any{}
		default:
			t.Fatalf("property %s has type %q", name, p.Type)
		}
	}
	if len(out) == 0 {
		t.Fatalf("the schema has no optional property beside %v", base)
	}
	maps.Copy(out, base)
	return out
}
