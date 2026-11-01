package repoconfig

import (
	"bytes"
	"strings"

	"go.yaml.in/yaml/v4"

	"github.com/home-operations/kritika/internal/configfile"
)

// RuleFileDirs are the directories of the merge base whose markdown files
// are discovered as rules: the ones coding agents read path-scoped
// conventions from. A repository without them discovers none.
var RuleFileDirs = []string{".agents/rules", ".claude/rules"}

// MaxRuleFiles bounds how many rule files are read for one review.
const MaxRuleFiles = 100

// RuleFileExt is the extension that makes a file under a rule file
// directory a rule.
const RuleFileExt = ".md"

// RuleFile is a discovered rule file as it reads: the globs of its paths
// frontmatter and its text without the frontmatter. Scoped says that the
// frontmatter has paths. A scoped file whose globs are all invalid matches
// no change, so Paths is empty and Scoped set.
type RuleFile struct {
	Paths  []string
	Scoped bool
	Text   string
}

// ParseRuleFile reads a rule file as coding agents do: optional YAML
// frontmatter between lines of --- alone, of which only paths is read,
// then the rule. paths is a list of globs or one comma-separated string.
// Frontmatter that does not parse is dropped and the file applies
// everywhere, and an invalid glob matches nothing while the others still
// apply. A file whose frontmatter is never closed is all rule.
func ParseRuleFile(data []byte) RuleFile {
	text := bytes.TrimLeft(data, "\xEF\xBB\xBF \t\r\n")
	plain := RuleFile{Text: strings.TrimSpace(string(text))}
	line, rest, ok := bytes.Cut(text, []byte("\n"))
	if !ok || !frontmatterMarker(line) {
		return plain
	}
	var front []byte
	for {
		var found bool
		line, rest, found = bytes.Cut(rest, []byte("\n"))
		if frontmatterMarker(line) {
			break
		}
		if !found {
			return plain
		}
		front = append(append(front, line...), '\n')
	}
	out := RuleFile{Text: strings.TrimSpace(string(rest))}
	var meta struct {
		Paths any `yaml:"paths"`
	}
	if yaml.Unmarshal(front, &meta) != nil {
		return out
	}
	var globs []string
	switch v := meta.Paths.(type) {
	case string:
		globs = splitGlobs(v)
	case []any:
		for _, g := range v {
			if s, ok := g.(string); ok {
				globs = append(globs, s)
			}
		}
	}
	for _, g := range globs {
		g = strings.TrimPrefix(strings.TrimSpace(g), "./")
		if g == "" {
			continue
		}
		out.Scoped = true
		if configfile.ValidGlob(g) {
			out.Paths = append(out.Paths, g)
		}
	}
	return out
}

// frontmatterMarker reports whether line is --- alone, a carriage return
// allowed at its end.
func frontmatterMarker(line []byte) bool {
	return string(bytes.TrimSuffix(line, []byte("\r"))) == "---"
}

// splitGlobs splits a comma-separated list of globs at the commas that
// are the list's: one inside a brace group or a character class, or
// escaped, is the glob's own. An opener that nothing closes is no group,
// so an invalid glob takes no later glob with it.
func splitGlobs(s string) []string {
	escaped := make([]bool, len(s))
	for i := 0; i+1 < len(s); i++ {
		if s[i] == '\\' && !escaped[i] {
			escaped[i+1] = true
		}
	}
	// closer[i] is where the group that opens at s[i] closes, -1 when it
	// does not: a class at the next unescaped ], a brace group at the } of
	// its own depth, the classes inside it passed over.
	closer := make([]int, len(s))
	next := -1
	for i := len(s) - 1; i >= 0; i-- {
		closer[i] = -1
		if escaped[i] {
			continue
		}
		switch s[i] {
		case ']':
			next = i
		case '[':
			closer[i] = next
		}
	}
	var open []int
	for i := 0; i < len(s); i++ {
		if escaped[i] {
			continue
		}
		switch s[i] {
		case '[':
			if closer[i] >= 0 {
				i = closer[i]
			}
		case '{':
			open = append(open, i)
		case '}':
			if n := len(open); n > 0 {
				closer[open[n-1]], open = i, open[:n-1]
			}
		}
	}
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if escaped[i] {
			continue
		}
		switch s[i] {
		case '[', '{':
			if closer[i] >= 0 {
				i = closer[i]
			}
		case ',':
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

// RuleFileID is the id of the rule a file is, from its path below the rule
// file directory without the extension: lowercased, each run of other
// characters than letters and digits a hyphen, at most 64 characters. It is
// "" for a name that keeps no letter or digit.
func RuleFileID(rel string) string {
	rel = strings.TrimSuffix(rel, RuleFileExt)
	var b strings.Builder
	hyphen := false
	for _, r := range strings.ToLower(rel) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			if hyphen && b.Len() > 0 {
				b.WriteByte('-')
			}
			hyphen = false
			b.WriteRune(r)
			continue
		}
		hyphen = true
	}
	return strings.TrimRight(b.String()[:min(b.Len(), 64)], "-")
}
