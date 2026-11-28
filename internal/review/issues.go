package review

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/home-operations/kritika/internal/textcut"
	"github.com/home-operations/kritika/internal/tuning"
)

// Issue is an issue the pull request says the change closes or works on,
// as the prompt shows it: what the change is meant to do, in its author's
// words.
type Issue struct {
	// ID names the issue: #12 for one of the forge's, ENG-123 for one of
	// Linear's.
	ID    string `json:"id"`
	Title string `json:"title"`
	Body  string `json:"body,omitempty"`
}

// ForgeIssueID is the ID of the forge's issue numbered number.
func ForgeIssueID(number int) string { return "#" + strconv.Itoa(number) }

// closingKeyword is GitHub's set of keywords that link a pull request to
// an issue it closes, each followed by a reference to the issue: its
// number, the repository and its number, or its URL.
var closingKeyword = regexp.MustCompile(`(?i)\b(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?)\s*:?\s+` +
	`(?:https?://[^\s/]+/([\w.-]+/[\w.-]+)/issues/|([\w.-]+/[\w.-]+)#|#)(\d+)\b`)

// LinkedIssues is the numbers of the issues of repository, "owner/name", that
// body says the change closes. They are in order of first mention, and there
// are at most tuning's Review.MaxLinkedIssues of them. A reference to another
// repository's issue is left out: the review reads only the repository it
// reviews.
func LinkedIssues(body, repository string) []int {
	var out []int
	for _, m := range closingKeyword.FindAllStringSubmatch(body, -1) {
		if repo := m[1] + m[2]; repo != "" && !strings.EqualFold(repo, repository) {
			continue
		}
		n, err := strconv.Atoi(m[3])
		if err != nil || n <= 0 {
			continue
		}
		if !slices.Contains(out, n) {
			out = append(out, n)
		}
		if len(out) == tuning.Get().Review.MaxLinkedIssues {
			break
		}
	}
	return out
}

// closingIssue matches every spelling of the closing tag that a model can
// read as one.
var closingIssue = regexp.MustCompile(`(?i)<\s*/\s*issue\s*>`)

// writeIssues appends the linked issues between tags that their bodies cannot
// close, as writeDescription does the description. The issues share limit
// bytes equally.
func writeIssues(b *strings.Builder, issues []Issue, limit int) {
	if len(issues) == 0 {
		return
	}
	each := limit / len(issues)
	b.WriteString(issuesLead)
	for _, is := range issues {
		body := strings.TrimSpace(is.Body)
		if len(body) > each {
			kept := textcut.Prefix(body, each)
			body = kept + fmt.Sprintf("\n[Issue %s was cut here to fit the prompt budget: %d more bytes.]", is.ID, len(body)-len(kept))
		}
		body = closingIssue.ReplaceAllString(body, "&lt;/issue&gt;")
		title := closingIssue.ReplaceAllString(strings.Join(strings.Fields(is.Title), " "), "&lt;/issue&gt;")
		fmt.Fprintf(b, "<issue id=\"%s\" title=\"%s\">\n%s\n</issue>\n", is.ID, strings.ReplaceAll(title, `"`, "&quot;"), body)
	}
}
