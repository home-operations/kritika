package review

import "fmt"

// Scope is what a review covered. The reviews table's CHECK lists the same
// set.
type Scope string

// Review scopes.
const (
	ScopeFull        Scope = "full"
	ScopeIncremental Scope = "incremental"
)

// DecideScope says whether a review can build on the last completed one. It
// can only when all of these hold: there is one, the head is not the one it
// reviewed, nobody asked for the review by hand, and the runner fetched its
// head. The last condition is that the change moved since in fewer than
// maxDeltaFiles files, and in at least one. A head that carries the change as
// the last review saw it, rebased, gets a fresh look rather than a review of
// nothing. A full review says why it is one.
func DecideScope(hasPrior, sameHead, manual, priorFetched bool, deltaFiles, maxDeltaFiles int) (Scope, string) {
	switch {
	case !hasPrior:
		return ScopeFull, "no completed review to build on"
	case sameHead:
		// Nothing changed since, so an incremental review can only repeat the
		// last one. A re-run of the same head is asked for a fresh look.
		return ScopeFull, "re-run at the reviewed head"
	case manual:
		// Whoever pressed Re-run or wrote @<bot> review asked for another
		// look, not a review of what moved since the last.
		return ScopeFull, "re-run asked for at a new head"
	case !priorFetched:
		return ScopeFull, "prior head unreachable"
	case deltaFiles >= maxDeltaFiles:
		return ScopeFull, fmt.Sprintf("%d files changed since last review", deltaFiles)
	case deltaFiles == 0:
		return ScopeFull, "the change is as the last review saw it"
	}
	return ScopeIncremental, ""
}
