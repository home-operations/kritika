package worker

import (
	"context"
	"log/slog"
	"time"

	"github.com/home-operations/kritika/internal/forge"
	"github.com/home-operations/kritika/internal/review"
)

// stackTimeout bounds the read of a stack. The prompt does without it, and
// the score's read spends the time the confidence model has to answer in.
const stackTimeout = 10 * time.Second

// stackBelow reads the pull requests under pr in its stack on the forge, for
// the prompt. A read that fails leaves the prompt without them: the review
// then judges the merge order from the base branch and the description
// alone.
func stackBelow(ctx context.Context, client forge.Client, pr *pullRequest, logger *slog.Logger) []review.StackEntry {
	ctx, cancel := context.WithTimeout(ctx, stackTimeout)
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
