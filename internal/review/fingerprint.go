package review

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"slices"
	"strings"
)

// Fingerprint identifies a finding across reviews of the same pull request:
// the one it carries, else the path and the title. It ignores case,
// whitespace, and a severity written into the title. So a finding that moves
// by a few lines or is reworded in case only is recognized as the same one.
func Fingerprint(f Finding) string {
	if f.Fingerprint != "" {
		return f.Fingerprint
	}
	return titleFingerprint(f)
}

// titleFingerprint is the fingerprint a finding's own path and title make.
func titleFingerprint(f Finding) string {
	title := strings.ToLower(strings.Join(strings.Fields(severityPrefix.ReplaceAllString(f.Title, "")), " "))
	sum := sha256.Sum256([]byte(f.Path + "\x00" + title))
	return hex.EncodeToString(sum[:])
}

// priorIDLen is how much of a fingerprint names a prior finding to the
// model: enough to tell a pull request's findings apart, short enough to
// copy.
const priorIDLen = 8

// PriorID is the id a prompt lists a prior finding under, which a finding
// that reports it again names.
func PriorID(f Finding) string {
	fp := Fingerprint(f)
	return fp[:min(len(fp), priorIDLen)]
}

// priorIndex is the last review's findings as a finding of this review names
// one: by its path and the id that the prompt listed it under, and by the
// fingerprint that its path and title make. It also holds the ones that a
// finding of this review carried on, so that two findings never share a
// thread.
type priorIndex struct {
	byID, byTitle map[string][]Finding
	claimed       map[string]bool
}

func indexPrior(prior []Finding) *priorIndex {
	x := &priorIndex{byID: map[string][]Finding{}, byTitle: map[string][]Finding{}, claimed: map[string]bool{}}
	for _, p := range prior {
		id, title := p.Path+"\x00"+PriorID(p), titleFingerprint(p)
		x.byID[id] = append(x.byID[id], p)
		x.byTitle[title] = append(x.byTitle[title], p)
	}
	return x
}

// inherit is the fingerprint that f carries on from a prior finding that no
// earlier finding of the review claimed. That prior finding is the one on its
// path whose id it names, else the one with its path and title. That one can
// itself carry an earlier finding's fingerprint. The one that it carries on
// is claimed, and returned even when its own path and title make it. So a
// finding of another reviewer that it is merged into under another title
// keeps its thread (see MergeReviewers). It is "" when it carries on none.
func (x *priorIndex) inherit(f Finding) string {
	own := titleFingerprint(f)
	candidates := x.byTitle[own]
	if f.Prior != "" {
		candidates = slices.Concat(x.byID[f.Path+"\x00"+f.Prior], candidates)
	}
	for _, p := range candidates {
		fp := Fingerprint(p)
		if x.claimed[fp] {
			continue
		}
		x.claimed[fp] = true
		return fp
	}
	return ""
}

// severityPrefix is a severity, or several, a model wrote into a title,
// which the finding's own field already says and prompts prepend again.
var severityPrefix = regexp.MustCompile(`^(?:\[(?i:p[0-2])\]\s*)+`)
