package worker

import (
	"context"
	"log/slog"

	"github.com/home-operations/kritika/internal/forge"
	"github.com/home-operations/kritika/internal/review"
	"github.com/home-operations/kritika/internal/tuning"
)

// stackBelow reads the pull requests under pr in its stack on the forge, for
// the prompt. A read that fails leaves the prompt without them: the review
// then judges the merge order from the base branch and the description
// alone.
func stackBelow(ctx context.Context, client forge.Client, pr *pullRequest, logger *slog.Logger) []review.StackEntry {
	ctx, cancel := context.WithTimeout(ctx, tuning.Get().Worker.StackTimeout)
	defer cancel()
	owner, repo := pr.ownerRepo()
	entries, err := client.StackBelow(ctx, owner, repo, pr.number)
	if err != nil {
		logger.Warn("stack not read", "error", err)
		return nil
	}
	var below []review.StackEntry
	for _, e := range entries {
		below = append(below, review.StackEntry{Number: e.Number, Merged: e.Merged, Open: e.Open})
	}
	return below
}
