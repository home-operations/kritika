package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/forge"
	"github.com/home-operations/kritika/internal/jobs"
	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/review"
	"github.com/home-operations/kritika/internal/store"
)

// publicationAnswer is the finalized model output passed to delivery. Delivery
// can use it without another merge or confidence call.
type publicationAnswer struct {
	Result     review.Result
	Unanchored []review.Finding
	Notes      []string
	Model      string
}

// publicationSnapshot freezes the inputs to delivery, including repository
// templates and policy. Credentials and model clients are resolved separately.
type publicationSnapshot struct {
	Version        int
	Answer         publicationAnswer
	Review         configfile.Review
	ConfidenceRule configfile.Confidence
	Effort         model.Effort
	MaxAutoReviews int
	Templates      review.Templates
	Scope          review.Scope
	Trigger        string
	Closed         string
	RepoNotes      []string
	Sources        []string
	PriorID        string
	PriorHead      string
	Prior          []publicationFinding
	Dismissed      []store.Dismissal
	Resolved       []publicationFinding
	HeldBack       []review.Finding
	Confidence     *review.Confidence
	Unscored       bool
	Unfinished     int
}

type publicationFinding struct {
	Finding review.Finding
	Posted  bool
	Comment int64
}

func snapshotFindings(prior []priorFinding) []publicationFinding {
	out := make([]publicationFinding, len(prior))
	for i, f := range prior {
		out[i] = publicationFinding{Finding: f.Finding, Posted: f.postedInline, Comment: f.commentID}
	}
	return out
}

func restoreFindings(prior []publicationFinding) []priorFinding {
	out := make([]priorFinding, len(prior))
	for i, f := range prior {
		out[i] = priorFinding{Finding: f.Finding, postedInline: f.Posted, commentID: f.Comment}
	}
	return out
}

func (p *publishPhase) checkpoint(ctx context.Context, answer *publicationAnswer) error {
	snapshot := publicationSnapshot{
		Version: 1, Answer: *answer, Review: p.settings.Review, ConfidenceRule: p.settings.Confidence,
		Effort: p.settings.Models.Effort, MaxAutoReviews: p.settings.MaxAutoReviews, Templates: p.templates,
		Scope: p.scope, Trigger: p.trigger, Closed: p.pr.closed, RepoNotes: p.repoNotes,
		PriorID: p.prior.id, PriorHead: p.prior.headSHA, Prior: snapshotFindings(p.prior.findings),
		Dismissed: p.prior.dismissed, Resolved: snapshotFindings(p.resolved), HeldBack: p.heldBack,
		Confidence: p.confidence, Unscored: p.unscored, Unfinished: p.unfinished,
	}
	if p.agent != nil {
		snapshot.Sources = p.agent.Sources
	}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("worker: encode publication: %w", err)
	}
	return p.w.Store.WithAccount(ctx, p.account.ID(), func(tx pgx.Tx) error {
		return store.SavePublication(ctx, tx, p.reviewID, payload)
	})
}

func (p *publishPhase) restore(payload json.RawMessage) (*publicationAnswer, error) {
	var s publicationSnapshot
	if err := json.Unmarshal(payload, &s); err != nil {
		return nil, fmt.Errorf("worker: decode publication: %w", err)
	}
	if s.Version != 1 {
		return nil, fmt.Errorf("worker: unsupported publication version %d", s.Version)
	}
	p.settings = configfile.Settings{Review: s.Review, Confidence: s.ConfidenceRule,
		Models: configfile.Models{Effort: s.Effort}, MaxAutoReviews: s.MaxAutoReviews}
	p.templates, p.scope, p.trigger = s.Templates, s.Scope, s.Trigger
	if p.pr.closed == "" {
		p.pr.closed = s.Closed
	}
	p.repoNotes = s.RepoNotes
	p.prior = priorReview{id: s.PriorID, headSHA: s.PriorHead, findings: restoreFindings(s.Prior), dismissed: s.Dismissed}
	p.resolved, p.heldBack = restoreFindings(s.Resolved), s.HeldBack
	p.confidence, p.unscored, p.unfinished = s.Confidence, s.Unscored, s.Unfinished
	p.agent = &store.AgentRunRow{Sources: s.Sources}
	return &s.Answer, nil
}

