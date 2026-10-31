package repoconfig

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseRuleFile(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		data string
		want RuleFile
	}{
		{name: "no frontmatter applies everywhere", data: "\n# Errors\n\nWrap them.\n", want: RuleFile{Text: "# Errors\n\nWrap them."}},
		{
			name: "paths as a list",
			data: "---\npaths:\n  - \"**/*.rs\"\n  - \"src/**/*.{ts,tsx}\"\n---\n# Errors\n",
			want: RuleFile{Paths: []string{"**/*.rs", "src/**/*.{ts,tsx}"}, Scoped: true, Text: "# Errors"},
		},
		{
			name: "paths as a comma-separated string",
			data: "---\npaths: \"**/*.rs, ./db/**\"\n---\nUse placeholders.",
			want: RuleFile{Paths: []string{"**/*.rs", "db/**"}, Scoped: true, Text: "Use placeholders."},
		},
		{
			name: "other keys are left unread",
			data: "\ufeff---\ndescription: Errors\n---\nWrap them.\n",
			want: RuleFile{Text: "Wrap them."},
		},
		{
			name: "frontmatter that does not parse is dropped",
			data: "---\npaths: [unclosed\n---\nWrap them.\n",
			want: RuleFile{Text: "Wrap them."},
		},
		{
			name: "an invalid glob matches nothing, the others apply",
			data: "---\npaths: [\"photos [2024/**\", \"**/*.go\"]\n---\nWrap them.\n",
			want: RuleFile{Paths: []string{"**/*.go"}, Scoped: true, Text: "Wrap them."},
		},
		{
			name: "every glob invalid matches nothing",
			data: "---\npaths: [\"photos [2024/**\"]\n---\nWrap them.\n",
			want: RuleFile{Scoped: true, Text: "Wrap them."},
		},
		{
			name: "frontmatter that is not closed is text",
			data: "---\npaths: [\"**/*.go\"]\nWrap them.\n",
			want: RuleFile{Text: "---\npaths: [\"**/*.go\"]\nWrap them."},
		},
		{name: "frontmatter alone holds no rule", data: "---\npaths: [\"**/*.go\"]\n---", want: RuleFile{Paths: []string{"**/*.go"}, Scoped: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := ParseRuleFile([]byte(tt.data)); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ParseRuleFile = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestRuleFileID(t *testing.T) {
	t.Parallel()
	tests := []struct{ rel, want string }{
		{"error-handling.md", "error-handling"},
		{"backend/API_Routes.md", "backend-api-routes"},
		{"--weird  name--.md", "weird-name"},
		{"___.md", ""},
		{strings.Repeat("a", 70) + ".md", strings.Repeat("a", 64)},
		{strings.Repeat("a", 63) + "-b.md", strings.Repeat("a", 63)},
	}
	for _, tt := range tests {
		t.Run(tt.rel, func(t *testing.T) {
			t.Parallel()
			if got := RuleFileID(tt.rel); got != tt.want {
				t.Fatalf("RuleFileID(%q) = %q, want %q", tt.rel, got, tt.want)
			}
		})
	}
}
