package review

import (
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/home-operations/kritika/internal/udiff"
)

// Bump is a version the change moves: a line of the diff rewritten with
// nothing but a version-like token or two changed, as a dependency update
// writes it, whatever the file. Line is the head-side line.
type Bump struct {
	Path     string
	Line     int
	Old, New string
	// Key names the dependency that the line moves, the same across its
	// bumps: the path and the line's tokens, each cut where its version,
	// digest or commit starts (see bumpKey), then, in quotes no token can
	// hold, the name that a name line just above gives it where there is
	// one (see nameAbove).
	// So whichever of them moves leaves it as it was, and what the line
	// names stays. Two lines of one file that name nothing but a version,
	// with no name line in the diff's context above them, share a key.
	Key string
	// positions identifies the changed tokens by their one-based offsets.
	// Distinct tokens on one line can have the same version. Zero is unused.
	positions [maxBumpTokens]int
}

// maxBumpTokens is how many tokens of one line can move for the line to be a
// bump: a tag and the digest pinned beside it, or a pinned action's commit
// and the version comment after it.
const maxBumpTokens = 2

// bumpToken is what a version-like token is made of: a version, a tag, a
// digest, a module path with its version, or a range constraint.
var bumpToken = regexp.MustCompile(`^[A-Za-z0-9._+@:/~^<>!-]+$`)

// Bumps finds the versions diff moves: in every file but a lockfile, each
// removed line replaced by an added one that differs from it in one or two
// version-like tokens alone. A lockfile repeats its manifest with hashes
// beside the versions, so it is left out.
func Bumps(diff string) []Bump {
	bumps, _ := bumpsAndRest(diff)
	return bumps
}

// OneBump reports whether diff moves one version and nothing else: it has
// one bump, of one token, and every other change it makes is a lockfile's.
// The risk such a change is rated is that dependency's own. A bump of two
// tokens can move two dependencies on one line, so it is none.
func OneBump(diff string) bool {
	bumps, rest := bumpsAndRest(diff)
	return len(bumps) == 1 && bumps[0].positions[1] == 0 && rest == 0
}

// bumpsAndRest is the bumps of diff and how many changes it makes outside a
// lockfile that are no bump's: a line, or a file changed in no line, such
// as a binary, a mode change or a rename alone.
func bumpsAndRest(diff string) (bumps []Bump, rest int) {
	for _, f := range udiff.Parse(diff) {
		p := f.Path()
		if p == "" || lockfile(p) {
			continue
		}
		if len(f.Hunks) == 0 || nonText(f.Header) {
			rest++
		}
		for _, h := range f.Hunks {
			for i := 0; i < len(h.Lines); {
				at := i
				removed, added, next := changeBlock(h.Lines, i)
				i = next
				if len(removed) != len(added) {
					rest += len(removed) + len(added)
					continue
				}
				for j := range removed {
					bump, ok := movedTokens(removed[j].Text, added[j].Text)
					if !ok {
						rest += 2
						continue
					}
					bump.Path, bump.Line = p, added[j].NewLine
					bump.Key = p + " " + bumpKey(added[j].Text, bump.positions)
					if name := nameAbove(h.Lines, at); name != "" {
						bump.Key += ` "` + name + `"`
					}
					bumps = append(bumps, bump)
				}
			}
		}
	}
	return bumps, rest
}

// nonText reports whether a file's header, the lines before its first hunk,
// records a change its hunks do not show: a mode change, a rename or a
// copy, or a binary.
func nonText(header string) bool {
	for line := range strings.SplitSeq(header, "\n") {
		for _, prefix := range []string{"old mode ", "new mode ", "rename ", "copy ", "similarity index", "Binary files ", "GIT binary patch"} {
			if strings.HasPrefix(line, prefix) {
				return true
			}
		}
	}
	return false
}

// changeBlock is the run of removed lines that starts at i, the run of added
// lines that follows it, and the index after both. A line of another kind at
// i yields empty runs and i+1. changeBlock passes over a "No newline at end
// of file" note between the runs, since the note parts a last line from its
// replacement.
func changeBlock(lines []udiff.Line, i int) (removed, added []udiff.Line, next int) {
	for i < len(lines) && lines[i].Kind == '-' {
		removed = append(removed, lines[i])
		i++
	}
	if len(removed) > 0 && i < len(lines) && lines[i].Kind == '\\' {
		i++
	}
	for i < len(lines) && lines[i].Kind == '+' {
		added = append(added, lines[i])
		i++
	}
	if len(removed) == 0 && len(added) == 0 {
		i++
	}
	return removed, added, i
}

