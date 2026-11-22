package linear

import (
	"slices"
	"testing"
)

func TestLinked(t *testing.T) {
	teams := []string{"ENG", "OPS"}
	tests := []struct {
		name                string
		title, branch, body string
		limit               int
		want                []string
	}{
		{name: "an ID in the title", title: "ENG-12: add widgets", limit: 3, want: []string{"ENG-12"}},
		{name: "an ID in the branch name", branch: "devin/eng-12-add-widgets", limit: 3, want: []string{"ENG-12"}},
		{name: "a magic word in the description", body: "Fixes ENG-12", limit: 3, want: []string{"ENG-12"}},
		{name: "a non-closing magic word", body: "Part of ops-7.", limit: 3, want: []string{"OPS-7"}},
		{name: "a list after a magic word", body: "Resolves ENG-1, SEC-2, and OPS-3 & ENG-4", limit: 5,
			want: []string{"ENG-1", "OPS-3", "ENG-4"}},
		{name: "an issue URL", body: "closes: https://linear.app/acme/issue/ENG-9/add-widgets", limit: 3, want: []string{"ENG-9"}},
		{name: "an issue URL whose title and workspace read as IDs",
			title: "https://linear.app/eng-4/issue/OPS-7/fix-eng-123-leak",
			body:  "Fixes https://linear.app/eng-4/issue/OPS-8/fix-eng-124-leak, ENG-5", limit: 5, want: []string{"OPS-7", "OPS-8", "ENG-5"}},
		{name: "letters after the number", title: "ENG-123abc", branch: "eng-7a", body: "Fixes ENG-12, ENG-3abc, OPS-4", limit: 5,
			want: []string{"ENG-12", "OPS-4"}},
		{name: "a skip of another ID", title: "ENG-123", body: "Ignore ENG-123abc", limit: 3, want: []string{"ENG-123"}},
		{name: "a skipped URL", title: "ENG-1", body: "ignore https://linear.app/acme/issue/ENG-1/x", limit: 3, want: nil},
		{name: "an issue URL with a query or a fragment", title: "https://linear.app/acme/issue/OPS-7?from=ENG-123",
			body: "Fixes https://linear.app/acme/issue/OPS-8#ENG-124, ENG-5", limit: 5, want: []string{"OPS-7", "OPS-8", "ENG-5"}},
		{name: "a word that runs into a URL", title: "ENG-1",
			body: "Ignorehttps://linear.app/acme/issue/ENG-1/x Fixeshttps://linear.app/acme/issue/ENG-2/x", limit: 3, want: []string{"ENG-1"}},
		{name: "a branch name with an underscore", branch: "feature/eng-9_widgets", limit: 3, want: []string{"ENG-9"}},
		{name: "a bare ID in the description", body: "See ENG-12 for why.", limit: 3, want: nil},
		{name: "a relation word", body: "Related to ENG-12", limit: 3, want: nil},
		{name: "a team the repository does not read", title: "SEC-1", body: "Fixes SEC-2", limit: 3, want: nil},
		{name: "an ID inside another word", title: "XENG-12 and UTF-8", limit: 3, want: nil},
		{name: "number zero", title: "ENG-0", limit: 3, want: nil},
		{name: "leading zeros", title: "ENG-007", limit: 3, want: []string{"ENG-7"}},
		{name: "skipped", title: "ENG-1 ENG-2", branch: "eng-2-x", body: "Fixes ENG-3, ignore ENG-1\nskip ENG-3", limit: 3,
			want: []string{"ENG-2"}},
		{name: "title, branch, then description, once each", title: "OPS-2", branch: "eng-1-ops-2", body: "Fixes ENG-1, ENG-5",
			limit: 5, want: []string{"OPS-2", "ENG-1", "ENG-5"}},
		{name: "the limit", title: "ENG-1 ENG-2", body: "Fixes ENG-3", limit: 2, want: []string{"ENG-1", "ENG-2"}},
		{name: "no room", title: "ENG-1", limit: 0, want: nil},
		{name: "a comma inside a URL", body: "Fixes https://linear.app/acme/issue/ENG-1?labels=frontend,OPS-7, ENG-2.", limit: 5,
			want: []string{"ENG-1", "ENG-2"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []string
			for _, r := range Linked(tt.title, tt.branch, tt.body, teams, tt.limit) {
				got = append(got, r.ID)
			}
			if !slices.Equal(got, tt.want) {
				t.Fatalf("Linked = %q, want %q", got, tt.want)
			}
		})
	}
	if got := Linked("ENG-1", "", "", nil, 3); got != nil {
		t.Fatalf("Linked with no teams = %q, want none", got)
	}
}

// TestLinkedWorkspaces: an issue that a URL links keeps that URL's
// workspace, and one written as an ID alone has none. A URL that links
// nothing lends its workspace to no other mention of its ID.
func TestLinkedWorkspaces(t *testing.T) {
	tests := []struct {
		name, title, body string
		want              []Ref
	}{
		{name: "an ID alone and URLs", title: "ENG-1",
			body: "Fixes https://linear.app/Acme/issue/ENG-2/x, ENG-3 and https://linear.app/other/issue/ENG-1",
			want: []Ref{{ID: "ENG-1"}, {ID: "ENG-2", Workspace: "acme"}, {ID: "ENG-3"}, {ID: "ENG-1", Workspace: "other"}}},
		{name: "a URL that links nothing", body: "See https://linear.app/acme/issue/ENG-1/context\nFixes https://linear.app/other/issue/ENG-1/work",
			want: []Ref{{ID: "ENG-1", Workspace: "other"}}},
		{name: "a skip of another workspace's URL", title: "https://linear.app/acme/issue/ENG-1/work",
			body: "Ignore https://linear.app/other/issue/ENG-1/work", want: []Ref{{ID: "ENG-1", Workspace: "acme"}}},
		{name: "a skip of an ID alone", title: "https://linear.app/acme/issue/ENG-1/work", body: "Ignore ENG-1", want: nil},
		{name: "a skip of a URL and the ID alone", title: "ENG-1", body: "Ignore https://linear.app/acme/issue/ENG-1", want: nil},
		{name: "a mark written in the text", title: "ENG-1\x000\x00", body: "Fixes https://linear.app/other/issue/ENG-2",
			want: []Ref{{ID: "ENG-1"}, {ID: "ENG-2", Workspace: "other"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Linked(tt.title, "", tt.body, []string{"ENG"}, 5); !slices.Equal(got, tt.want) {
				t.Fatalf("Linked = %+v, want %+v", got, tt.want)
			}
		})
	}
}
