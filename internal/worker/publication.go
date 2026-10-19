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

type publicationPreparation struct {
	Agent                  store.AgentRunRow
	RunID                  string
	Parse                  review.ParseOptions
	Models                 configfile.Models
	Limits                 configfile.Limits
	MaxPromptTokens        int
	Title, Author, BaseRef string
}

// publicationSnapshot freezes the inputs to preparation and delivery.
// Credentials and model clients are resolved separately.
type publicationSnapshot struct {
	Agent           *store.AgentRunRow      `json:",omitempty"`
	RunID           string                  `json:",omitempty"`
	Preparation     *publicationPreparation `json:",omitempty"`
	PriorConfidence *review.Confidence
	Version         int
	Answer          publicationAnswer
	Review          configfile.Review
	ConfidenceRule  configfile.Confidence
	Effort          model.Effort
	MaxAutoReviews  int
	Templates       review.Templates
	Scope           review.Scope
	Trigger         string
	Closed          string
	RepoNotes       []string
	Sources         []string
	PriorID         string
	PriorHead       string
	PriorDiagram    string
	Prior           []publicationFinding
	Dismissed       []store.Dismissal
	Resolved        []publicationFinding
	HeldBack        []review.Finding
	Confidence      *review.Confidence
	Unscored        bool
	Unfinished      int
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
	defer p.timeStage("checkpoint")()
	snapshot := publicationSnapshot{
		Version: 1, Review: p.settings.Review, ConfidenceRule: p.settings.Confidence,
		Effort: p.settings.Models.Effort, MaxAutoReviews: p.settings.MaxAutoReviews, Templates: p.templates,
		Scope: p.scope, Trigger: p.trigger, Closed: p.pr.closed, RepoNotes: p.repoNotes,
		PriorID: p.prior.id, PriorConfidence: p.prior.confidence, PriorHead: p.prior.headSHA, PriorDiagram: p.prior.diagram,
		Prior:     snapshotFindings(p.prior.findings),
		Dismissed: p.prior.dismissed, Resolved: snapshotFindings(p.resolved), HeldBack: p.heldBack,
		Confidence: p.confidence, Unscored: p.unscored, Unfinished: p.unfinished,
	}
	if p.agent != nil {
		snapshot.Sources = p.agent.Sources
	}
	if answer == nil {
		if p.agent == nil {
			return errors.New("worker: the runner wrote no agent run")
		}
		snapshot.Version = 2
		snapshot.Preparation = &publicationPreparation{Agent: *p.agent, RunID: p.runID, Parse: p.parse,
			Models: p.settings.Models, Limits: p.settings.Limits, MaxPromptTokens: p.settings.Agent.MaxPromptTokens,
			Title: p.pr.title, Author: p.pr.author, BaseRef: p.pr.baseRef}
	} else {
		snapshot.Answer = *answer
		snapshot.RunID = p.runID
		if p.agent != nil {
			snapshot.Agent = &store.AgentRunRow{
				CreatedAt: p.agent.CreatedAt,
				Sources:   p.agent.Sources, SkillsOffered: p.agent.SkillsOffered, SkillsOpened: p.agent.SkillsOpened,
				CommandsOffered: p.agent.CommandsOffered, CommandsRun: p.agent.CommandsRun,
			}
		}
	}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("worker: encode publication: %w", err)
	}
	err = p.w.Store.WithAccount(ctx, p.account.ID(), func(tx pgx.Tx) error {
		var err error
		if p.pending != nil {
			err = store.ReplacePublication(ctx, tx, p.reviewID, p.pending, payload)
		} else {
			err = store.SavePublication(ctx, tx, p.reviewID, payload)
		}
		if err == nil && p.handoff != nil {
			err = p.handoff(ctx, tx)
		}
		return err
	})
	if err == nil {
		p.pending = nil
		if answer == nil {
			p.pending = payload
		}
	}
	return err
}

