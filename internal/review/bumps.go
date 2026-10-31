package review

import (
	"path"
	"regexp"
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
	var bumps []Bump
	for _, f := range udiff.Parse(diff) {
		p := f.Path()
		if p == "" || lockfile(p) {
			continue
		}
		for _, h := range f.Hunks {
			for i := 0; i < len(h.Lines); {
				removed, added, next := changeBlock(h.Lines, i)
				i = next
				if len(removed) != len(added) {
					continue
				}
				for j := range removed {
					if bump, ok := movedTokens(removed[j].Text, added[j].Text); ok {
						bump.Path, bump.Line = p, added[j].NewLine
						bumps = append(bumps, bump)
					}
				}
			}
		}
	}
	return bumps
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