// resumePublication runs before admission: a saved answer requires neither
// a runner nor a model slot, even if model settings have since changed.
func (w *Review) resumePublication(ctx context.Context, job *river.Job[jobs.ReviewArgs], file *configfile.File,
	logger *slog.Logger,
) (resumed bool, err error) {
	var saved store.Publication
	err = w.Store.WithAccount(ctx, job.Args.AccountID, func(tx pgx.Tx) error {
		var err error
		saved, err = store.ReadPublication(ctx, tx, job.ID)
		return err
	})
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil || saved.Status != store.ReviewPrepared {
		return true, err
	}
	started := time.Now()
	p := &publishPhase{w: w, reviewID: saved.ReviewID, jobID: job.ID, logger: logger,
		pr: &pullRequest{id: saved.PullRequestID, repositoryID: saved.RepositoryID, repository: saved.Repository,
			number: saved.Number, headSHA: saved.HeadSHA, authorIsBot: saved.AuthorIsBot, closed: saved.Closed}}
	defer func() {
		if err != nil && publicationTerminal(job, err) {
			err = w.failPublication(ctx, job.Args.AccountID, p, err, started)
		}
		if p.client != nil {
			w.settlePublication(ctx, job.Args.AccountID, p)
		}
	}()
	p.account, err = w.account(file, job.Args.AccountID)
	if err != nil {
		return true, err
	}
	p.client, err = w.client(ctx, file, p.account, p.pr.repository)
	if err != nil {
		return true, err
	}
	if p.pr.closed != "" {
		p.client = statusless{p.client}
	}
	if saved.CancelRequested {
		return true, w.cancelPublication(ctx, job, p, started)
	}
	answer, err := p.restore(saved.Payload)
	if err != nil {
		return true, river.JobCancel(err)
	}
	if p.pr.closed != "" {
		p.client = statusless{p.client}
	}
	logger.Info("resuming review publication", "review", saved.ReviewID)
	status, deliveryErr := p.deliver(ctx, answer)
	return true, w.publicationEnded(ctx, job, p, status, deliveryErr, started)
}

func publicationTerminal(job *river.Job[jobs.ReviewArgs], err error) bool {
	_, canceled := errors.AsType[*river.JobCancelError](err)
	return canceled || job.Attempt >= job.MaxAttempts
}

func (w *Review) settlePublication(ctx context.Context, accountID string, p *publishPhase) {
	mctx, cancel := detach(ctx)
	defer cancel()
	owner, repo := p.pr.ownerRepo()
	marks := pullMarks(p.client, owner, repo, p.pr.number, p.logger)
	if !w.otherReviewRunning(mctx, p.logger, accountID, p.pr.id, p.reviewID) {
		marks.remove(mctx, forge.ReactionEyes)
	}
	marks.settle(mctx, w.reviewOutcome(mctx, p.logger, accountID, p.reviewID, nil, false))
}

func (w *Review) failPublication(ctx context.Context, accountID string, p *publishPhase, cause error, started time.Time) error {
	fctx, cancel := detach(ctx)
	defer cancel()
	ended, err := w.endReview(fctx, accountID, p.reviewID, store.ReviewEnd{
		Status: store.ReviewFailed, Error: cause.Error(), OnlyUnfinished: true,
	})
	if err != nil {
		return err
	}
	if !ended {
		return cause
	}
	p.logger.Error("publication failed", "review", p.reviewID, "error", cause)
	if p.client != nil {
		owner, repo := p.pr.ownerRepo()
		if err := p.client.SetStatus(fctx, owner, repo, p.pr.headSHA, forge.StatusError, "kritika: publication failed"); err != nil {
			p.logger.Warn("commit status not set", "error", err)
		}
	} else {
		p.logger.Warn("publication status and reactions not set: forge client unavailable")
	}
	if p.account != nil {
		w.Metrics.Review(p.account.Key(), string(store.ReviewFailed), time.Since(started))
	}
	return cause
}

func (w *Review) cancelPublication(ctx context.Context, job *river.Job[jobs.ReviewArgs], p *publishPhase, started time.Time) error {
	cctx, cancel := context.WithCancelCause(ctx)
	cancel(river.ErrJobCancelledRemotely)
	return w.publicationEnded(cctx, job, p, store.ReviewCanceled, river.ErrJobCancelledRemotely, started)
}

