package worker

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/home-operations/kritika/internal/forge"
	"github.com/home-operations/kritika/internal/jobs"
	"github.com/home-operations/kritika/internal/review"
	"github.com/home-operations/kritika/internal/store"
)

// The commands a mention of the bot carries, and what each does: a review
// asked for, a finding dismissed, automatic reviews paused or resumed.

var (
	mentionPattern = regexp.MustCompile(`(?i)(^|[^\w@])@([\w-]+)`)
	reviewPattern  = regexp.MustCompile(`(?i)(^|[^\w@])@([\w-]+)\s+review\b`)
	dismissPattern = regexp.MustCompile(`(?i)(^|[^\w@])@([\w-]+)\s+dismiss(?:ed)?\b`)
	pausePattern   = regexp.MustCompile(`(?i)(^|[^\w@])@([\w-]+)\s+(pause|resume)\b`)
)

// requestsReview reports whether body asks slug for a review: "@<slug>
// review", the word review right after the mention.
func requestsReview(body, slug string) bool {
	for _, m := range reviewPattern.FindAllStringSubmatch(body, -1) {
		if strings.EqualFold(m[2], slug) {
			return true
		}
	}
	return false
}

// requestsDismiss reports whether body asks slug to dismiss the finding it
// replies to, "@<slug> dismiss", and returns the reason: the rest of the
// comment.
func requestsDismiss(body, slug string) (string, bool) {
	for _, m := range dismissPattern.FindAllStringSubmatchIndex(body, -1) {
		if strings.EqualFold(body[m[4]:m[5]], slug) {
			return strings.TrimSpace(strings.TrimLeft(body[m[1]:], " \t\r\n,:;.-")), true
		}
	}
	return "", false
}

// dismiss records the finding the comment replies to as dismissed, with
// reason, resolves its thread and says so: later reviews of the pull
// request are told not to raise it again. Only a reply in one of the bot's
// finding threads names a finding; anywhere else, the reply says how to
// use it. It counts against the hourly follow-up limit.
func (f *followUp) dismiss(ctx context.Context, reason string) (store.FollowupStatus, error) {
	limited, err := f.rateLimited(ctx)
	if err != nil {
		return store.FollowupFailed, err
	}
	if limited {
		return store.FollowupLimited, nil
	}
	fingerprint, err := f.dismissedFinding(ctx)
	if err != nil {
		return store.FollowupFailed, err
	}
	if fingerprint == "" {
		return f.answer(ctx, review.DismissHintBody, "dismiss outside a finding thread")
	}
	var found bool
	err = f.w.Store.WithAccount(ctx, f.account.ID(), func(tx pgx.Tx) error {
		finding, ok, err := store.LatestFinding(ctx, tx, f.pr.id, fingerprint)
		if err != nil || !ok {
			return err
		}
		found = true
		return store.RecordDismissal(ctx, tx, store.Dismissal{
			AccountID: f.account.ID(), PullRequestID: f.pr.id, Fingerprint: fingerprint, Finding: finding,
			Reason: reason, Author: f.comment.Author, CommentID: f.comment.ID,
		})
	})
	if err != nil {
		return store.FollowupFailed, err
	}
	if !found {
		return f.answer(ctx, review.DismissUnknownBody, "finding unknown")
	}
	// Best effort, like the review's own thread resolution: the record and
	// the reply stand either way.
	if _, err := f.client.ResolveThread(ctx, f.owner, f.repo, f.pr.number, f.comment.InReplyTo, false); err != nil {
		f.logger.Warn("dismissed finding's thread not resolved", "comment", f.comment.InReplyTo, "error", err)
	}
	f.logger.Info("finding dismissed", "fingerprint", fingerprint, "reason", reason)
	return f.answer(ctx, review.DismissedBody, "finding dismissed")
}

