package worker

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/review"
	"github.com/home-operations/kritika/internal/store"
)

// overlaps are the repository's other open pull requests that change a
// path this change does, read now rather than when the review started,
// since one may have been opened or closed in between; none when they
// could not be read, which is not worth failing the review.
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
