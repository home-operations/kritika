package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/configfile"
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

// publicationContext is the frozen review policy and history both stages use.
type publicationContext struct {
	PriorConfidence *review.Confidence
	Review          configfile.Review
	ConfidenceRule  configfile.Confidence
	Effort          model.Effort
	Route           string `json:",omitempty"`
	Focus           string `json:",omitempty"`
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
	Started         time.Time
}

// preparationInput carries the runner answer and every input needed to prepare it.
type preparationInput struct {
	publicationContext
	Version     int
	Preparation *publicationPreparation
}

// publicationInput is the finalized answer and its rendering context. Delivery
// has no dependency on parsing settings or on the original runner's full row.
type publicationInput struct {
	publicationContext
	Version   int
	Answer    publicationAnswer
	Agent     *store.AgentRunRow `json:",omitempty"`
	RunID     string             `json:",omitempty"`
	Reviewers []string           `json:",omitempty"`
}

// publicationSnapshot decodes either stage's persisted input: a
// publicationInput, version 1, or a preparationInput, version 2.
type publicationSnapshot struct {
	publicationInput
	Preparation *publicationPreparation `json:",omitempty"`
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

func (p *publishPhase) snapshotContext() publicationContext {
	snapshot := publicationContext{
		Review: p.settings.Review, ConfidenceRule: p.settings.Confidence,
		Effort: p.settings.Models.Effort, Route: p.settings.Models.Route, MaxAutoReviews: p.settings.MaxAutoReviews, Templates: p.templates,
		Scope: p.scope, Trigger: p.trigger, Focus: p.focus, Closed: p.pr.closed, RepoNotes: p.repoNotes,
		PriorID: p.prior.id, PriorConfidence: p.prior.confidence, PriorHead: p.prior.headSHA, PriorDiagram: p.prior.diagram,
		Prior:     snapshotFindings(p.prior.findings),
		Dismissed: p.prior.dismissed, Resolved: snapshotFindings(p.resolved), HeldBack: p.heldBack,
		Confidence: p.confidence, Unscored: p.unscored, Unfinished: p.unfinished, Started: p.started,
	}
	if p.agent != nil {
		snapshot.Sources = p.agent.Sources
	}
	return snapshot
}

func (p *publishPhase) preparationInput() (preparationInput, error) {
	if p.agent == nil {
		return preparationInput{}, errors.New("worker: the runner wrote no agent run")
	}
	return preparationInput{publicationContext: p.snapshotContext(), Version: 2,
		Preparation: &publicationPreparation{Agent: *p.agent, RunID: p.runID, Parse: p.parse,
			Models: p.settings.Models, Limits: p.settings.Limits, MaxPromptTokens: p.settings.Agent.MaxPromptTokens,
			Title: p.pr.title, Author: p.pr.author, BaseRef: p.pr.baseRef}}, nil
}

func (p *publishPhase) publicationInput(answer publicationAnswer) publicationInput {
	input := publicationInput{publicationContext: p.snapshotContext(), Version: 1,
		Answer: answer, RunID: p.runID, Reviewers: p.reviewers}
	if p.agent != nil {
		input.Agent = &store.AgentRunRow{CreatedAt: p.agent.CreatedAt,
			Sources: p.agent.Sources, SkillsOffered: p.agent.SkillsOffered, SkillsOpened: p.agent.SkillsOpened,
			CommandsOffered: p.agent.CommandsOffered, CommandsRun: p.agent.CommandsRun}
	}
	return input
}

func (p *publishPhase) checkpoint(ctx context.Context, answer *publicationAnswer) error {
	defer p.timeStage("checkpoint")()
	var snapshot any
	if answer == nil {
		input, err := p.preparationInput()
		if err != nil {
			return err
		}
		snapshot = input
	} else {
		snapshot = p.publicationInput(*answer)
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
		Models: configfile.Models{Effort: s.Effort, Route: s.Route}, MaxAutoReviews: s.MaxAutoReviews}
	// Whether the comments carry the dashboard's URL follows the
	// configuration the publication resumes under, not the one it was saved
	// with: the restart that turned comments.dashboard off may be what
	// interrupted it.
	p.settings.Review.Dashboard = p.file.Settings(p.account).Review.Dashboard
	p.templates, p.scope, p.trigger, p.focus = s.Templates, s.Scope, s.Trigger, s.Focus
	p.pr.closed = s.Closed
	p.repoNotes = s.RepoNotes
	p.prior = priorReview{id: s.PriorID, confidence: s.PriorConfidence, headSHA: s.PriorHead, diagram: s.PriorDiagram,
		findings: restoreFindings(s.Prior), dismissed: s.Dismissed}
	p.resolved, p.heldBack = restoreFindings(s.Resolved), s.HeldBack
	p.confidence, p.unscored, p.unfinished = s.Confidence, s.Unscored, s.Unfinished
	p.reviewers = s.Reviewers
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
