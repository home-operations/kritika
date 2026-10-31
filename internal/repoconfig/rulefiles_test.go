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
		{
			name: "a horizontal rule is not a marker",
			data: "----\nIntro.\n\n---\n\nBody.\n",
			want: RuleFile{Text: "----\nIntro.\n\n---\n\nBody."},
		},
		{
			name: "a line that starts with the marker does not close it",
			data: "---\npaths: [\"**/*.go\"]\n---oops\nBody.\n",
			want: RuleFile{Text: "---\npaths: [\"**/*.go\"]\n---oops\nBody."},
		},
		{
			name: "a comma in a brace group or a class is the glob's",
			data: "---\npaths: \"src/**/*.{ts,tsx}, docs/[a,b]/**, docs/[{]/**, a\\\\,b/**, lib/**\"\n---\nBody.\n",
			want: RuleFile{Paths: []string{"src/**/*.{ts,tsx}", "docs/[a,b]/**", "docs/[{]/**", "a\\,b/**", "lib/**"}, Scoped: true, Text: "Body."},
		},
		{
			name: "a group that never closes takes no later glob with it",
			data: "---\npaths: 'photos [2024/**, src/*.{ts, **/*.go'\n---\nBody.\n",
			want: RuleFile{Paths: []string{"**/*.go"}, Scoped: true, Text: "Body."},
		},
		{
			name: "an escaped closer does not close a group",
			data: "---\npaths: 'photos [2024\\]/**, src/*.{ts\\}, **/*.go'\n---\nBody.\n",
			want: RuleFile{Paths: []string{"**/*.go"}, Scoped: true, Text: "Body."},
		},
		{
			name: "an inner closer does not close an outer group",
			data: "---\npaths: 'src/{{ts,tsx}, **/*.go'\n---\nBody.\n",
			want: RuleFile{Paths: []string{"**/*.go"}, Scoped: true, Text: "Body."},
		},
		{
			name: "a blank entry seeds no glob",
			data: "---\npaths: ', **/*.go,, lib/**,'\n---\nBody.\n",
			want: RuleFile{Paths: []string{"**/*.go", "lib/**"}, Scoped: true, Text: "Body."},
		},
		{
			name: "a list that is mostly commas",
			data: "---\npaths: '" + strings.Repeat("[,", 1024) + "**/*.go'\n---\nBody.\n",
			want: RuleFile{Paths: []string{"**/*.go"}, Scoped: true, Text: "Body."},
		},
		{
			name: "a long brace group is one glob",
			data: "---\npaths: 'src/**/*.{go,rs,ts,tsx,js,jsx,py,rb,c,cpp,h,hpp,java,kt,swift,scala,sh}, [{,x]/**'\n---\nBody.\n",
			want: RuleFile{
				Paths:  []string{"src/**/*.{go,rs,ts,tsx,js,jsx,py,rb,c,cpp,h,hpp,java,kt,swift,scala,sh}", "[{,x]/**"},
				Scoped: true, Text: "Body.",
			},
		},
		{
			name: "windows line ends",
			data: "---\r\npaths: [\"**/*.go\"]\r\n---\r\nBody.\r\n",
			want: RuleFile{Paths: []string{"**/*.go"}, Scoped: true, Text: "Body."},
		},
		{name: "a byte order mark without frontmatter", data: "\xEF\xBB\xBFBody.\n", want: RuleFile{Text: "Body."}},
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
