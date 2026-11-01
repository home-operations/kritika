package review

import (
	"slices"
	"strings"
)

// Uncovered is the bumps of bumps that none of sources, the URLs and
// references a review read outside the repository, names. A source names a
// bump when it holds the name the bump gives its dependency; or the version
// the bump moves to, where the bump gives no name, or beside a weaker name,
// the repository or the scope the dependency is under, which alone can be
// another dependency's. A source that names the dependency without the
// version counts: what it showed is the judge's to weigh, and the fact is
// only that nothing recorded was about the version.
func Uncovered(bumps []Bump, sources []string) []Bump {
	lower := make([]string, len(sources))
	for i, s := range sources {
		lower[i] = strings.ToLower(s)
	}
	var out []Bump
	for _, b := range bumps {
		n := b.needles()
		covered := slices.ContainsFunc(lower, func(s string) bool {
			holds := func(names []string) bool {
				return slices.ContainsFunc(names, func(name string) bool { return holdsName(s, name) })
			}
			versioned := slices.ContainsFunc(n.versions, func(v string) bool { return holdsVersion(s, v) })
			return holds(n.names) || versioned && (len(n.names) == 0 || holds(n.weak))
		})
		if !covered {
			out = append(out, b)
		}
	}
	return out
}

// holdsName reports whether s holds name whole: with no letter, digit,
// underscore or hyphen on either side, so redis is not in redis_exporter
// and react is not in react-router, while a dot or a slash beside it leaves
// it whole, as a path has it, and so does a hyphen after it that a version
// follows, as a release tag such as kube-prometheus-stack-92.17.0 has it.
func holdsName(s, name string) bool {
	word := func(c byte) bool {
		return c == '_' || c == '-' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
	}
	digit := func(c byte) bool { return c >= '0' && c <= '9' }
	for at := 0; at < len(s); {
		i := strings.Index(s[at:], name)
		if i < 0 {
			return false
		}
		i += at
		end := i + len(name)
		after := end == len(s) || !word(s[end]) ||
			s[end] == '-' && end+1 < len(s) && (digit(s[end+1]) || s[end+1] == 'v' && end+2 < len(s) && digit(s[end+2]))
		if (i == 0 || !word(s[i-1])) && after {
			return true
		}
		at = i + 1
	}
	return false
}

// holdsVersion reports whether s holds version whole: not as the tail of a
// longer number, nor as the head of one, so 1.3.0 is not in v11.3.0 or in
// 1.3.0.4. A pre-release or build suffix after it is the same version.
func holdsVersion(s, version string) bool {
	for at := 0; at < len(s); {
		i := strings.Index(s[at:], version)
		if i < 0 {
			return false
		}
		i += at
		end := i + len(version)
		before := i == 0 || !strings.ContainsRune("0123456789.", rune(s[i-1]))
		after := end == len(s) || !strings.ContainsRune("0123456789.", rune(s[end]))
		if before && after {
			return true
		}
		at = i + 1
	}
	return false
}

// needles is what a source names when it is about a bump: the versions its
// moved tokens move to; the names its line and path give the dependency;
// and the weak names, the repository or the scope the dependency is under,
// which name it only beside its version, since another dependency can be
// under them too. All are lowercased.
type needles struct {
	versions, names, weak []string
}

// needles is what a source names when it is about b. A name is the last
// segment of a token's path before its version, with its scope where a
// package has one, as @babel/core is babel/core, and the path before a
// major suffix where a module has one, as go-oidc has before /v3; the
// nearest directory of the path that is no word every path has; and the
// name a name line gave the key. The repository segment after a host and an
// owner, as koanf is in github.com/knadh/koanf/providers/confmap, and a
// package's scope, as babel is, are weak names. A host, an owner or a word a
// manifest uses for any dependency is none, since a source about another
// dependency holds it too, and so is a name under three letters.
func (b Bump) needles() needles {
	var n needles
	eligible := func(s string) (string, bool) {
		s = strings.ToLower(strings.Trim(s, "\"'`,;:=()[]{}<>!^~*#/-."))
		return s, len(s) >= minNeedle && !slices.Contains(genericWords, s)
	}
	add := func(to *[]string, s string) {
		if s, ok := eligible(s); ok && !slices.Contains(*to, s) {
			*to = append(*to, s)
		}
	}
	// name adds what tok names, and the weak name it is under.
	name := func(tok string) {
		tok, _, _ = strings.Cut(tok, "*")
		tok, _, _ = strings.Cut(tok, "@sha256:")
		tok, _, _ = strings.Cut(tok, "@sha512:")
		if strings.HasPrefix(tok, "@") {
			scope, rest, _ := strings.Cut(tok[1:], "/")
			rest, _, _ = strings.Cut(rest, "@")
			add(&n.names, scope+"/"+rest)
			add(&n.weak, scope)
			return
		}
		tok = strings.TrimRight(tok, "/:@")
		segs := strings.Split(tok, "/")
		last := segs[len(segs)-1]
		last, _, _ = strings.Cut(last, ":")
		last, _, _ = strings.Cut(last, "@")
		add(&n.names, last)
		if len(segs) > 3 && strings.ContainsRune(segs[0], '.') {
			add(&n.weak, segs[2])
		}
	}
	for tok := range strings.FieldsSeq(b.New) {
		if start, parts := versionSpan(tok); start >= 0 {
			add(&n.versions, strings.Join(parts, "."))
			tok = tok[:start]
		} else if h := hexRun(tok); h >= 0 {
			tok = tok[:h]
		}
		name(tok)
	}
	for tok := range strings.FieldsSeq(strings.TrimPrefix(b.Key, b.Path)) {
		if tok != "*" {
			name(tok)
		}
	}
	dirs := strings.Split(b.Path, "/")
	for i := len(dirs) - 2; i >= 0; i-- {
		if _, ok := eligible(dirs[i]); ok {
			add(&n.names, dirs[i])
			break
		}
	}
	return n
}

// minNeedle is the shortest name a source is matched on: under it a name is
// a letter or two that any URL holds.
const minNeedle = 3

// genericWords are the words a manifest, a path or a version comment uses
// for any dependency, which name none.
var genericWords = []string{
	"tag", "image", "images", "version", "versions", "name", "chart", "charts", "ref", "uses", "newtag", "newname",
	"digest", "repository", "repo", "registry", "latest", "main", "master", "app", "apps", "base", "kubernetes",
	"overlays", "templates", "github", "workflows", "actions", "docker", "dockerfile", "src", "lib", "pkg", "cmd",
	"internal", "mod", "sum", "json", "yaml", "yml", "toml", "lock", "txt", "requirements", "package", "deps",
	"dependencies", "require", "from", "run", "pip", "install", "env", "args", "spec", "values", "helmrelease",
	"ocirepository", "kustomization", "config", "com", "org", "net", "www", "sha256", "sha512", "oci",
	"release", "releases", "stable", "default", "system", "test", "tests", "docs", "build", "dist", "with",
}