func (p *publishPhase) restore(payload json.RawMessage) (*publicationAnswer, error) {
	var s publicationSnapshot
	if err := json.Unmarshal(payload, &s); err != nil {
		return nil, fmt.Errorf("worker: decode publication: %w", err)
	}
	if s.Version != 1 && s.Version != 2 {
		return nil, fmt.Errorf("worker: unsupported publication version %d", s.Version)
	}
	p.settings = configfile.Settings{Review: s.Review, Confidence: s.ConfidenceRule,
		Models: configfile.Models{Effort: s.Effort}, MaxAutoReviews: s.MaxAutoReviews}
	// Whether the comments carry the dashboard's URL follows the
	// configuration the publication resumes under, not the one it was saved
	// with: the restart that turned comments.dashboard off may be what
	// interrupted it.
	p.settings.Review.Dashboard = p.file.Settings(p.account, p.pr.repository).Review.Dashboard
	p.templates, p.scope, p.trigger = s.Templates, s.Scope, s.Trigger
	p.pr.closed = s.Closed
	p.repoNotes = s.RepoNotes
	p.prior = priorReview{id: s.PriorID, confidence: s.PriorConfidence, headSHA: s.PriorHead, diagram: s.PriorDiagram,
		findings: restoreFindings(s.Prior), dismissed: s.Dismissed}
	p.resolved, p.heldBack = restoreFindings(s.Resolved), s.HeldBack
	p.confidence, p.unscored, p.unfinished = s.Confidence, s.Unscored, s.Unfinished
	p.agent = &store.AgentRunRow{Sources: s.Sources}
	if s.Agent != nil {
		p.agent, p.runID = s.Agent, s.RunID
	}
	if s.Version == 2 {
		if s.Preparation == nil {
			return nil, errors.New("worker: publication preparation missing")
		}
		prep := s.Preparation
		p.agent, p.runID, p.parse = &prep.Agent, prep.RunID, prep.Parse
		p.settings.Models, p.settings.Limits = prep.Models, prep.Limits
		p.settings.Agent.MaxPromptTokens = prep.MaxPromptTokens
		p.pr.title, p.pr.author, p.pr.baseRef = prep.Title, prep.Author, prep.BaseRef
		p.pending = payload
		return nil, nil
	}
	return &s.Answer, nil
}

