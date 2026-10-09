package review

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

// Fingerprint identifies a finding across reviews of the same pull request:
// the one it carries, else the path and the title, ignoring case,
// whitespace and a severity written into it, so a finding that moves by a
// few lines or is reworded in case only is recognised as the same one.
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

// inherited is the fingerprint a finding carries on from one of prior, the
// last review's findings, that no earlier finding of the review claimed:
// the one on its path whose id it names, else the one with its path and
// title, which may itself carry an earlier finding's. The one it carries on
// is added to claimed, so two findings never share a thread. "" when its
// own path and title identify it.
func inherited(f Finding, prior []Finding, claimed map[string]bool) string {
	own := titleFingerprint(f)
	claim := func(p Finding) string {
		fp := Fingerprint(p)
		claimed[fp] = true
		if fp != own {
			return fp
		}
		return ""
	}
	if f.Prior != "" {
		for _, p := range prior {
			if p.Path == f.Path && PriorID(p) == f.Prior && !claimed[Fingerprint(p)] {
				return claim(p)
			}
		}
	}
	for _, p := range prior {
		if titleFingerprint(p) == own && !claimed[Fingerprint(p)] {
			return claim(p)
		}
	}
	return ""
}

// severityPrefix is a severity, or several, a model wrote into a title,
// which the finding's own field already says and prompts prepend again.
var severityPrefix = regexp.MustCompile(`^(?:\[(?i:blocking|important|nit)\]\s*)+`)
