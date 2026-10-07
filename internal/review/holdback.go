package review

import "github.com/home-operations/kritika/internal/contextpack"

// HeldBackLines is how far from every line changed since the last review
// an incremental re-review's new finding may point before it is held back.
const HeldBackLines = 3

// HoldBack keeps an incremental re-review to the commits since the last
// review, which the prompt asks for and the model does not always heed. It
// reports whether a finding is held back: one the last review, whose
// findings are prior, did not make, pointing more than HeldBackLines from
// every line delta, the diff since that review, changed. reported are all
// of this review's findings. A prior finding in a file delta renames is
// taken as made under the new name. A finding is never held back in a file
// where one of prior went unreported, since it may be that finding
// reworded and Fingerprint keys on the title, nor in one of deltaPaths
// whose changed lines delta does not show: a file cut from it, binary, or
// only renamed.
func HoldBack(prior, reported []Finding, delta string, deltaPaths []string) func(Finding) bool {
	again := make(map[string]bool, len(reported))
	for _, f := range reported {
		again[Fingerprint(f)] = true
	}
	renamed := contextpack.Renames(delta)
	made := make(map[string]bool, len(prior))
	spared := map[string]bool{}
	for _, f := range prior {
		if to, ok := renamed[f.Path]; ok {
			f.Path = to
		}
		made[Fingerprint(f)] = true
		if !again[Fingerprint(f)] {
			spared[f.Path] = true
		}
	}
	touched := contextpack.TouchedLines(delta)
	for _, p := range deltaPaths {
		if _, shown := touched[p]; !shown {
			spared[p] = true
		}
	}
	return func(f Finding) bool {
		if made[Fingerprint(f)] || spared[f.Path] {
			return false
		}
		from, to := f.Line-HeldBackLines, max(f.Line, f.EndLine)+HeldBackLines
		for _, l := range touched[f.Path] {
			if l >= from && l <= to {
				return false
			}
		}
		return true
	}
}
