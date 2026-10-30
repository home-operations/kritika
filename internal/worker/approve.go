package worker

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/review"
	"github.com/home-operations/kritika/internal/store"
)

// approvalScope is whether review.approve's conditions hold for a pull
// request.
type approvalScope int

const (
	approvalInScope approvalScope = iota
	approvalOutOfScope
	// approvalUnjudged is conditions that could not be judged: the pull
	// request is neither approved nor its approval dismissed.
	approvalUnjudged
	// approvalClosed is a pull request closed since its review began, which
	// is neither approved nor its approval withdrawn, as one closed before.
	approvalClosed
)

// judgeApproval reads whether the pull request as it stands, from its
// stored row, is one kritika approves: open, and one review.approve's
// conditions hold for, as trigger started the review. The row is read
// whether or not there are conditions: the review may have begun on an
// open pull request that merged or closed since, and a head that merges
// no more is neither approved nor its approval withdrawn.
func judgeApproval(
	ctx context.Context, st *store.Store, accountID, prID, trigger string, r configfile.Review, logger *slog.Logger,
) approvalScope {
	var vars map[string]any
	if err := st.WithAccount(ctx, accountID, func(tx pgx.Tx) error {
		var err error
		vars, err = filterVars(ctx, tx, prID, trigger)
		return err
	}); err != nil {
		logger.Warn("approval conditions not judged: the pull request could not be read", "error", err)
		return approvalUnjudged
	}
	if open, _ := vars["open"].(bool); !open {
		return approvalClosed
	}
	if len(r.ApproveWhen) == 0 && len(r.ApproveWhenInRepo) == 0 {
		return approvalInScope
	}
	if r.ApprovesFor(vars) {
		return approvalInScope
	}
	return approvalOutOfScope
}

// approve approves the head when the review's verdict allows it (see
// approvable) and no reviewer stands as requesting changes, and otherwise
// dismisses the approval an earlier review gave, so an approval never
// outlives the verdict behind it. current says the head is still the pull
// request's: a head that moved while it was reviewed is left to its own
// review to approve. A pull request review.approve's conditions do not
// hold for is not one kritika approves: an approval it gave is dismissed,
// and otherwise the summary says nothing of approval. All of it is best
// effort, logged when it fails, and what became of it is returned for the
// summary to state.
func (p *publishPhase) approve(ctx context.Context, counts review.Counts, current bool, scope approvalScope) *review.Approval {
	defer p.timeStage("forge_approval")()
	owner, repo := p.pr.ownerRepo()
	switch scope {
	case approvalUnjudged:
		return &review.Approval{Reason: "review.approve's conditions could not be judged"}
	case approvalOutOfScope:
		return p.dismissOutOfScope(ctx)
	case approvalClosed:
		return nil
	}
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

// dismissOutOfScope dismisses the approval kritika gave a pull request
// review.approve's conditions no longer hold for, and says so; one it never
// approved gets no word on approval.
func (p *publishPhase) dismissOutOfScope(ctx context.Context) *review.Approval {
	owner, repo := p.pr.ownerRepo()
	const why = "review.approve's conditions no longer hold for this pull request"
	n, err := p.client.DismissApprovals(ctx, owner, repo, p.pr.number, fmt.Sprintf("kritika: %s at %s.", why, review.ShortSHA(p.pr.headSHA)))
	switch {
	case err != nil:
		p.logger.Warn("approval not dismissed", "error", err)
		return &review.Approval{
			Reason: "review.approve's conditions do not hold for this pull request; an earlier approval, if one stands, could not be dismissed",
		}
	case n > 0:
		p.logger.Info("approval dismissed", "reviews", n, "reason", why)
		return &review.Approval{Reason: why}
	}
	return nil
}

// approvable reports whether a review's verdict lets kritika approve the
// pull request, and what the approval rests on or why it is withheld.
// Where the repository asks for no confidence score that is the findings:
// nothing at P0 or P1. Where it asks for one, the score
// decides instead, with the risk the change was rated: the score must
// reach the threshold and the risk stay within the ceiling, and a review
// left unscored approves nothing.
func approvable(counts review.Counts, want configfile.Confidence, c *review.Confidence, unscored bool) (bool, string) {
	switch {
	case want.RepoUnscored:
		return false, "this repository's confidence.when leaves it unscored"
	case unscored:
		return false, "confidence was not scored"
	case c == nil && counts.Approvable():
		return true, "nothing at P0 or P1 found"
	case c == nil:
		return false, fmt.Sprintf("%d P0 and %d P1 finding(s)", counts.P0, counts.P1)
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
		var err error
		head, err = store.PullRequestHead(ctx, tx, p.pr.id)
		return err
	})
	if err != nil {
		p.logger.Warn("head not re-read before approving", "error", err)
	}
	return err == nil && head == p.pr.headSHA
}
