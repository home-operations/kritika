package repoconfig

import (
	"bytes"
	"strings"

	"go.yaml.in/yaml/v4"

	"github.com/home-operations/kritika/internal/configfile"
)

// MaxRuleFiles bounds how many rule files are discovered for one review.
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
// frontmatter between --- lines, of which only paths is read, then the
// rule. paths is a list of globs or one comma-separated string. Frontmatter
// that does not parse is dropped and the file applies everywhere, and an
// invalid glob matches nothing while the others still apply.
func ParseRuleFile(data []byte) RuleFile {
	text := bytes.TrimLeft(data, "\ufeff \t\r\n")
	rest, ok := bytes.CutPrefix(text, []byte("---"))
	if !ok {
		return RuleFile{Text: strings.TrimSpace(string(data))}
	}
	front, body, ok := bytes.Cut(rest, []byte("\n---"))
	if !ok {
		return RuleFile{Text: strings.TrimSpace(string(data))}
	}
	// The closing marker's line ends where its newline is.
	if _, after, found := bytes.Cut(body, []byte("\n")); found {
		body = after
	} else {
		body = nil
	}
	out := RuleFile{Text: strings.TrimSpace(string(body))}
	var meta struct {
		Paths any `yaml:"paths"`
	}
	if yaml.Unmarshal(front, &meta) != nil {
		return out
	}
	var globs []string
	switch v := meta.Paths.(type) {
	case string:
		globs = strings.Split(v, ",")
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
