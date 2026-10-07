package agent

import (
	"strings"
	"testing"
)

func TestGHSource(t *testing.T) {
	for _, tt := range []struct {
		args []string
		want string
	}{
		{[]string{"api", "repos/a/b/compare/v1...v2"}, "https://api.github.com/repos/a/b/compare/v1...v2"},
		{[]string{"api", "repos/a/b/compare/v1...v2", "--jq", ".files[].filename"}, "https://api.github.com/repos/a/b/compare/v1...v2"},
		{[]string{"api", "-H", "Accept: application/vnd.github+json", "/repos/a/b/releases/tags/v2", "--jq", ".body"},
			"https://api.github.com/repos/a/b/releases/tags/v2"},
		{[]string{"api", "graphql", "-f", "query=x"}, ""},
		{[]string{"api", "repos/{owner}/{repo}/pulls"}, ""},
		{[]string{"release", "view", "v2", "-R", "a/b"}, "https://github.com/a/b/releases/tag/v2"},
		{[]string{"release", "view", "--repo=a/b", "--json", "body"}, "https://github.com/a/b/releases/latest"},
		{[]string{"release", "list", "--repo", "a/b", "-L", "5"}, "https://github.com/a/b/releases"},
		{[]string{"pr", "view", "12", "-R", "a/b", "--comments"}, "https://github.com/a/b/pull/12"},
		{[]string{"pr", "diff", "12", "-R", "a/b"}, "https://github.com/a/b/pull/12"},
		{[]string{"issue", "view", "7", "-R", "a/b"}, "https://github.com/a/b/issues/7"},
		{[]string{"repo", "view", "a/b"}, "https://github.com/a/b"},
		// Nothing to name: no repository, or a call that reads no page.
		{[]string{"release", "view", "v2"}, ""},
		{[]string{"pr", "view", "12", "-R", "a"}, ""},
		{[]string{"search", "repos", "kritika"}, ""},
		{[]string{"--version"}, ""},
	} {
		if got := ghSource(tt.args); got != tt.want {
			t.Errorf("ghSource(%q) = %q, want %q", tt.args, got, tt.want)
		}
	}
}

func TestGHUnknownCommand(t *testing.T) {
	const usage = "\n\nUsage:  gh <command> <subcommand> [flags]\n\nAvailable commands:\n  api\n  release\n"
	for _, tt := range []struct {
		name, out string
		want      []string
	}{
		{"a REST path", `unknown command "repos/a/b/compare/v1...v2" for "gh"` + usage,
			[]string{`unknown command "repos/a/b/compare/v1...v2" for gh: its first argument is one of its commands`,
				"A REST path is read with api before it: gh api repos/a/b/compare/v1...v2."}},
		{"a subcommand", `unknown command "view" for "gh"` + usage,
			[]string{"view is a subcommand: name its command first, as gh release view, gh pr view or gh issue view."}},
		{"a subcommand of one command", `unknown command "diff" for "gh"` + usage,
			[]string{"diff is a subcommand: name its command first, as gh pr diff."}},
		{"the whole call as one argument", `unknown command "repos/a/b --jq .x" for "gh"` + usage,
			[]string{"gh api repos/a/b.", "Each argument is an element of its own, not one string."}},
		{"another name", `unknown command "fetch" for "gh"` + usage, []string{"such as api, release, pr, issue or repo."}},
		{"a command's own subcommand", `unknown command "fetch" for "gh release"` + usage, nil},
		{"other output", "HTTP 404: Not Found", nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := ghUnknownCommand(tt.out)
			if tt.want == nil {
				if got != "" {
					t.Fatalf("hint = %q, want none", got)
				}
				return
			}
			for _, w := range tt.want {
				if !strings.Contains(got, w) {
					t.Errorf("hint = %q, want it to hold %q", got, w)
				}
			}
			if strings.Contains(got, "Available commands") || strings.Count(got, "\n") > 0 {
				t.Errorf("hint = %q, want one line without gh's list", got)
			}
		})
	}
}
