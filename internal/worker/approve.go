package worker

import (
	"context"
	"fmt"
	"log/slog"
	"path"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/repoconfig"
	"github.com/home-operations/kritika/internal/review"
	"github.com/home-operations/kritika/internal/store"
	"github.com/home-operations/kritika/internal/udiff"
)

// approvalScope is whether review.approve's conditions hold for a pull
// request.
type approvalScope int

const (
	approvalInScope approvalScope = iota
	approvalOutOfScope
	// approvalUnjudged is conditions that kritika failed to judge: the pull
	// request is neither approved nor its approval dismissed.
	approvalUnjudged
	// approvalClosed is a pull request closed since its review began, which
	// is neither approved nor its approval withdrawn, as one closed before.
	approvalClosed
)

// judgeApproval reads whether the pull request as it stands, from its stored
// row, is one that kritika approves. That means that it is open and that
// review.approve's conditions hold for it, given that trigger started the
// review. judgeApproval reads the row whether or not there are conditions.
// The review can begin on an open pull request that then merges or closes. A
// head that merges no more is neither approved nor its approval withdrawn.
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
// approvable) and no reviewer stands as requesting changes. Otherwise it
// dismisses the approval that an earlier review gave, so an approval never
// outlives the verdict behind it. current says the head is still the pull
// request's: a head that moved while it was reviewed is left to its own
// review to approve. A pull request that review.approve's conditions do not
// hold for is not one that kritika approves. An approval that it gave is
// dismissed, and otherwise the summary says nothing of approval. All of it is
// best effort, logged when it fails, and what became of it is returned for
// the summary to state.
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
	if ok && len(p.ownChanged) > 0 {
		ok, why = false, ownReason(p.ownChanged)
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

// dismissOutOfScope dismisses the approval that kritika gave a pull request
// that review.approve's conditions no longer hold for, and says so. A pull
// request that it never approved gets no word on approval.
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

// ownPaths are the files and directories whose content steers kritika's
// reviews of the repository: .kritika.yaml, the rule file and skill
// directories, and the files that the configuration names. Once a change to
// one merges, it steers every later review.
func ownPaths(s configfile.Settings) []string {
	paths := append([]string{repoconfig.FileName}, repoconfig.RuleFileDirs...)
	paths = append(paths, s.Skills.Paths...)
	paths = append(paths, s.Review.Referenced()...)
	paths = append(paths, configfile.ReviewerFiles(s.Reviewers)...)
	for _, c := range s.Review.Context {
		paths = append(paths, c.Path)
	}
	return paths
}

// changedOwn is the paths of changed that are of ownPaths, or under one of
// them. changed names a rename by both of its names.
func changedOwn(s configfile.Settings, changed []string) []string {
	own := ownPaths(s)
	for i, o := range own {
		own[i] = path.Clean(o)
	}
	var out []string
	for _, p := range changed {
		if p == "" || slices.Contains(out, p) {
			continue
		}
		if slices.ContainsFunc(own, func(o string) bool { return p == o || strings.HasPrefix(p, o+"/") }) {
			out = append(out, p)
		}
	}
	return out
}

// diffPaths are the paths that a unified diff changes, a rename by both of
// its names.
func diffPaths(diff string) []string {
	files := udiff.Parse(diff)
	out := make([]string, 0, 2*len(files))
	for _, f := range files {
		out = append(out, f.Path(), f.RenamedFrom())
	}
	return out
}

// ownReason is why a change to the paths of changedOwn is not approved,
// with the first few of them.
func ownReason(changed []string) string {
	const named = 3
	list := strings.Join(changed[:min(len(changed), named)], ", ")
	if len(changed) > named {
		list += fmt.Sprintf(" and %d more", len(changed)-named)
	}
	return "it changes files that configure kritika (" + list + ")"
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

// headCurrent reports whether the reviewed head is still the pull request's.
// A head that it failed to read is taken as moved.
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
