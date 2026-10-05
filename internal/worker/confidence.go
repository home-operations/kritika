package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/adapter"
	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/forge"
	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/review"
	"github.com/home-operations/kritika/internal/store"
)

// confidenceTimeout bounds scoring a review, the wait for a slot on the
// confidence model included, and confidenceMaxOutputTokens its answer: a
// score and a sentence or two, with room for a reasoning model's thinking.
// With the write-back's own bound it stays inside
// jobtimeout.PublishHeadroom.
const (
	confidenceTimeout         = 2 * time.Minute
	confidenceMaxOutputTokens = 4096
)

// unscoredNote is what the summary states when the repository asks for a
// confidence score and the scorer gave none.
const unscoredNote = "The confidence model did not answer, so this review has no confidence score"

// judge has the repository's confidence model score the reviewed pull
// request, on a context of its own so the score is had even if job ends
// meanwhile. It returns the note the summary states when the scorer gave
// none: the review still stands, without a verdict on it.
func (p *publishPhase) judge(job context.Context, res review.Result, diff string) string {
	ref := p.settings.Confidence.Model
	if ref == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(job), confidenceTimeout)
	defer cancel()
	// The agent is done with its slot on the review model. Scoring takes a
	// slot of its own, and a job that held one while it waited for another
	// could wait on a job doing the same the other way round.
	if p.lease != nil {
		p.w.releaseLease(ctx, p.logger, p.lease, string(p.settings.Models.Review))
	}
	c, err := p.score(ctx, ref, res, diff)
	if err != nil {
		p.logger.Warn("confidence not scored", "model", ref, "error", err)
		p.unscored = true
		return unscoredNote
	}
	p.logger.Info("confidence scored", "model", c.Model, "score", c.Score, "threshold", c.Threshold)
	p.confidence = &c
	return ""
}

// score asks ref for the score, recording the call against the review and
// charging it to the account.
func (p *publishPhase) score(ctx context.Context, ref configfile.ModelRef, res review.Result, diff string) (review.Confidence, error) {
	// The agent's admission checked the caps before it ran; what it spent
	// since may have reached the month's.
	if limit := p.settings.Limits.TokensPerMonth; limit > 0 {
		u, err := p.w.Store.AccountUsage(ctx, p.account.ID())
		if err != nil {
			return review.Confidence{}, fmt.Errorf("worker: read caps: %w", err)
		}
		if u.Tokens >= limit {
			return review.Confidence{}, fmt.Errorf("worker: tokensPerMonth (%d) reached", limit)
		}
	}
	stepper, err := p.w.Steppers.Stepper(p.file, p.account, ref.Provider())
	if err != nil {
		return review.Confidence{}, err
	}
	spec, _ := p.file.Provider(p.account, ref.Provider())
	// The calls are charged as they are made: one the model answered
	// without the score is billed all the same.
	record := p.w.recorder().OnStep(ctx, p.logger, store.ModelCall{
		AccountID: p.account.ID(), ReviewID: p.reviewID, Kind: store.ModelCallConfidence,
	}, adapter.Mask(p.file, spec))
	completer := model.Structured{Stepper: stepper, OnStep: func(req model.StepRequest, resp model.StepResponse, err error, d time.Duration) {
		record(req, resp, err, d)
		p.charge(ctx, resp)
	}}
	req := model.CompletionRequest{
		System: review.ConfidenceSystem,
		User: review.BuildConfidence(review.Input{
			Repository: p.pr.repository, Number: p.pr.number, Title: p.pr.title, Author: p.pr.author, BaseRef: p.pr.baseRef,
			Changed: review.ChangedPaths(diff), Diff: diff, Dismissed: dismissedFindings(p.prior.dismissed),
		}, res.Findings),
		Model: ref.Model(), Session: "confidence-" + p.reviewID,
		Schema: review.ConfidenceSchema(), SchemaName: "confidence", MaxTokens: confidenceMaxOutputTokens,
	}
	var resp model.CompletionResponse
	call := func(ctx context.Context) error {
		var err error
		resp, err = completer.Complete(ctx, req)
		p.w.Metrics.ModelCall(p.account.Key(), adapter.ServedRef(ref, resp.Model), store.RoleConfidence, adapter.Outcome(err),
			resp.InputTokens, resp.CachedTokens, resp.OutputTokens, resp.CostUSD)
		return err
	}
	if err := p.w.withLease(ctx, p.account, string(ref), p.settings.Limits.Concurrency, p.jobID, call); err != nil {
		return review.Confidence{}, err
	}
	score, reason, err := review.ParseConfidence(resp.Raw, p.pr.repository, res.Counts())
	if err != nil {
		return review.Confidence{}, err
	}
	return review.Confidence{Score: score, Threshold: p.settings.Confidence.Threshold, Reason: reason, Model: resp.Model}, nil
}

