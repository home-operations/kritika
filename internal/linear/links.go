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
// or digits that run on from its number. refs then leaves out one that
// they run on from, such as ENG-123abc.
const token = `[a-z0-9]+-[0-9]+[a-z0-9]*`

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
	// or fragment. Group 1 is its ID: the rest can hold what reads as one
	// too.
	issueURL = regexp.MustCompile(`(?i)https?://linear\.app/[^\s/]+/issue/(` + token + `)(?:[/?#][^\s,]*)?`)
	// magicList matches a magic word and the list of IDs that follows it,
	// such as "Fixes ENG-123, DES-5, and ENG-256".
	magicList = regexp.MustCompile(`(?i)\b(?:` + magicWords + `)\b\s*:?\s+(` + token + `(?:` + listSep + token + `)*)`)
	// skipped matches an ID that Linear leaves unlinked: one written after
	// skip or ignore.
	skipped = regexp.MustCompile(`(?i)\b(?:skip|ignore)\s+(` + token + `)`)
)

const listSep = `\s*(?:,\s*(?:and\s+)?|&\s*|and\s+)`

// Linked is the IDs of the issues of teams that a pull request with title,
// head branch and description body links, as Linear's GitHub integration
// links them: an ID in the title or the branch name, or after a magic word
// in the description. An issue's URL stands for its ID. teams are team keys
// in upper case. An ID that the title or the description writes after skip
// or ignore is left out, as Linear leaves it unlinked. The IDs are in upper
// case, in order of first mention in the title, the branch name, then the
// description. There are at most limit of them.
func Linked(title, branch, body string, teams []string, limit int) []string {
	if len(teams) == 0 || limit <= 0 {
		return nil
	}
	// The ID takes the URL's place alone, so that a word that runs into the
	// URL, such as Ignorehttps://, runs into the ID as well.
	title = issueURL.ReplaceAllString(title, "${1}")
	body = issueURL.ReplaceAllString(body, "${1}")
	skip := map[string]bool{}
	for _, text := range []string{title, body} {
		for _, m := range skipped.FindAllStringSubmatch(text, -1) {
			for _, id := range refs(m[1]) {
				skip[id] = true
			}
		}
	}
	var out []string
	add := func(ids []string) bool {
		for _, id := range ids {
			if skip[id] || slices.Contains(out, id) || !slices.Contains(teams, teamOf(id)) {
				continue
			}
			out = append(out, id)
			if len(out) == limit {
				return true
			}
		}
		return false
	}
	if add(refs(title)) || add(refs(branch)) {
		return out
	}
	for _, m := range magicList.FindAllStringSubmatch(body, -1) {
		if add(refs(m[1])) {
			break
		}
	}
	return out
}

// refs is the IDs in text, in upper case and in order. No letter or digit
// runs into an ID on either side, so ENG-1 is read neither out of XENG-1
// nor out of ENG-1A.
func refs(text string) []string {
	var out []string
	for _, m := range bareRef.FindAllStringSubmatchIndex(text, -1) {
		if end := m[5]; end < len(text) && alnum(text[end]) {
			continue
		}
		if id, ok := issueID(text[m[2]:m[3]], text[m[4]:m[5]]); ok {
			out = append(out, id)
		}
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
