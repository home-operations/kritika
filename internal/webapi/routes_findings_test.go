package webapi

import (
	"errors"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/home-operations/kritika/internal/review"
	"github.com/home-operations/kritika/internal/store"
)

// TestFindingFilter: each filter reaches the store's, and a value the
// store would refuse is a bad request naming the allowed ones.
func TestFindingFilter(t *testing.T) {
	for _, tc := range []struct {
		name, query string
		want        store.FindingFilter
		wantErr     string
	}{
		{name: "none", query: "", want: store.FindingFilter{}},
		{
			name:  "every filter",
			query: "severity=p1&category=tests&status=dismissed&dismissed=conceded&rule=wrap-errors&agent=security&path=internal%2F&pr=merged&is=downvoted&q=nil",
			want: store.FindingFilter{
				Severity: review.SeverityP1, Category: review.CategoryTests, Status: store.FindingDismissed,
				DismissedBy: store.DismissedByConcession, Rule: "wrap-errors", Reviewer: "security", Path: "internal/", Downvoted: true,
				Pull: store.FindingPullMerged, Query: "nil",
			},
		},
		{name: "unknown pull request state", query: "pr=draft", wantErr: "pr must be open, merged or closed"},
		{name: "unknown severity", query: "severity=p3", wantErr: "severity must be p0, p1 or p2"},
		{name: "unknown status", query: "status=fixed", wantErr: "status must be open, addressed or dismissed"},
		{name: "unknown dismissal", query: "dismissed=ignored", wantErr: "dismissed must be command, resolved or conceded"},
		{name: "unknown is", query: "is=upvoted", wantErr: "is must be downvoted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := findingFilter(httptest.NewRequest("GET", "/findings?"+tc.query, nil))
			if tc.wantErr != "" {
				if e, ok := errors.AsType[*apiError](err); !ok || e.message != tc.wantErr {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("findingFilter = %+v, %v; want %+v", got, err, tc.want)
			}
		})
	}
}

// TestFindingsMarkdown: an export names the account and the filters, says
// when the filters keep more than it holds, and gives each finding a
// heading, where it is, what became of it and its explanation quoted, so
// a heading in an explanation cannot end it.
func TestFindingsMarkdown(t *testing.T) {
	pull := PullRef{Repository: "alpha/one", Number: 7, Title: "Add\nwidgets", URL: "https://git.example/alpha/one/pulls/7"}
	dismissed := AccountFinding{
		Path: "a.go", Line: 3, EndLine: 5, Severity: review.SeverityP0, Category: review.CategoryCorrectness, Title: "nil\nderef",
		Explanation: "x may be nil.\n\n# Not a heading\n", Rules: []string{"wrap-errors", "no-tokens"}, Agents: []string{"security"},
		ReactionsUp: 2, ReactionsDown: 1, Status: store.FindingDismissed, DismissReason: "house style",
		DismissSource: store.DismissedByCommand,
		Pull:          pull,
	}
	open := AccountFinding{
		Path: "go.mod", Severity: review.SeverityP2, Title: "old directive", Explanation: "bump it", Status: store.FindingOpen,
		Pull: pull,
	}
	for _, tc := range []struct {
		name, filter string
		items        []AccountFinding
		truncated    bool
		want         string
	}{
		{name: "none", want: "# kritika findings of github/alpha\n\n0 findings, newest first.\n"},
		{
			name: "filtered and truncated", filter: "status:dismissed is:downvoted", items: []AccountFinding{dismissed, open}, truncated: true,
			want: `# kritika findings of github/alpha

2 findings matching status:dismissed is:downvoted, newest first. The filters keep more: narrow them for the rest.

## nil deref

- p0 · correctness · dismissed
- a.go:3-5 in alpha/one#7, Add widgets: https://git.example/alpha/one/pulls/7
- Rules: wrap-errors, no-tokens
- Agents: security
- Reactions: 👍 2 · 👎 1
- Dismissed (command): house style

> x may be nil.
>
> # Not a heading

## old directive

- p2 · open
- go.mod in alpha/one#7, Add widgets: https://git.example/alpha/one/pulls/7

> bump it
`,
		},
		{
			name: "one finding", items: []AccountFinding{open},
			want: "# kritika findings of github/alpha\n\n1 finding, newest first.\n\n## old directive\n\n- p2 · open\n" +
				"- go.mod in alpha/one#7, Add widgets: https://git.example/alpha/one/pulls/7\n\n> bump it\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := findingsMarkdown("github/alpha", tc.filter, tc.items, tc.truncated); got != tc.want {
				t.Errorf("got:\n%s\nwant:\n%s", got, tc.want)
			}
		})
	}
}

// TestFilterText: an export's filters read as the search box writes them,
// the free text last, whatever order the query had.
func TestFilterText(t *testing.T) {
	q := url.Values{"q": {"nil deref"}, "is": {"downvoted"}, "repo": {"alpha/one"}, "status": {"open"}, "cursor": {"x"}}
	if got, want := filterText(q), "repo:alpha/one status:open is:downvoted nil deref"; got != want {
		t.Errorf("filterText = %q, want %q", got, want)
	}
}
