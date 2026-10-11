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
	p.checkpointed = true
	return &s.Answer, nil
}

// resumePublication runs before admission: a saved answer requires neither
// a runner nor a model slot, even if model settings have since changed.
func (w *Review) resumePublication(ctx context.Context, job *river.Job[jobs.ReviewArgs], file *configfile.File,
	account *configfile.Account, logger *slog.Logger,
) (resumed bool, err error) {
	var saved store.Publication
	err = w.Store.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
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
	defer func() {
		if err == nil || job.Attempt < job.MaxAttempts {
			return
		}
		fctx, cancel := detach(ctx)
		defer cancel()
		_, finishErr := w.endReview(fctx, account.ID(), saved.ReviewID, store.ReviewEnd{
			Status: store.ReviewFailed, Error: err.Error(), OnlyUnfinished: true,
		})
		err = errors.Join(err, finishErr)
	}()
	pr, err := loadPullRequest(ctx, w.Store, account.ID(), job.Args.RepositoryID, job.Args.Number)
	if err != nil {
		return true, err
	}
	client, err := w.client(ctx, file, account, pr.repository)
	if err != nil {
		return true, err
	}
	pr.headSHA = saved.HeadSHA
	p := &publishPhase{w: w, account: account, client: client, pr: pr, reviewID: saved.ReviewID, jobID: job.ID, logger: logger}
	answer, err := p.restore(saved.Payload)
	if err != nil {
		return true, err
	}
	if pr.closed != "" {
		p.client = statusless{client}
	}
	owner, repo := pr.ownerRepo()
	end := pullMarks(client, owner, repo, pr.number, logger).start(ctx)
	started := time.Now()
	logger.Info("resuming review publication", "review", saved.ReviewID)
	if saved.CancelRequested {
		cctx, cancel := context.WithCancelCause(ctx)
		cancel(river.ErrJobCancelledRemotely)
		err = w.publicationEnded(cctx, job, p, store.ReviewCanceled, river.ErrJobCancelledRemotely, started)
	} else {
		status, deliveryErr := p.deliver(ctx, answer)
		err = w.publicationEnded(ctx, job, p, status, deliveryErr, started)
	}
	mctx, cancel := detach(ctx)
	defer cancel()
	end(w.reviewOutcome(mctx, logger, account.ID(), p.reviewID, err, job.Attempt >= job.MaxAttempts),
		w.otherReviewRunning(mctx, logger, account.ID(), pr.id, p.reviewID))
	return true, err
}

// publicationEnded leaves retryable delivery failures prepared. The job's
// final attempt and explicit cancellation still produce terminal reviews.
func (w *Review) publicationEnded(ctx context.Context, job *river.Job[jobs.ReviewArgs], p *publishPhase,
	status store.ReviewStatus, err error, started time.Time,
) error {
	owner, repo := p.pr.ownerRepo()
	e := endedReview{accountID: p.account.ID(), accountKey: p.account.Key(), reviewID: p.reviewID,
		headSHA: p.pr.headSHA, owner: owner, repo: repo, client: p.client, started: started, logger: p.logger}
	if err != nil {
		if ctx.Err() != nil && !workerStopping(ctx) {
			return w.finishEnded(ctx, e, err)
		}
		if job.Attempt < job.MaxAttempts {
			p.logger.Warn("publication will be retried", "review", p.reviewID, "error", err)
			return err
		}
		fctx, cancel := detach(ctx)
		defer cancel()
		ended, finishErr := w.endReview(fctx, p.account.ID(), p.reviewID, store.ReviewEnd{
			Status: store.ReviewFailed, Error: err.Error(), OnlyUnfinished: true,
		})
		if ended {
			if statusErr := p.client.SetStatus(fctx, owner, repo, p.pr.headSHA, forge.StatusError, "kritika: publication failed"); statusErr != nil {
				p.logger.Warn("commit status not set", "error", statusErr)
			}
			w.Metrics.Review(p.account.Key(), string(store.ReviewFailed), time.Since(started))
		}
		return errors.Join(err, finishErr)
	}
	w.Metrics.Review(p.account.Key(), string(status), time.Since(started))
	p.logger.Info("review " + string(status))
	return nil
}

// withAccount reuses delivery's transaction so a publisher never waits for
// a second pool connection while holding its publication lock.
func (p *publishPhase) withAccount(ctx context.Context, fn func(pgx.Tx) error) error {
	if p.tx != nil {
		return fn(p.tx)
	}
	return p.w.Store.WithAccount(ctx, p.account.ID(), fn)
}

func (p *publishPhase) deliver(job context.Context, answer *publicationAnswer) (store.ReviewStatus, error) {
	// A fenced job may be another replica's by now, its review on the way:
	// the detached write-back must not post beside that one.
	if cause := context.Cause(job); errors.Is(cause, errJobFenced) {
		return store.ReviewSuperseded, cause
	}
	// Scoring had its own time; the write-back gets a whole bound after it.
	ctx, cancel := detach(job)
	defer cancel()
	status := store.ReviewPrepared
	completed := false
	err := p.w.Store.WithAccount(ctx, p.account.ID(), func(tx pgx.Tx) error {
		p.tx = tx
		defer func() { p.tx = nil }()
		if err := store.LockPublication(ctx, tx, p.pr.id); err != nil {
			return err
		}
		saved, err := store.ReadPublication(ctx, tx, p.jobID)
		if err != nil {
			return err
		}
		if saved.Status != store.ReviewPrepared {
			status = saved.Status
			return nil
		}
		current, err := store.PublicationCurrent(ctx, tx, p.reviewID)
		if err != nil {
			return err
		}
		if !current {
			status = store.ReviewSuperseded
			_, err := store.EndReview(ctx, tx, p.reviewID, store.ReviewEnd{Status: status, OnlyUnfinished: true})
			return err
		}
		res, unanchored := answer.Result, answer.Unanchored
		notes := slices.Clone(answer.Notes)
		pauseNote, err := p.autoReviewPauses(ctx)
		if err != nil {
			return err
		}
		if pauseNote != "" {
			notes = append(notes, pauseNote)
		}
		commentID, inline, err := p.writeBack(ctx, res, unanchored, answer.Model, append(notes, p.repoNotes...))
		if err != nil {
			return err
		}
		completed, err = store.CompletePublication(ctx, tx, store.ReviewResult{
			AccountID: p.account.ID(), ReviewID: p.reviewID, PullRequestID: p.pr.id,
			Result: review.Result{Summary: res.Summary, Findings: slices.Concat(res.Findings, unanchored)},
			Inline: inline, Model: answer.Model, CommentID: commentID, Confidence: p.confidence, Partial: p.unfinished > 0,
		}, p.trigger != jobs.TriggerManual, pauseNote != "")
		if err == nil {
			status = store.ReviewCompleted
		}
		return err
	})
	if err != nil {
		return store.ReviewPrepared, err
	}
	if completed {
		p.countFindings(answer.Result)
		if c := p.confidence; c != nil {
			p.w.Metrics.ConfidenceScored(p.account.Key(), c.Score, string(c.Risk))
		}
	}
	return status, nil
}
