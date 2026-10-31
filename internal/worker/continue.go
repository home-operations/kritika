package worker

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/review"
	"github.com/home-operations/kritika/internal/runspec"
	"github.com/home-operations/kritika/internal/store"
)

// Carrying on a conversation saves only while the provider still caches it.
// OpenAI keeps a prompt's prefix for about 30 minutes after its last use, and
// Anthropic for 5. Each window leaves a runner the minutes it takes from the
// decision to its first step.
const (
	continueWindow          = 25 * time.Minute
	anthropicContinueWindow = 4 * time.Minute
)

// carryWindow is how recently a run must keep a conversation on ref, whose
// provider is of type t, for a review to carry it on.
func carryWindow(t configfile.ProviderType, ref configfile.ModelRef) time.Duration {
	if t == configfile.ProviderAnthropic ||
		(t == configfile.ProviderOpenRouter && strings.HasPrefix(strings.TrimPrefix(ref.Model(), "~"), "anthropic/")) {
		return anthropicContinueWindow
	}
	return continueWindow
}

// continuation is the last review's conversation that a review of head
// against mergeBase can carry on, nil for none. The pull request must be past
// the head that review saw, with its merge base where it was. So the diff
// since then shows all that changed. That review's run must keep its
// conversation, on ref, recently enough that ref's provider, of type t,
// likely still caches it. The runner starts afresh all the same when the
// prompt that it is about to send differs. A conversation that cannot be read
// is not carried on.
func (w *Review) continuation(
	ctx context.Context, logger *slog.Logger, accountID string, prior priorReview, head, mergeBase string,
	ref configfile.ModelRef, t configfile.ProviderType,
) *runspec.Continuation {
	if prior.id == "" || prior.headSHA == head || prior.mergeBase != mergeBase {
		return nil
	}
	var kept store.KeptConversation
	err := w.Store.WithAccount(ctx, accountID, func(tx pgx.Tx) error {
		var err error
		kept, err = store.ReviewConversation(ctx, tx, prior.id)
		return err
	})
	var why string
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil
	case err != nil:
		logger.Warn("the last review's conversation not read; the review starts afresh", "error", err)
		return nil
	case kept.Model != string(ref):
		why = "kept on another model"
	case kept.Age >= carryWindow(t, ref):
		why = "too old for the provider's cache"
	}
	if why != "" {
		logger.Info("the last review's conversation is not carried on", "reason", why, "age", kept.Age.Round(time.Second))
		return nil
	}
	logger.Info("the review may carry on the last review's conversation", "run", review.ShortSHA(kept.RunID),
		"age", kept.Age.Round(time.Second), "tokens", kept.Tokens)
	return &runspec.Continuation{RunID: kept.RunID, Session: kept.Session}
}