// movedTokens is the version-like tokens that an edit from old to now moved,
// each side's joined by spaces. It applies when the two lines split into the
// same tokens except for at most maxBumpTokens of them. Every token that
// differs must also be version-like on both sides.
func movedTokens(old, now string) (bump Bump, ok bool) {
	a, b := bumpTokens(old), bumpTokens(now)
	if len(a) != len(b) {
		return Bump{}, false
	}
	var was, is []string
	for i := range a {
		if a[i] == b[i] {
			continue
		}
		if !versionLike(a[i]) || !versionLike(b[i]) || len(was) == maxBumpTokens {
			return Bump{}, false
		}
		bump.positions[len(was)] = i + 1
		was, is = append(was, a[i]), append(is, b[i])
	}
	if len(was) == 0 {
		return Bump{}, false
	}
	bump.Old, bump.New = strings.Join(was, " "), strings.Join(is, " ")
	return bump, true
}

// nameAbove is the name that the nearest name line in the diff's context
// above the change block at at gives the lines of the block, "" when none
// is within the context a diff shows: a chart's dependency and a kustomize
// image have their version on a line of their own under a "name:" line.
func nameAbove(lines []udiff.Line, at int) string {
	for k := at - 1; k >= 0 && k >= at-contextLines; k-- {
		if lines[k].Kind != ' ' {
			break
		}
		text := strings.TrimPrefix(strings.TrimSpace(lines[k].Text), "- ")
		if name, ok := strings.CutPrefix(text, "name:"); ok {
			return strings.Join(bumpTokens(name), " ")
		}
	}
	return ""
}

// contextLines is how many unchanged lines a diff shows before a change,
// as git shows them.
const contextLines = 3

// hexRunRE finds a commit or a digest in a token: a run of hex digits long
// enough to be an abbreviated commit, at the token's start or after the @
// or the colon that parts a reference from what it pins, and at its end or
// before an @. So a registry's numeric account before a dot is none. One
// with no digit among them is a word of a to f, unless it is as long as a
// whole commit (see hexRun).
var hexRunRE = regexp.MustCompile(`(?:^|[@:])([0-9a-fA-F]{7,})(?:$|@)`)

// minPlainHex is the length from which a run of hex letters alone is a
// commit or a digest: a whole SHA-1.
const minPlainHex = 40

// hexRun is where the first commit or digest in tok starts, -1 for none.
func hexRun(tok string) int {
	for _, h := range hexRunRE.FindAllStringSubmatchIndex(tok, -1) {
		if run := tok[h[2]:h[3]]; strings.ContainsAny(run, "0123456789") || len(run) >= minPlainHex {
			return h[2]
		}
	}
	return -1
}

// bumpKey is line's tokens joined by a space, each cut where its version
// starts, or its commit or digest where that comes first, with a star in
// their place. Every token is cut, not only the ones at positions, which
// moved: a pinned action's version comment stays when its commit alone
// moves, and the key must not depend on which of the two did. What a token
// names before its version stays, so an image keeps its name and a port,
// and two names that differ by a digit stay apart. A line that holds more
// than one version of its own, such as two packages one install command
// names, has the moved positions in its key, so that each package keeps a
// history of its own. A version comment after a # is the version of the
// token before it, not one of its own.
func bumpKey(line string, positions [maxBumpTokens]int) string {
	toks := bumpTokens(line)
	versions := 0
	for i, tok := range toks {
		start, _ := versionSpan(tok)
		if h := hexRun(tok); h >= 0 && (start < 0 || h < start) {
			start = h
		}
		if start < 0 {
			continue
		}
		toks[i] = tok[:start] + "*"
		if i == 0 || toks[i-1] != "#" {
			versions++
		}
	}
	key := strings.Join(toks, " ")
	if versions > 1 {
		var at []string
		for _, pos := range positions {
			if pos > 0 {
				at = append(at, strconv.Itoa(pos))
			}
		}
		key += " @" + strings.Join(at, ",")
	}
	return key
}

// bumpTokens splits a line on whitespace, a carriage return included, and
// the punctuation manifests put around a version: quotes, equals, commas,
// brackets, and the semicolon that parts a requirement from its marker.
func bumpTokens(line string) []string {
	return strings.FieldsFunc(line, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\r' || strings.ContainsRune(`"'=,;()[]{}`, r)
	})
}

// versionLike reports whether tok reads as a version: a digit in it, a
// separator a version has, and nothing a version has not. An address reads
// as one too, and a four-part version as an address, so the prompt, not
// the shape, tells them apart.
func versionLike(tok string) bool {
	return len(tok) <= 200 && strings.ContainsAny(tok, "0123456789") && strings.ContainsAny(tok, ".@:/-") && bumpToken.MatchString(tok)
}

// lockfile reports whether p is a lockfile, by the names the package
// managers give them.
func lockfile(p string) bool {
	base := path.Base(p)
	return strings.HasSuffix(base, ".lock") || strings.HasSuffix(base, ".sum") ||
		base == "package-lock.json" || base == "pnpm-lock.yaml" || base == ".terraform.lock.hcl"
}
