package worker

import (
	"context"

	"golang.org/x/sync/errgroup"

	"github.com/home-operations/kritika/internal/forge"
)

// The sticky summary must exist first. Inline delivery is best effort and
// cannot change the verdict, so these calls can share its write-back deadline.
func (p *publishPhase) postInlineAndStatus(ctx context.Context, inline []forge.InlineComment, findings int) ([]int64, error) {
	owner, repo := p.pr.ownerRepo()
	state, desc := p.verdict(findings)
	var status errgroup.Group
	status.Go(func() error {
		defer p.timeStage("forge_status")()
		return p.client.SetStatus(ctx, owner, repo, p.pr.headSHA, state, "kritika: "+desc)
	})
	inlineDone := p.timeStage("forge_inline")
	ids, err := p.client.CreateReview(ctx, owner, repo, p.pr.number, p.pr.headSHA, inline)
	inlineDone()
	if err := status.Wait(); err != nil {
		p.logger.Warn("commit status not set", "error", err)
	}
	return ids, err
}
