package worker

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/forge"
	"github.com/home-operations/kritika/internal/review"
	"github.com/home-operations/kritika/internal/store"
	"github.com/home-operations/kritika/internal/textcut"
)

// replyDisqualified returns why the comment is not answered as a reply, or
// "" with fingerprint set to the finding its thread holds.
func (f *followUp) replyDisqualified(ctx context.Context) string {
	fingerprint, err := f.dismissedFinding(ctx)
	if err != nil {
		f.logger.Warn("thread lookup failed", "error", err)
		return "thread unknown"
	}
	if fingerprint == "" {
		return "not a reply in one of kritika's finding threads"
	}
	resolved, found, err := f.client.ThreadResolved(ctx, f.owner, f.repo, f.pr.number, f.comment.InReplyTo)
	switch {
	case err != nil:
		f.logger.Warn("thread state lookup failed", "error", err)
		return "thread state unknown"
	case !found:
		return "thread not found"
	case resolved:
		return "thread is resolved"
	}
	f.fingerprint = fingerprint
	return ""
}

// runReply answers a reply in one of the bot's finding threads that did
// not address it, as run answers a mention, once the thread has had fewer
// than repliesPerThread of the bot's, counted again before the reply is
// posted: replies handled at once pass the first count together, and the
// second leaves the window the posts themselves take. The agent may post
// nothing, which is recorded as answered all the same, with why: the
// model was paid, so it counts against the hour and is not asked again on
// a retry. Or it may concede the finding, which dismisses it as "@<bot>
// dismiss" would; a concession the store refuses fails the job, with the
// reply up and marked answered, for a retry to find on the forge and
// record.
func (f *followUp) runReply(ctx context.Context) (_ store.FollowupStatus, err error) {
	reason, err := f.repoConfig(ctx)
	if err != nil {
		return store.FollowupFailed, err
	}
	if reason != "" {
		f.logger.Info("reply ignored", "reason", reason)
		return store.FollowupIgnored, f.record(ctx, store.FollowupIgnored, reason, 0, "")
	}
	thread, replied, err := f.thread(ctx)
	if err != nil {
		return store.FollowupFailed, err
	}
	if replied >= repliesPerThread {
		reason := fmt.Sprintf("kritika has replied %d times in this thread; mention it to go on", replied)
		f.logger.Info("reply ignored", "reason", reason)
		return store.FollowupIgnored, f.record(ctx, store.FollowupIgnored, reason, 0, "")
	}
	limited, err := f.rateLimited(ctx)
	if err != nil {
		return store.FollowupFailed, err
	}
	if limited {
		return store.FollowupLimited, nil
	}
	rec, err := f.reviewRecord(ctx)
	if err != nil {
		return store.FollowupFailed, err
	}
	end, o := f.marks().start(ctx), unanswered
	defer func() {
		if err != nil && o != answered {
			o = f.failure(err)
		}
		end(o, false)
	}()
	agent, err := f.ask(ctx, thread, rec)
	if err != nil {
		return store.FollowupFailed, err
	}
	reply, err := review.ParseReply(string(agent.Result), f.pr.repository)
	if err != nil {
		return store.FollowupFailed, finalError{err}
	}
	if cause := context.Cause(jobFence(ctx)); errors.Is(cause, errJobFenced) {
		return store.FollowupFailed, cause
	}
	pctx, cancel := detachUnlessFenced(ctx)
	defer cancel()
	if reply.Text == "" {
		f.logger.Info("reply not posted", "reason", reply.Silent, "model", agent.Model, "steps", agent.Steps, "cost_usd", agent.CostUSD)
		return store.FollowupAnswered, f.record(pctx, store.FollowupAnswered, "nothing to add: "+reply.Silent, 0, agent.Model)
	}
	// The count is read again for the window since the first; a listing
	// that fails leaves the reply, which the model was paid for, to post.
	if _, replied, err := f.thread(pctx); err != nil {
		f.logger.Warn("thread not counted again before the reply", "error", err)
	} else if replied >= repliesPerThread {
		reason := fmt.Sprintf("nothing posted: kritika replied %d times in this thread meanwhile", replied)
		f.logger.Info("reply not posted", "reason", reason, "model", agent.Model, "cost_usd", agent.CostUSD)
		return store.FollowupAnswered, f.record(pctx, store.FollowupAnswered, reason, 0, agent.Model)
	}
	body := review.FollowUpBody(reply.Text, agent.Model, string(f.settings.Models.Effort), agent.CommandErrors)
	if reply.Dismiss {
		body = review.ConcessionMarker(f.fingerprint) + "\n" + body
	}
	replyID, err := f.reply(pctx, body)
	if err != nil {
		return store.FollowupFailed, err
	}
	o = answered
	f.logger.Info("reply answered", "model", agent.Model, "reply", replyID, "dismiss", reply.Dismiss, "steps", agent.Steps,
		"commands", agent.CommandsRun, "input_tokens", agent.Usage.Prompt(), "output_tokens", agent.Usage.Output, "cost_usd", agent.CostUSD)
	if reply.Dismiss {
		if err := f.recordConcession(pctx, replyID, agent.Model, f.fingerprint, reply.Text); err != nil {
			return store.FollowupAnswered, fmt.Errorf("worker: concession not recorded: %w", err)
		}
		return store.FollowupAnswered, nil
	}
	if err := f.record(pctx, store.FollowupAnswered, "", replyID, agent.Model); err != nil {
		f.logger.Error("reply not recorded", "error", err, "reply", replyID)
	}
	return store.FollowupAnswered, nil
}

// recordConcession records the reply replyID, which conceded the finding
// fingerprint, as answered, and the finding dismissed, in one transaction,
// having resolved the thread; a finding no review of the pull request
// holds is left alone. A dismissal replaces an earlier one of the same
// finding, so a retry records it once.
func (f *followUp) recordConcession(ctx context.Context, replyID int64, modelName, fingerprint, reply string) error {
	// Best effort, like the review's own thread resolution: the record
	// and the reply stand either way.
	_, err := f.client.ResolveThread(ctx, f.owner, f.repo, f.pr.number, f.comment.InReplyTo, false)
	if err != nil && !errors.Is(err, forge.ErrUnsupported) {
		f.logger.Warn("conceded finding's thread not resolved", "comment", f.comment.InReplyTo, "error", err)
	}
	return f.w.Store.WithAccount(ctx, f.account.ID(), func(tx pgx.Tx) error {
		if err := f.recordTx(ctx, tx, store.FollowupAnswered, "", replyID, modelName); err != nil {
			return err
		}
		finding, ok, err := store.LatestFinding(ctx, tx, f.pr.id, fingerprint)
		if err != nil || !ok {
			return err
		}
		f.logger.Info("finding conceded", "fingerprint", fingerprint)
		return store.RecordDismissal(ctx, tx, store.Dismissal{
			AccountID: f.account.ID(), PullRequestID: f.pr.id, Fingerprint: fingerprint, Finding: finding,
			Reason: "kritika conceded: " + textcut.Truncate(reply, 200), Author: f.botLogin, CommentID: replyID,
			Source: store.DismissedByConcession,
		})
	})
}
