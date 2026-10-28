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

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/forge"
	"github.com/home-operations/kritika/internal/jobs"
	"github.com/home-operations/kritika/internal/review"
	"github.com/home-operations/kritika/internal/store"
)

// The commands a mention of the bot carries, and what each does: a review
// asked for, a finding dismissed, automatic reviews paused or resumed.

var (
	mentionPattern = regexp.MustCompile(`(?i)(^|[^\w@])@([\w-]+)`)
	// reviewAfter is a review request as it follows a mention: the word
	// review, then what the request says, up to the line's end; a mention
	// in it is a request of its own, and cut from it.
	reviewAfter    = regexp.MustCompile(`(?i)^\s+review\b(?:[ \t]+([^\r\n]*))?`)
	reviewerWord   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	dismissPattern = regexp.MustCompile(`(?i)(^|[^\w@])@([\w-]+)\s+dismiss(?:ed)?\b`)
	pausePattern   = regexp.MustCompile(`(?i)(^|[^\w@])@([\w-]+)\s+(pause|resume)\b`)
)

// reviewRequests is what each "@<slug> review" in body says after the
// word review, trimmed, in order: "" for one that says nothing. body asks
// for a review when there is one.
func reviewRequests(body, slug string) []string {
	var out []string
	for _, m := range mentionPattern.FindAllStringSubmatchIndex(body, -1) {
		if !strings.EqualFold(body[m[4]:m[5]], slug) {
			continue
		}
		if r := reviewAfter.FindStringSubmatch(body[m[1]:]); r != nil {
			text := r[1]
			// The next mention is where a request ends: "@" in a word,
			// as in an address, is the request's.
			if next := mentionPattern.FindStringIndex(text); next != nil {
				text = text[:next[0]]
			}
			out = append(out, strings.TrimSpace(text))
		}
	}
	return out
}

// requestsReview reports whether body asks slug for a review: "@<slug>
// review", the word review right after the mention.
func requestsReview(body, slug string) bool { return len(reviewRequests(body, slug)) > 0 }