// dismissedFinding is the fingerprint of the finding the comment replies
// to: the one the bot's root comment of its thread carries a FindingMarker
// for. "" when the comment is not such a reply.
func (f *followUp) dismissedFinding(ctx context.Context) (string, error) {
	if !f.comment.Inline || f.comment.InReplyTo == 0 {
		return "", nil
	}
	root, err := f.client.GetComment(ctx, f.owner, f.repo, f.comment.InReplyTo, true)
	if err != nil {
		return "", err
	}
	if !strings.EqualFold(root.Author, f.botLogin) {
		return "", nil
	}
	fingerprint, _ := review.MarkedFinding(root.Body)
	return fingerprint, nil
}

// answer posts body as the reply and records the mention answered, for
// reason.
func (f *followUp) answer(ctx context.Context, body, reason string) (store.FollowupStatus, error) {
	replyID, err := f.reply(ctx, body)
	if err != nil {
		return store.FollowupFailed, err
	}
	if err := f.record(ctx, store.FollowupAnswered, reason, replyID, ""); err != nil {
		f.logger.Error("follow-up not recorded", "error", err, "reply", replyID)
	}
	return store.FollowupAnswered, nil
}

// requestsPause reports whether body asks slug to pause or resume the pull
// request's automatic reviews, "@<slug> pause" or "@<slug> resume", and
// which.
func requestsPause(body, slug string) (paused, ok bool) {
	for _, m := range pausePattern.FindAllStringSubmatch(body, -1) {
		if strings.EqualFold(m[2], slug) {
			return strings.EqualFold(m[3], "pause"), true
		}
	}
	return false, false
}

// pause pauses or resumes the pull request's automatic reviews, as someone
// with write access asked, and says so. It counts against the hourly
// follow-up limit.
func (f *followUp) pause(ctx context.Context, slug string, paused bool) (store.FollowupStatus, error) {
	limited, err := f.rateLimited(ctx)
	if err != nil {
		return store.FollowupFailed, err
	}
	if limited {
		return store.FollowupLimited, nil
	}
	err = f.w.Store.WithAccount(ctx, f.account.ID(), func(tx pgx.Tx) error {
		return store.PausePullRequest(ctx, tx, f.pr.id, paused)
	})
	if err != nil {
		return store.FollowupFailed, err
	}
	if paused {
		f.logger.Info("automatic reviews paused")
		return f.answer(ctx, review.PausedBody(slug), "reviews paused")
	}
	f.logger.Info("automatic reviews resumed")
	return f.answer(ctx, review.ResumedBody, "reviews resumed")
}

// reviewRequested is the reason a review request is recorded answered
// with: its job then waits for the review (awaitingReview).
const reviewRequested = "review requested"

// requestReview queues a review of the pull request's head, as the
// dashboard's re-run does, for someone with write access who asked with
// "@<bot> review": it is how a pull request from a fork, which is not
// reviewed on its own, gets one, and a merged or closed one a look back. A
// review of the head already queued or running stands in for it. The
// request is answered with the bot's marks rather than a reply: its eyes
// now, and its job then waits to end them as the review ends (awaitReview).
// Like every mark, the eyes are best effort: a forge that refuses them
// costs the request its marks and nothing else. It counts against the
// hourly follow-up limit.
func (f *followUp) requestReview(ctx context.Context) (store.FollowupStatus, error) {
	limited, err := f.rateLimited(ctx)
	if err != nil {
		return store.FollowupFailed, err
	}
	if limited {
		return store.FollowupLimited, nil
	}
	if f.pr.closed != "" {
		base, err := reviewBase(ctx, f.w.Store, f.client, f.account.ID(), f.pr)
		if err != nil {
			return store.FollowupFailed, err
		}
		if base == "" {
			return f.answer(ctx, review.NothingToReviewBody(f.pr.headSHA, f.pr.baseRef), nothingToReview(f.pr))
		}
	}
	queue := river.ClientFromContext[pgx.Tx](ctx)
	already := false
	err = f.w.Store.WithAccount(ctx, f.account.ID(), func(tx pgx.Tx) error {
		_, err := jobs.EnqueueRerun(ctx, tx, queue, f.account.ID(), f.pr.repositoryID, f.pr.number)
		if errors.Is(err, jobs.ErrRerunQueued) {
			already, err = true, nil
		}
		if err != nil {
			return err
		}
		return f.recordTx(ctx, tx, store.FollowupAnswered, reviewRequested, 0, "")
	})
	if errors.Is(err, jobs.ErrNoHead) {
		return store.FollowupIgnored, f.record(ctx, store.FollowupIgnored, "the pull request has no head to review", 0, "")
	}
	if err != nil {
		return store.FollowupFailed, fmt.Errorf("worker: queue the requested review: %w", err)
	}
	f.logger.Info("review requested", "already_queued", already)
	return store.FollowupAnswered, f.awaitReview(ctx)
}