// charge records what one scoring call spent against the review, where the
// caps count it; a call the provider did not answer spent nothing.
func (p *publishPhase) charge(ctx context.Context, resp model.StepResponse) {
	if resp.Usage.Prompt() == 0 && resp.Usage.Output == 0 {
		return
	}
	err := p.w.Store.WithAccount(ctx, p.account.ID(), func(tx pgx.Tx) error {
		return store.InsertUsage(ctx, tx, store.Usage{
			AccountID: p.account.ID(), RepositoryID: p.pr.repositoryID, ReviewID: p.reviewID, Role: store.RoleConfidence,
			Model: resp.Model, Upstream: resp.Upstream, Input: resp.Usage.Prompt(), Output: resp.Usage.Output, CostUSD: resp.CostUSD,
		})
	})
	if err != nil {
		p.logger.Error("confidence usage not recorded", "error", err)
	}
}

// verdict is the commit status of a published review with that many
// findings: a success saying so, unless the repository asks for a
// confidence score, when the score decides, and a review left unscored is
// an error rather than a pass.
func (p *publishPhase) verdict(findings int) (forge.StatusState, string) {
	desc := "no findings"
	if findings > 0 {
		desc = fmt.Sprintf("%d finding(s)", findings)
	}
	switch c := p.confidence; {
	case p.unscored:
		return forge.StatusError, "confidence not scored, " + desc
	case c == nil:
		return forge.StatusSuccess, desc
	case c.Passed():
		return forge.StatusSuccess, fmt.Sprintf("confidence %d/%d, %s", c.Score, review.MaxConfidence, desc)
	default:
		return forge.StatusFailure, fmt.Sprintf("confidence %d/%d, below %d, %s", c.Score, review.MaxConfidence, c.Threshold, desc)
	}
}

// carriedConfidence is the confidence a patch unchanged since its last
// review keeps: the one the pull request's newest prepared or completed
// review other than reviewID got, held to the threshold want sets now.
// skippable says the patch may be skipped as unchanged: always for a
// repository that asks for no score, and otherwise only when that review
// left one to carry, so a rebase neither passes a patch that failed nor
// leaves an unscored one unscored.
func carriedConfidence(
	ctx context.Context, tx pgx.Tx, prID, reviewID string, want configfile.Confidence,
) (c *review.Confidence, skippable bool, err error) {
	if want.Model == "" {
		return nil, true, nil
	}
	var raw []byte
	err = tx.QueryRow(ctx, `SELECT confidence FROM reviews WHERE pull_request_id = $1 AND id::text <> $2
		AND status IN ('prepared', 'completed') ORDER BY created_at DESC LIMIT 1`, prID, reviewID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && raw == nil) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("worker: read last review's confidence: %w", err)
	}
	c = &review.Confidence{}
	if err := json.Unmarshal(raw, c); err != nil {
		return nil, false, fmt.Errorf("worker: decode last review's confidence: %w", err)
	}
	c.Threshold = want.Threshold
	return c, true, nil
}

// skipVerdict is the commit status of a review skipped for reason: a
// success saying so, unless the skip carries the score its unchanged patch
// got, which then decides as it did.
func skipVerdict(carried *review.Confidence, reason string) (forge.StatusState, string) {
	desc := "skipped (" + reason + ")"
	switch {
	case carried == nil:
		return forge.StatusSuccess, desc
	case carried.Passed():
		return forge.StatusSuccess, fmt.Sprintf("confidence %d/%d, %s", carried.Score, review.MaxConfidence, desc)
	default:
		return forge.StatusFailure, fmt.Sprintf("confidence %d/%d, below %d, %s", carried.Score, review.MaxConfidence, carried.Threshold, desc)
	}
}
