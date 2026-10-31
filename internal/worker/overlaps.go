package worker

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/review"
	"github.com/home-operations/kritika/internal/store"
)

// overlaps are the repository's other open pull requests that change a path
// this change does. They are read now rather than when the review started,
// because a pull request can open or close in between. overlaps is none when
// the read failed, which is not worth failing the review.
func (p *publishPhase) overlaps(ctx context.Context) []review.Overlap {
	var overlaps []review.Overlap
	err := p.w.Store.WithAccount(ctx, p.account.ID(), func(tx pgx.Tx) error {
		var err error
		overlaps, err = store.OverlappingPullRequests(ctx, tx, p.runID)
		return err
	})
	if err != nil {
		p.logger.Warn("overlapping pull requests not read", "error", err)
	}
	return overlaps
}