// awaitInterval is how long the job of an answered review request sleeps
// between looks at whether the review has ended.
const awaitInterval = 15 * time.Second

// awaitingReview reports whether the comment is a review request answered
// with the bot's eyes, whose job waits for the review to end them.
func (f *followUp) awaitingReview(ctx context.Context) (bool, error) {
	var state store.FollowupState
	err := f.w.Store.WithAccount(ctx, f.account.ID(), func(tx pgx.Tx) error {
		var err error
		state, err = store.FindFollowup(ctx, tx, f.pr.id, f.comment.ID, f.comment.Inline)
		return err
	})
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("worker: read follow-up: %w", err)
	}
	return state.Status == store.FollowupAnswered && state.Reason == reviewRequested && state.ReplyCommentID == nil, nil
}

// awaitReview ends the marks on an answered review request once no review
// job of the pull request is left, as requestOutcome says of the review
// that ended last since the request, and snoozes the job until then. Only
// River's jobs count as left: a review row whose job died running holds
// no wait.
func (f *followUp) awaitReview(ctx context.Context) error {
	if _, err := f.client.React(ctx, f.owner, f.repo, f.comment, forge.ReactionEyes); err != nil {
		f.logger.Info("not marked as being worked on", "error", err)
	}
	var queued bool
	var status store.ReviewStatus
	err := f.w.Store.WithAccount(ctx, f.account.ID(), func(tx pgx.Tx) error {
		var err error
		if queued, err = jobs.PullQueued(ctx, tx, f.account.ID(), f.pr.repositoryID, f.pr.number); err != nil || queued {
			return err
		}
		status, err = store.ReviewStatusSince(ctx, tx, f.pr.id, f.requested)
		return err
	})
	if err != nil {
		return fmt.Errorf("worker: read the requested review: %w", err)
	}
	if queued {
		return river.JobSnooze(cmp.Or(f.w.awaitEvery, awaitInterval))
	}
	f.logger.Info("requested review ended", "status", status)
	f.marks().start(ctx)(requestOutcome(status), false)
	return f.record(ctx, store.FollowupAnswered, "review request settled: "+string(cmp.Or(status, store.ReviewFailed)), 0, "")
}

// requestOutcome is what the marks on a review request say of status, that
// of the review that ended last since the request, "" for none: answered
// once one was posted, failed when it failed or none ended, as when the
// review's job gave up before recording one, and unanswered otherwise, as
// when it was skipped, or superseded by a head no review followed.
func requestOutcome(status store.ReviewStatus) outcome {
	switch status {
	case store.ReviewCompleted:
		return answered
	case store.ReviewFailed, "":
		return failed
	}
	return unanswered
}

// mentioned reports whether body @-mentions slug as a whole word.
func mentioned(body, slug string) bool {
	for _, m := range mentionPattern.FindAllStringSubmatch(body, -1) {
		if strings.EqualFold(m[2], slug) {
			return true
		}
	}
	return false
}
