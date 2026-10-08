package worker

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/review"
)

// approve approves the head when the review's verdict allows it (see
// approvable) and no reviewer stands as requesting changes, and otherwise
// dismisses the approval an earlier review gave, so an approval never
// outlives the verdict behind it. current says the head is still the pull
// request's: a head that moved while it was reviewed is left to its own
// review to approve. All of it is best effort, logged when it fails, and
// what became of it is returned for the summary to state.
func (p *publishPhase) approve(ctx context.Context, counts review.Counts, current bool) *review.Approval {
	owner, repo := p.pr.ownerRepo()
	ok, why := approvable(counts, p.settings.Confidence, p.confidence, p.unscored)
	if ok && p.unfinished > 0 {
		ok, why = false, "parts of the change went unreviewed"
	}
	if ok {
		requested, err := p.client.ChangesRequested(ctx, owner, repo, p.pr.number)
		if err != nil {
			p.logger.Warn("pull request not approved: its reviews could not be read", "error", err)
			return &review.Approval{Reason: "the pull request's reviews could not be read"}
		}
		if requested {
			ok, why = false, "a reviewer has requested changes"
		}
	}
	at := fmt.Sprintf("kritika: %s at %s.", why, review.ShortSHA(p.pr.headSHA))
	switch {
	case ok && !current:
		p.logger.Info("pull request not approved: its head moved during the review")
		return &review.Approval{Reason: "the head moved during the review"}
	case ok:
		posted, err := p.client.Approve(ctx, owner, repo, p.pr.number, p.pr.headSHA, at)
		if err != nil {
			p.logger.Warn("pull request not approved", "error", err)
			return &review.Approval{Reason: "the approval could not be posted"}
		}
		if posted {
			p.logger.Info("pull request approved")
		}
		if p.confidence != nil {
			why = ""
		}
		return &review.Approval{Approved: true, Reason: why}
	default:
		n, err := p.client.DismissApprovals(ctx, owner, repo, p.pr.number, at)
		if err != nil {
			p.logger.Warn("approval not dismissed", "error", err)
			return &review.Approval{Reason: why + "; an earlier approval, if one stands, could not be dismissed"}
		}
		if n > 0 {
			p.logger.Info("approval dismissed", "reviews", n, "reason", why)
		}
		return &review.Approval{Reason: why}
	}
}

// approvable reports whether a review's verdict lets kritika approve the
// pull request, and what the approval rests on or why it is withheld.
// Where the repository asks for no confidence score that is the findings:
// nothing blocking and nothing important. Where it asks for one, the score
// decides instead, with the risk the change was rated: the score must
// reach the threshold and the risk stay within the ceiling, and a review
// left unscored approves nothing.
func approvable(counts review.Counts, want configfile.Confidence, c *review.Confidence, unscored bool) (bool, string) {
	switch {
	case unscored:
		return false, "confidence was not scored"
	case c == nil && counts.Approvable():
		return true, "nothing blocking or important found"
	case c == nil:
		return false, fmt.Sprintf("%d blocking and %d important finding(s)", counts.Blocking, counts.Important)
	case !c.Passed():
		return false, fmt.Sprintf("confidence %d/%d is below the threshold of %d", c.Score, review.MaxConfidence, c.Threshold)
	case !c.Risk.Within(want.Risk):
		return false, fmt.Sprintf("%s risk is above the %s this repository approves", c.Risk, want.Risk)
	}
	return true, fmt.Sprintf("confidence %d/%d with %s risk", c.Score, review.MaxConfidence, c.Risk)
}

// headCurrent reports whether the reviewed head is still the pull
// request's; a head that could not be read is taken as moved.
func (p *publishPhase) headCurrent(ctx context.Context) bool {
	var head string
	err := p.w.Store.WithAccount(ctx, p.account.ID(), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT head_sha FROM pull_requests WHERE id = $1`, p.pr.id).Scan(&head)
	})
	if err != nil {
		p.logger.Warn("head not re-read before approving", "error", err)
	}
	return err == nil && head == p.pr.headSHA
}
