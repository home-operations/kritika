// Package linear reads the Linear issues that a pull request links, so that
// a review can judge the change against them as it does the forge's issues.
package linear

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// token matches what can be an issue ID, such as ENG-123, with the letters
// or digits that run on from its number, and the mark that Linked gives an
// ID that a URL stood for. refs then leaves out an ID that letters or
// digits run on from, such as ENG-123abc.
const token = `[a-z0-9]+-[0-9]+[a-z0-9]*(?:\x00[0-9]+\x00)?`

// magicWords are Linear's closing and non-closing magic words. A
// description that writes one before issue IDs links the pull request to
// those issues as work on them. Linear's relation words, which mark a pull
// request as only related to an issue, are left out.
const magicWords = `close[sd]?|closing|fix(?:e[sd])?|fixing|resolve[sd]?|resolving|complete[sd]?|completing|` +
	`implement(?:s|ed|ing)?|linear\s+issue|refs?|references|part\s+of|contributes\s+to|towards?`

var (
	// bareRef matches an issue ID that no letter or digit runs into from
	// the left, so ENG-1 is not read out of XENG-1. Group 1 is the team's
	// key and group 2 the number.
	bareRef = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])([a-z0-9]+)-([0-9]+)`)
	// issueURL matches a Linear issue URL,
	// https://linear.app/<workspace>/issue/ENG-123/<title>, with any query
	// or fragment, but not the punctuation that ends a sentence or a list
	// item after it. Group 1 is the workspace's URL key and group 2 the ID:
	// the rest can hold what reads as an ID too.
	issueURL = regexp.MustCompile(`(?i)https?://linear\.app/([^\s/]+)/issue/([a-z0-9]+-[0-9]+[a-z0-9]*)(?:[/?#][^\s]*[^\s,.;:!?)\]>'"])?`)
	// magicList matches a magic word and the list of IDs that follows it,
	// such as "Fixes ENG-123, DES-5, and ENG-256".
	magicList = regexp.MustCompile(`(?i)\b(?:` + magicWords + `)\b\s*:?\s+(` + token + `(?:` + listSep + token + `)*)`)
	// skipped matches an ID that Linear leaves unlinked: one written after
	// skip or ignore.
	skipped = regexp.MustCompile(`(?i)\b(?:skip|ignore)\s+(` + token + `)`)
)

const listSep = `\s*(?:,\s*(?:and\s+)?|&\s*|and\s+)`

// Ref is an issue that a pull request links.
type Ref struct {
	// ID is the issue's ID, in upper case.
	ID string
	// Workspace is the URL key of the workspace that the link names, in
	// lower case, or "" for an ID written alone. The same ID can be another
	// issue's in another workspace.
	Workspace string
}

// Linked is the issues of teams that a pull request with title, head branch
// and description body links, as Linear's GitHub integration links them: an
// ID in the title or the branch name, or after a magic word in the
// description. An issue's URL stands for its ID in its workspace. teams are
// team keys in upper case. An ID that the title or the description writes
// after skip or ignore is left out, as Linear leaves it unlinked. The issues
// are in order of first mention in the title, the branch name, then the
// description. There are at most limit of them.
func Linked(title, branch, body string, teams []string, limit int) []Ref {
	if len(teams) == 0 || limit <= 0 {
		return nil
	}
	// Each URL becomes its ID and a mark, NUL, the index of its workspace
	// in workspaces, NUL. A NUL that the text holds becomes a space, so only
	// a URL makes a mark. The ID takes the URL's place
	// alone, so that a word that runs into the URL, such as
	// Ignorehttps://, runs into the ID as well.
	var workspaces []string
	mark := func(text string) string {
		return issueURL.ReplaceAllStringFunc(strings.ReplaceAll(text, "\x00", " "), func(u string) string {
			m := issueURL.FindStringSubmatch(u)
			workspaces = append(workspaces, strings.ToLower(m[1]))
			return m[2] + "\x00" + strconv.Itoa(len(workspaces)-1) + "\x00"
		})
	}
	title, branch, body = mark(title), strings.ReplaceAll(branch, "\x00", " "), mark(body)
	var skip []Ref
	for _, text := range []string{title, body} {
		for _, m := range skipped.FindAllStringSubmatch(text, -1) {
			skip = append(skip, refs(m[1], workspaces)...)
		}
	}
	// A skip of an ID alone covers the ID in every workspace, and a link
	// alone covers it in the workspace that a skip's URL names.
	ignored := func(r Ref) bool {
		return slices.ContainsFunc(skip, func(s Ref) bool {
			return s.ID == r.ID && (s.Workspace == "" || r.Workspace == "" || s.Workspace == r.Workspace)
		})
	}
	var out []Ref
	add := func(found []Ref) bool {
		for _, r := range found {
			if ignored(r) || slices.Contains(out, r) || !slices.Contains(teams, teamOf(r.ID)) {
				continue
			}
			out = append(out, r)
			if len(out) == limit {
				return true
			}
		}
		return false
	}
	if add(refs(title, workspaces)) || add(refs(branch, workspaces)) {
		return out
	}
	for _, m := range magicList.FindAllStringSubmatch(body, -1) {
		if add(refs(m[1], workspaces)) {
			break
		}
	}
	return out
}

// refs is the issues that text names, in order, each with the workspace
// that its mark gives. No letter or digit runs into an ID on either side, so
// ENG-1 is read neither out of XENG-1 nor out of ENG-1A.
func refs(text string, workspaces []string) []Ref {
	var out []Ref
	for _, m := range bareRef.FindAllStringSubmatchIndex(text, -1) {
		end := m[5]
		if end < len(text) && alnum(text[end]) {
			continue
		}
		id, ok := issueID(text[m[2]:m[3]], text[m[4]:m[5]])
		if !ok {
			continue
		}
		r := Ref{ID: id}
		if rest, marked := strings.CutPrefix(text[end:], "\x00"); marked {
			if i, _, ok := strings.Cut(rest, "\x00"); ok {
				if n, err := strconv.Atoi(i); err == nil && n < len(workspaces) {
					r.Workspace = workspaces[n]
				}
			}
		}
		out = append(out, r)
	}
	return out
}

// alnum reports whether c is an ASCII letter or digit.
func alnum(c byte) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9'
}

// issueID is the ID of the issue numbered number of the team keyed team,
// in upper case, and whether the number is one.
func issueID(team, number string) (string, bool) {
	n, err := strconv.Atoi(number)
	if err != nil || n <= 0 {
		return "", false
	}
	return strings.ToUpper(team) + "-" + strconv.Itoa(n), true
}

// teamOf is the key of the team of issue ID id.
func teamOf(id string) string {
	team, _, _ := strings.Cut(id, "-")
	return team
}