// resumePublication runs before admission. A saved runner answer may still
// need merge and confidence calls; a finalized answer only needs delivery.
func (w *Review) resumePublication(ctx context.Context, job *river.Job[jobs.ReviewArgs], file *configfile.File,
	logger *slog.Logger,
) (resumed bool, err error) {
	pickedUp := time.Now()
	var saved store.Publication
	err = w.Store.WithAccount(ctx, job.Args.AccountID, func(tx pgx.Tx) error {
		var err error
		saved, err = store.ReadPublication(ctx, tx, job.ID)
		return err
	})
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil || saved.Status != store.ReviewPrepared || saved.JobID != job.ID {
		return true, err
	}
	started := time.Now()
	p := &publishPhase{w: w, file: file, reviewID: saved.ReviewID, jobID: job.ID, logger: logger,
		pr: &pullRequest{id: saved.PullRequestID, repositoryID: saved.RepositoryID, repository: saved.Repository,
			number: saved.Number, headSHA: saved.HeadSHA, authorIsBot: saved.AuthorIsBot, closed: saved.Closed}}
	if job.Kind == jobs.QueuePrepare {
		p.handoff = func(ctx context.Context, tx pgx.Tx) error {
			return p.advance(ctx, tx, job, jobs.PublishArgs{ReviewArgs: job.Args, ReviewID: saved.ReviewID})
		}
	}
	handled, handedOff := false, false
	defer func() {
		if !handled && err != nil {
			decision := decidePublication(context.Cause(ctx), "", err, true, job.Attempt >= job.MaxAttempts)
			switch decision.disposition {
			case reviewInterrupted, reviewPublicationFailed:
				err = w.finishPublication(ctx, job.Args.AccountID, p, decision, started)
			}
		}
		// The pull request keeps its eyes while the next stage owns the review.
		if p.client != nil && !handedOff {
			w.settlePublication(ctx, job.Args.AccountID, p)
		}
	}()
	p.account, err = w.account(file, job.Args.AccountID)
	if err != nil {
		return true, err
	}
	client, err := w.client(ctx, file, p.account, p.pr.repository)
	if err != nil {
		return true, err
	}
	p.client = client
	if p.pr.closed != "" {
		p.client = statusless{client}
	}
	if saved.CancelRequested {
		handled = true
		return true, w.cancelPublication(ctx, job, p, started)
	}
	answer, err := p.restore(saved.Payload)
	if err != nil {
		return true, river.JobCancel(err)
	}
	// The checkpoint knows whether the review began on an open pull
	// request, which is what set its pending status; a close since, even
	// one the row's timestamps put before the review, leaves the outcome
	// to replace it.
	p.client = client
	if p.pr.closed != "" {
		p.client = statusless{client}
	}
	p.logger = logger.With("account", p.account.Key(), "attempt", job.Attempt)
	p.logger.Info("resuming review publication", "review", saved.ReviewID, "run", p.runID, "job", job.ID, "stage", job.Kind,
		"queue_wait", max(0, pickedUp.Sub(job.ScheduledAt)), "since_enqueued", max(0, pickedUp.Sub(job.CreatedAt)))
	var status store.ReviewStatus
	var deliveryErr error
	if p.pending != nil {
		if job.Kind == jobs.QueuePublish {
			return true, river.JobCancel(errors.New("worker: publication answer is not finalized"))
		}
		status, deliveryErr = p.run(ctx)
	} else {
		status, deliveryErr = p.deliver(ctx, answer)
	}
	handled = true
	if deliveryErr == nil && status == store.ReviewPrepared && p.handoff != nil {
		handedOff = true
		return true, nil
	}
	return true, w.publicationEnded(ctx, job, p, status, deliveryErr, started)
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

func (w *Review) finishPublication(
	ctx context.Context, accountID string, p *publishPhase, decision reviewDecision, started time.Time,
) error {
	fctx, cancel := detach(ctx)
	defer cancel()
	ended, err := w.endReview(fctx, accountID, p.reviewID, decision.end)
	if err != nil {
		return err
	}
	if !ended {
		return decision.jobError()
	}
	level := slog.LevelInfo
	if decision.disposition == reviewPublicationFailed {
		level = slog.LevelError
	}
	p.logger.Log(fctx, level, "publication "+string(decision.end.Status), "review", p.reviewID, "error", decision.end.Error)
	if p.client != nil {
		w.reportEnd(fctx, p.ended(), decision.description)
	} else {
		p.logger.Warn("publication status and reactions not set: forge client unavailable")
	}
	if p.account != nil {
		w.Metrics.Review(p.account.Key(), string(decision.end.Status), time.Since(started))
	}
	return decision.jobError()
}

func (w *Review) cancelPublication(ctx context.Context, job *river.Job[jobs.ReviewArgs], p *publishPhase, started time.Time) error {
	cctx, cancel := context.WithCancelCause(context.WithoutCancel(ctx))
	cancel(river.ErrJobCancelledRemotely)
	return w.publicationEnded(cctx, job, p, store.ReviewCanceled, river.ErrJobCancelledRemotely, started)
}

// publicationEnded leaves retryable delivery failures prepared. The job's
// final attempt and explicit cancellation still produce terminal reviews.
func (w *Review) publicationEnded(ctx context.Context, job *river.Job[jobs.ReviewArgs], p *publishPhase,
	status store.ReviewStatus, err error, started time.Time,
) error {
	decision := decidePublication(context.Cause(ctx), status, err, true, job.Attempt >= job.MaxAttempts)
	if err == nil {
		if status != "" {
			w.Metrics.Review(p.account.Key(), string(status), time.Since(started))
			p.logger.Info("review ended", "status", status)
		}
		return nil
	}
	if decision.disposition == reviewInterrupted {
		return w.finishPublication(ctx, job.Args.AccountID, p, decision, started)
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
	if decision.disposition == reviewPublicationFailed {
		return w.finishPublication(ctx, p.account.ID(), p, decision, started)
	}
	p.logger.Warn("publication will be retried", "review", p.reviewID, "error", err)
	return errors.Join(decision.jobError(), readErr)
}

// preparationCurrent checks durable cancellation even when River's context
// already carries a shutdown cause and cannot receive a later cancellation,
// and whether a newer head or review has overtaken the review meanwhile.
func (p *publishPhase) preparationCurrent(ctx context.Context) error {
	return p.w.Store.WithAccount(ctx, p.account.ID(), func(tx pgx.Tx) error {
		saved, err := store.ReadPublication(ctx, tx, p.jobID)
		if err != nil {
			return err
		}
		if saved.CancelRequested {
			return river.ErrJobCancelledRemotely
		}
		current, err := store.PublicationCurrent(ctx, tx, p.reviewID)
		if err != nil || current {
			return err
		}
		replaced, err := store.ReviewReplaced(ctx, tx, p.reviewID)
		if err != nil {
			return err
		}
		if replaced {
			return errReplaced
		}
		return errSuperseded
	})
}

func (p *publishPhase) deliver(job context.Context, answer *publicationAnswer) (store.ReviewStatus, error) {
	defer p.timeStage("deliver")()
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
	persistDone := p.timeStage("persist")
	err = p.w.Store.WithAccount(fctx, p.account.ID(), func(tx pgx.Tx) error {
		var err error
		completed, err = store.CompletePublication(fctx, tx, store.ReviewResult{
			AccountID: p.account.ID(), ReviewID: p.reviewID, PullRequestID: p.pr.id,
			Result: review.Result{Summary: res.Summary, Findings: slices.Concat(res.Findings, unanchored)},
			Inline: inline, Model: answer.Model, CommentID: commentID, Confidence: p.confidence, Partial: p.unfinished > 0,
		}, p.trigger != jobs.TriggerManual, pauseNote != "")
		return err
	})
	persistDone()
	if err != nil || !completed {
		return "", err
	}
	p.agentElapsed("agent_to_published")
	p.countFindings(answer.Result)
	if c := p.confidence; c != nil {
		p.w.Metrics.ConfidenceScored(p.account.Key(), c.Score, string(c.Risk))
	}
	if pauseNote != "" {
		p.logger.Info("automatic reviews paused")
	}
	return store.ReviewCompleted, nil
}