// requestedReview is what follows "@<slug> review" on its line in body,
// up to the next mention, for the first request in body: word, its first
// word as written, when its lowercased, punctuation-trimmed form is
// shaped like a reviewer's name, and rest, what follows the word,
// trimmed. A request with nothing after review, anywhere in body, asks
// for every reviewer with nothing in particular to look at, whatever
// another request names.
func requestedReview(body, slug string) (word, rest string) {
	first := true
	for _, text := range reviewRequests(body, slug) {
		if text == "" {
			return "", ""
		}
		if !first {
			continue
		}
		first = false
		fields := strings.Fields(text)
		w := strings.TrimRight(strings.ToLower(fields[0]), ",.;:!?")
		if !reviewerWord.MatchString(w) {
			word, rest = "", text
			continue
		}
		word, rest = fields[0], strings.TrimSpace(strings.TrimPrefix(text, fields[0]))
	}
	return word, rest
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
			Reason: reason, Author: f.comment.Author, CommentID: f.comment.ID, Source: store.DismissedByCommand,
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
	_, err = f.client.ResolveThread(ctx, f.owner, f.repo, f.pr.number, f.comment.InReplyTo, false)
	if err != nil && !errors.Is(err, forge.ErrUnsupported) {
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
	root, err := f.client.GetComment(ctx, f.owner, f.repo, f.pr.number, f.comment.InReplyTo, true)
	if err != nil {
		return "", err
	}
	if !strings.EqualFold(root.Author, f.botLogin) {
		return "", nil
	}
	fingerprint, _ := review.MarkedFinding(root.Body)
	return fingerprint, nil
}

// findingReviewer is the reviewer other than the default one that raised
// the finding of rec, the pull request's last review, whose thread the
// comment is in: "" when it is in none, or the default reviewer raised
// it. A reply has its thread's finding already; a mention's is looked up,
// and one that cannot be is answered by the default reviewer.
func (f *followUp) findingReviewer(ctx context.Context, rec reviewRecord) string {
	fingerprint := f.fingerprint
	if fingerprint == "" {
		var err error
		if fingerprint, err = f.dismissedFinding(ctx); err != nil {
			f.logger.Warn("finding's thread not read; the default reviewer answers", "error", err)
			return ""
		}
	}
	for _, finding := range rec.findings {
		if fingerprint != "" && review.Fingerprint(finding) == fingerprint {
			return finding.Specialist()
		}
	}
	return ""
}

// answer posts body as the reply and records the mention answered, for
// reason. Both run on past the job's ctx, as run's reply does: a reply cut
// after the forge created it refunds the attempt when the worker stops,
// and the rerun, a first attempt again, would not look for it on the forge
// and post it twice. A fenced job is the exception.
func (f *followUp) answer(ctx context.Context, body, reason string) (store.FollowupStatus, error) {
	if cause := context.Cause(jobFence(ctx)); errors.Is(cause, errJobFenced) {
		return store.FollowupFailed, cause
	}
	pctx, cancel := detachUnlessFenced(ctx)
	defer cancel()
	replyID, err := f.reply(pctx, body)
	if err != nil {
		return store.FollowupFailed, err
	}
	if err := f.record(pctx, store.FollowupAnswered, reason, replyID, ""); err != nil {
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
// "@<bot> review", or of one reviewer's alone with "@<bot> review <name>":
// it is how a pull request the trigger filter keeps out, such as a fork's,
// gets one, and a merged or closed one a look back. A
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
	reviewer, requested, focus, err := f.namedReviewer(ctx)
	if err != nil {
		return store.FollowupFailed, err
	}
	queue := river.ClientFromContext[pgx.Tx](ctx)
	already := false
	err = f.w.Store.WithAccount(ctx, f.account.ID(), func(tx pgx.Tx) error {
		_, err := jobs.EnqueueReviewerRerun(ctx, tx, queue, f.account.ID(), f.pr.repositoryID, f.pr.number,
			reviewer, requested, focus)
		if errors.Is(err, jobs.ErrRerunOther) {
			return err
		}
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
	if errors.Is(err, jobs.ErrRerunOther) {
		// The review in flight does not stand in for this one, and starting
		// this one would cancel it.
		return f.answer(ctx, review.ReviewBusyBody(f.pr.headSHA), "another review of the head is in flight")
	}
	if err != nil {
		return store.FollowupFailed, fmt.Errorf("worker: queue the requested review: %w", err)
	}
	f.logger.Info("review requested", "already_queued", already, "reviewer", reviewer, "focus", focus)
	return store.FollowupAnswered, f.awaitReview(ctx)
}

// namedReviewer is the reviewer "@<bot> review <name>" asks to run alone:
// the default one, or one the repository's settings, its .kritika.yaml
// applied, name, while it is on; "" when nothing follows review, for a
// review of every reviewer that applies. requested is a word alone that
// names no reviewer that is on, which the review states it ran every
// reviewer in place of. focus is the rest of the line, what the request
// asks the review to look hardest at: the whole of it when its first
// word names no reviewer, since a word that opens a sentence names none.
func (f *followUp) namedReviewer(ctx context.Context) (reviewer, requested, focus string, err error) {
	slug := strings.TrimSuffix(f.botLogin, "[bot]")
	word, rest := requestedReview(f.comment.Body, slug)
	if word == "" && rest == "" {
		return "", "", "", nil
	}
	if _, err := f.repoConfig(ctx); err != nil {
		return "", "", "", err
	}
	reviewer, requested, focus = resolveRequest(word, rest, f.eff.Settings)
	return reviewer, requested, focus, nil
}

// resolveRequest is what a request's word and rest, as requestedReview
// splits them, ask for, settings being the repository's: the reviewer the
// word names, when it names one that is on, with the rest as the focus;
// else the word alone as requested, or the whole line as the focus when
// there is more than the word.
func resolveRequest(word, rest string, settings configfile.Settings) (reviewer, requested, focus string) {
	if word != "" {
		reviewer, requested = splitRequested(strings.TrimRight(strings.ToLower(word), ",.;:!?"), settings)
	}
	switch {
	case reviewer != "":
		focus = rest
	case rest != "":
		focus, requested = strings.TrimSpace(word+" "+rest), ""
	}
	return reviewer, requested, review.CleanFocus(focus)
}

// splitRequested is name as a review request carries it, settings being
// the repository's: the agent to run alone when it is the default pass,
// an agent that replaces it, or a specialist that is on, else the word
// requested, kept apart so the request is one of every agent, which a
// review of every agent in flight stands in for.
func splitRequested(name string, settings configfile.Settings) (reviewer, requested string) {
	if runsAlone(settings, name) {
		return name, ""
	}
	return "", name
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
