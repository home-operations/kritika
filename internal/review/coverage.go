package review

import (
	"slices"
	"strconv"
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
// 1.3.0.4, and not inside a digest or a word, so 2 is not in ab2c, while a
// v before it is its own. A pre-release or build suffix after it is the
// same version.
func holdsVersion(s, version string) bool {
	letter := func(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }
	digit := func(c byte) bool { return c >= '0' && c <= '9' || c == '.' }
	for at := 0; at < len(s); {
		i := strings.Index(s[at:], version)
		if i < 0 {
			return false
		}
		i += at
		end := i + len(version)
		before := i == 0 || !digit(s[i-1]) && (!letter(s[i-1]) || (s[i-1] == 'v' || s[i-1] == 'V') && (i == 1 || !letter(s[i-2])))
		after := end == len(s) || !digit(s[end]) && !letter(s[end])
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
	// A version is matched whole (see holdsVersion), so a short one is no
	// risk of an incidental match as a short name is.
	version := func(s string) {
		if s = strings.ToLower(s); s != "" && !slices.Contains(n.versions, s) {
			n.versions = append(n.versions, s)
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
			version(strings.Join(parts, "."))
			tok = tok[:start]
		} else if h := hexRun(tok); h >= 0 {
			tok = tok[:h]
		}
		name(tok)
	}
	// The key's tokens name the dependency where the line holds one
	// version. Where it holds several, the key ends with the moved
	// positions, and only the moved token names it, or the nearest token
	// before it that can name anything where the moved token is a bare
	// version, as frontend is before 1.2.3 in frontend==1.2.3 and database
	// is before "version: 2.0.0" in an inline list: the line's other
	// dependencies are not what moved. The name a name line gave follows
	// in quotes, which no token holds.
	rest := strings.TrimPrefix(b.Key, b.Path+" ")
	if strings.HasSuffix(rest, `"`) {
		if i := strings.LastIndex(rest[:len(rest)-1], ` "`); i >= 0 {
			for tok := range strings.FieldsSeq(rest[i+2 : len(rest)-1]) {
				name(tok)
			}
			rest = rest[:i]
		}
	}
	toks := strings.Fields(rest)
	if last := len(toks) - 1; last >= 0 && strings.HasPrefix(toks[last], "@") {
		for pos := range strings.SplitSeq(toks[last][1:], ",") {
			p, err := strconv.Atoi(pos)
			if err != nil || p < 1 || p > last {
				continue
			}
			// A placeholder behind a range operator alone, such as ^*, is as
			// bare as * is.
			if moved := toks[p-1]; strings.TrimLeft(moved, "^~<>=!vV") != "*" {
				name(moved)
				continue
			}
			for i := p - 2; i >= 0; i-- {
				if _, ok := eligible(toks[i]); ok && toks[i] != "*" {
					name(toks[i])
					break
				}
			}
		}
	} else {
		for _, tok := range toks {
			if tok != "*" {
				name(tok)
			}
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
