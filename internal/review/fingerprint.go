package review

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

// Fingerprint identifies a finding across reviews of the same pull request:
// the one it carries, else the path and the title, ignoring case and
// whitespace, so a finding that moves by a few lines or is reworded in
// case only is recognised as the same one.
func Fingerprint(f Finding) string {
	if f.Fingerprint != "" {
		return f.Fingerprint
	}
	return titleFingerprint(f)
}

// titleFingerprint is the fingerprint a finding's own path and title make.
func titleFingerprint(f Finding) string {
	title := strings.ToLower(strings.Join(strings.Fields(f.Title), " "))
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
// last review's findings: the one whose id it names, else the one with its
// path and title, which may itself carry an earlier finding's. "" when its
// own path and title identify it.
func inherited(f Finding, prior []Finding) string {
	own := titleFingerprint(f)
	carried := func(p Finding) string {
		if fp := Fingerprint(p); fp != own {
			return fp
		}
		return ""
	}
	if f.Prior != "" {
		for _, p := range prior {
			if PriorID(p) == f.Prior {
				return carried(p)
			}
		}
	}
	for _, p := range prior {
		if titleFingerprint(p) == own {
			return carried(p)
		}
	}
	return ""
}

// severityPrefix is a severity a model wrote into a title, which the
// finding's own field already says and prompts prepend again.
var severityPrefix = regexp.MustCompile(`^\[(?i:blocking|important|nit)\]\s*`)