// publicationEnded leaves retryable delivery failures prepared. The job's
// final attempt and explicit cancellation still produce terminal reviews.
func (w *Review) publicationEnded(ctx context.Context, job *river.Job[jobs.ReviewArgs], p *publishPhase,
	status store.ReviewStatus, err error, started time.Time,
) error {
	if err == nil {
		if status != "" {
			w.Metrics.Review(p.account.Key(), string(status), time.Since(started))
			p.logger.Info("review " + string(status))
		}
		return nil
	}
	owner, repo := p.pr.ownerRepo()
	e := endedReview{accountID: p.account.ID(), accountKey: p.account.Key(), reviewID: p.reviewID,
		headSHA: p.pr.headSHA, owner: owner, repo: repo, client: p.client, started: started, logger: p.logger}
	if ctx.Err() != nil && !workerStopping(ctx) {
		return w.finishEnded(ctx, e, err)
	}
	// Cancellation is durable before its notification can arrive. Prepared jobs
	// remain retryable so a request racing this read is handled on their retry.
	fctx, cancel := detach(ctx)
	defer cancel()
	var saved store.Publication
	readErr := w.Store.WithAccount(fctx, p.account.ID(), func(tx pgx.Tx) error {
		var err error
		saved, err = store.ReadPublication(fctx, tx, p.jobID)
		return err
	})
	if readErr == nil && saved.CancelRequested {
		return w.cancelPublication(ctx, job, p, started)
	}
	if publicationTerminal(job, err) {
		return w.failPublication(ctx, p.account.ID(), p, err, started)
	}
	p.logger.Warn("publication will be retried", "review", p.reviewID, "error", err)
	return errors.Join(err, readErr)
}

func (p *publishPhase) deliver(job context.Context, answer *publicationAnswer) (store.ReviewStatus, error) {
	if cause := context.Cause(job); errors.Is(cause, errJobFenced) {
		return store.ReviewSuperseded, cause
	}
	lockCtx, cancel := context.WithTimeout(context.WithoutCancel(job), 2*detachTimeout)
	defer cancel()
	var status store.ReviewStatus
	err := p.w.Store.WithPublicationLock(lockCtx, p.pr.id, func(ctx context.Context) error {
		var err error
		status, err = p.deliverLocked(ctx, answer)
		return err
	})
	return status, err
}

func (p *publishPhase) deliverLocked(lockCtx context.Context, answer *publicationAnswer) (store.ReviewStatus, error) {
	ctx, cancel := context.WithTimeout(lockCtx, detachTimeout)
	defer cancel()
	var saved store.Publication
	current := false
	err := p.w.Store.WithAccount(ctx, p.account.ID(), func(tx pgx.Tx) error {
		var err error
		saved, err = store.ReadPublication(ctx, tx, p.jobID)
		if err != nil || saved.Status != store.ReviewPrepared {
			return err
		}
		current, err = store.PublicationCurrent(ctx, tx, p.reviewID)
		return err
	})
	if err != nil || saved.Status != store.ReviewPrepared {
		return "", err
	}
	if saved.CancelRequested {
		return store.ReviewCanceled, river.ErrJobCancelledRemotely
	}
	if !current {
		ended, err := p.w.endReview(ctx, p.account.ID(), p.reviewID, store.ReviewEnd{
			Status: store.ReviewSuperseded, OnlyUnfinished: true,
		})
		if ended {
			return store.ReviewSuperseded, err
		}
		return "", err
	}
	res, unanchored := answer.Result, answer.Unanchored
	notes := slices.Clone(answer.Notes)
	pauseNote, err := p.autoReviewPauses(ctx)
	if err != nil {
		return "", err
	}
	if pauseNote != "" {
		notes = append(notes, pauseNote)
	}
	commentID, inline, err := p.writeBack(ctx, res, unanchored, answer.Model, append(notes, p.repoNotes...))
	if err != nil {
		return "", err
	}
	// Delivery may consume its entire timeout after posting. Recording that
	// outcome has its own bound, while the publication lock is still held.
	fctx, finish := context.WithTimeout(lockCtx, detachTimeout)
	defer finish()
	completed := false
	err = p.w.Store.WithAccount(fctx, p.account.ID(), func(tx pgx.Tx) error {
		var err error
		completed, err = store.CompletePublication(fctx, tx, store.ReviewResult{
			AccountID: p.account.ID(), ReviewID: p.reviewID, PullRequestID: p.pr.id,
			Result: review.Result{Summary: res.Summary, Findings: slices.Concat(res.Findings, unanchored)},
			Inline: inline, Model: answer.Model, CommentID: commentID, Confidence: p.confidence, Partial: p.unfinished > 0,
		}, p.trigger != jobs.TriggerManual, pauseNote != "")
		return err
	})
	if err != nil || !completed {
		return "", err
	}
	p.countFindings(answer.Result)
	if c := p.confidence; c != nil {
		p.w.Metrics.ConfidenceScored(p.account.Key(), c.Score, string(c.Risk))
	}
	if pauseNote != "" {
		p.logger.Info("automatic reviews paused")
	}
	return store.ReviewCompleted, nil
}
