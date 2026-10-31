package review

import (
	"cmp"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Step is how far a bump moves a version.
type Step string

// Steps, from the one that moves a version least.
const (
	// StepDigest is a version that stands while something beside it moves: a
	// digest, a build tag or a pre-release.
	StepDigest Step = "digest"
	StepPatch  Step = "patch"
	StepMinor  Step = "minor"
	StepMajor  Step = "major"
	// StepUnknown is a bump a side of which names no version: a digest under a
	// tag such as latest, or a commit alone. It can hide any step, so it rates
	// as a minor one.
	StepUnknown Step = "unknown"
)

// phrase names the step in a sentence, after "the".
func (s Step) phrase() string {
	switch s {
	case StepDigest:
		return "new digest"
	case StepUnknown:
		return "step of unknown size"
	}
	return string(s) + " step"
}

// Reach is how much depends on a dependency whose version a bump moves, as
// the judge rates it.
type Reach string

// Reaches, from the one that the least depends on.
const (
	// ReachNone is a bump that moves no dependency: an address, a port or a
	// version in prose.
	ReachNone       Reach = "none"
	ReachStandalone Reach = "standalone"
	ReachShared     Reach = "shared"
	ReachCentral    Reach = "central"
)

var reaches = []Reach{ReachNone, ReachStandalone, ReachShared, ReachCentral}

// Valid reports whether s is a reach.
func (s Reach) Valid() bool { return slices.Contains(reaches, s) }

// Step is how far b moves its version, and whether that version is under
// 1.0, where the first non-zero component moving is a minor step that rates
// at least medium. A bump of two tokens, a commit and the version comment
// beside it, moves as far as the token that names a version does.
func (b Bump) Step() (step Step, early bool) {
	step = StepUnknown
	old, now := strings.Fields(b.Old), strings.Fields(b.New)
	for i := range min(len(old), len(now)) {
		s, e := tokenStep(old[i], now[i])
		if s == StepUnknown {
			continue
		}
		switch by := slices.Index(stepsBySize, s) - slices.Index(stepsBySize, step); {
		case step == StepUnknown || by > 0:
			step, early = s, e
		case by == 0:
			early = early || e
		}
	}
	return step, early
}

// stepsBySize orders the known steps.
var stepsBySize = []Step{StepDigest, StepPatch, StepMinor, StepMajor}

// minCommitDigits is the length from which digits alone read as a commit or
// a date rather than a version: the shortest commit a forge abbreviates to,
// and under what a major version or an npm selector such as react@18 has.
const minCommitDigits = 7

// versionRE finds where a version can start in a token: digits and dots
// after the token's start, a separator or a range operator, with a v allowed
// before them. What follows the digits decides whether they are a version
// (see version).
var versionRE = regexp.MustCompile(`(?:^|[:/@^~<>=!])[vV]?(\d+(?:\.\d+)*)`)

// version is the components of the version tok names, nil when it names
// none. Digits are a version when a separator, a closing tag, a dot that
// starts an archive's suffix, or the token's end follows them, so the digits
// of a word are none. Digits alone, with no dot and no v, are a version only
// under seven of them: longer, they are a commit that happens to hold no
// letter, or a date, which a step does not measure. Digits inside an XML
// tag's name are none. Digits before the token's last slash are none either:
// an image's registry, its port and its repository path come before the tag,
// and a URL's path segment, such as /v1/, names no version of what the line
// moves. Of several, the last is the version. The components are kept as
// digits with their leading zeros dropped, since only whether two differ is
// asked of them.
func version(tok string) []string {
	var found string
	// A closing tag's slash parts no path.
	slash := strings.LastIndexByte(tok, '/')
	for slash > 0 && tok[slash-1] == '<' {
		slash = strings.LastIndexByte(tok[:slash], '/')
	}
	for _, m := range versionRE.FindAllStringSubmatchIndex(tok, -1) {
		start, end := m[2], m[3]
		if end < len(tok) && !strings.ContainsRune("-+@:/_<.", rune(tok[end])) || end <= slash {
			continue
		}
		v := start > 0 && (tok[start-1] == 'v' || tok[start-1] == 'V')
		if v {
			start--
		}
		// Inside a tag, one a later > closes, the digits are the tag's name,
		// or the closing tag's after its slash. After a < that no > closes,
		// they are a range's upper bound, such as <2.0.0.
		if lt := strings.LastIndexByte(tok[:start], '<'); lt > strings.LastIndexByte(tok[:start], '>') &&
			(start != lt+1 || strings.IndexByte(tok[end:], '>') >= 0) {
			continue
		}
		if digits := tok[m[2]:end]; !v && len(digits) >= minCommitDigits && !strings.Contains(digits, ".") {
			continue
		}
		if lower := strings.ToLower(tok[:start]); strings.HasSuffix(lower, "sha256:") || strings.HasSuffix(lower, "sha512:") {
			continue
		}
		found = tok[m[2]:end]
	}
	if found == "" {
		return nil
	}
	var out []string
	for part := range strings.SplitSeq(found, ".") {
		out = append(out, cmp.Or(strings.TrimLeft(part, "0"), "0"))
	}
	return out
}

// tokenStep is how far the edit from old to now moves the version the
// tokens name, StepUnknown when either names none.
func tokenStep(old, now string) (Step, bool) {
	a, b := version(old), version(now)
	if a == nil || b == nil {
		return StepUnknown, false
	}
	for len(a) < len(b) {
		a = append(a, "0")
	}
	for len(b) < len(a) {
		b = append(b, "0")
	}
	i := 0
	for i < len(a) && a[i] == b[i] {
		i++
	}
	switch {
	case i == len(a):
		return StepDigest, false
	case i == 0:
		return StepMajor, false
	case a[0] == "0" && b[0] == "0":
		// Under 1.0, the first non-zero component is what routine releases
		// move, and a component before it moving is such a release too.
		first := slices.IndexFunc(a, func(c string) bool { return c != "0" })
		if first < 0 || i <= first {
			return StepMinor, true
		}
		return StepPatch, false
	case i == 1:
		return StepMinor, false
	}
	return StepPatch, false
}

// stepRisk is the least risk a dependency of reach rates for a step, early
// when the version is under 1.0.
func stepRisk(step Step, early bool, reach Reach) Risk {
	if reach == ReachNone {
		return RiskLow
	}
	var r Risk
	switch step {
	case StepMajor:
		r = RiskMedium
		if reach != ReachStandalone {
			r = RiskHigh
		}
	case StepMinor, StepUnknown:
		r = map[Reach]Risk{ReachStandalone: RiskLow, ReachShared: RiskMedium, ReachCentral: RiskHigh}[reach]
		if early && r == RiskLow {
			r = RiskMedium
		}
	default:
		r = RiskLow
		if reach == ReachCentral {
			r = RiskMedium
		}
	}
	return r
}

// StepFloor is the least risk the versions bumps move rate, by their steps
// and the reaches the judge gave them, reaches[i] for bumps[i] and
// ReachStandalone where it gave none, and the bump that sets it, named with
// its step for a sentence. It is RiskLow and "" when none raises it. bumps
// are the versions the judge was shown (see ListedBumps): one it was not
// shown it rated in the risk it gave, and cannot have given a reach.
func StepFloor(bumps []Bump, reaches []Reach) (Risk, string) {
	floor, at := RiskLow, ""
	for i, b := range bumps {
		reach := ReachStandalone
		if i < len(reaches) && reaches[i].Valid() {
			reach = reaches[i]
		}
		step, early := b.Step()
		if r := stepRisk(step, early, reach); !r.Within(floor) {
			floor, at = r, fmt.Sprintf("%s at %s:%d", step.phrase(), b.Path, b.Line)
		}
	}
	return floor, at
}
