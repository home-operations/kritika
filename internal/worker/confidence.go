package worker

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/forge"
	"github.com/home-operations/kritika/internal/jobs"
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

// confidenceBudgetShare is how many times the agent's prompt budget the
// scorer's prompt can take, up to confidenceBudgetCap tokens and never less
// than the agent's. The scorer makes one call with no tool outputs to leave
// room for, and the diff is what its risk rating rests on. But an agent
// budget set for a large context must not double past what the scorer's model
// holds.
const (
	confidenceBudgetShare = 2
	confidenceBudgetCap   = 96_000
)

// confidenceBudget is the scorer's prompt budget for an agent budget of
// prompt tokens.
func confidenceBudget(prompt int) int {
	return max(prompt, min(confidenceBudgetShare*prompt, confidenceBudgetCap))
}

// unscoredNote is what the summary states when the repository asks for a
// confidence score and the scorer gave none.
const unscoredNote = "The confidence model did not answer, so this review has no confidence score"

// judge scores the review within the finalization context. changed is every
// path that the change touches, and the diff can show no line of such a path.
// unfinished is the summary's notes on the parts that went unreviewed. judge
// returns the note the summary states when the scorer gave no answer.
func (p *publishPhase) judge(job context.Context, res review.Result, diff string, changed, unfinished []string) string {
	ref := p.settings.Confidence.Model
	if ref == "" {
		return ""
	}
	defer p.timeStage("confidence")()
	ctx, cancel := context.WithTimeout(job, confidenceTimeout)
	defer cancel()
	// The agent is done with its slot on the review model. Scoring takes a
	// slot of its own. A job that holds one while it waits for another can
	// wait on a job that does the same the other way round.
	if p.lease != nil {
		p.w.releaseLease(ctx, p.logger, p.lease, string(p.settings.Models.Review))
	}
	c, err := p.score(ctx, ref, res, diff, changed, unfinished)
	if err != nil {
		p.logger.Warn("confidence not scored", "model", ref, "error", err)
		p.unscored = true
		return unscoredNote
	}
	p.logger.Info("confidence scored", "model", c.Model, "score", c.Score, "threshold", c.Threshold, "risk", c.Risk)
	p.confidence = &c
	return ""
}

// checkMonthCap is an error once the account spent its tokensPerMonth. The
// agent's admission made sure that the caps allowed a review before the agent
// ran. What the agent spent since then can reach the month's cap.
func (p *publishPhase) checkMonthCap(ctx context.Context) error {
	limit := p.settings.Limits.TokensPerMonth
	if limit <= 0 {
		return nil
	}
	u, err := p.w.Store.AccountUsage(ctx, p.account.ID())
	if err != nil {
		return fmt.Errorf("worker: read caps: %w", err)
	}
	if u.Tokens >= limit {
		return fmt.Errorf("worker: tokensPerMonth (%d) reached", limit)
	}
	return nil
}

// score asks ref for the score. It records the call against the review and
// charges it to the account.
func (p *publishPhase) score(
	ctx context.Context, ref configfile.ModelRef, res review.Result, diff string, changed, unfinished []string,
) (review.Confidence, error) {
	if err := p.checkMonthCap(ctx); err != nil {
		return review.Confidence{}, err
	}
	// Without a limit, a confidence model that hangs, or a long Retry-After,
	// will spend the score's whole time. With a fallback on another provider, its attempts
	// get at most half of what is left. An attempt that the model answered
	// without the score is billed all the same.
	call, err := p.structured(ref, p.settings.Confidence.Fallback, store.ModelCallConfidence, store.RoleConfidence,
		"confidence", true)
	if err != nil {
		return review.Confidence{}, err
	}
	body, err := p.pullBody(ctx)
	if err != nil {
		return review.Confidence{}, err
	}
	var rec review.Record
	if p.agent != nil {
		rec = review.Record{Sources: p.agent.Sources, CommandsRun: p.agent.CommandsRun, CommandErrors: p.agent.CommandErrors}
	}
	rec.Unfinished = unfinished
	system := review.ConfidenceSystemPrompt(p.settings.Confidence.Instructions)
	rubric := review.ConfidenceRubric(system)
	bumps := review.Bumps(diff)
	// The judge rates in its risk the versions its list leaves out, and
	// gives a reach to the ones it is shown alone, which are the ones a
	// last merged change can have been rated for.
	listed := review.ListedBumps(bumps)
	history, err := p.dependencyHistory(ctx, bumps, listed, rubric)
	if err != nil {
		// The history keeps the judge consistent and the judge rates
		// without it, so a read that fails costs the consistency, not the
		// score.
		p.logger.Warn("dependency history not read", "error", err)
	}
	req := model.CompletionRequest{
		System: system,
		User: review.BuildConfidence(review.Input{
			Repository: p.pr.repository, Number: p.pr.number, Title: p.pr.title, Author: p.pr.author, BaseRef: p.pr.baseRef,
			Body: body, Changed: changed, Diff: diff, Bumps: bumps, Dismissed: dismissedFindings(p.prior.dismissed),
		}, res, rec, history, earlierRisk(p.prior, p.pr.headSHA, rubric, p.trigger), system, confidenceBudget(p.settings.Agent.MaxPromptTokens)),
		Model: ref.Model(), Session: "confidence-" + p.reviewID, Effort: p.settings.Confidence.Effort,
		Schema: review.ConfidenceSchema(), SchemaName: "confidence", MaxTokens: confidenceMaxOutputTokens,
	}
	var resp model.CompletionResponse
	complete := func(ctx context.Context) error {
		var err error
		resp, err = call.Complete(ctx, req)
		return err
	}
	if err := p.w.withLease(ctx, p.account, string(ref), p.settings.Limits.Concurrency, p.jobID, complete); err != nil {
		return review.Confidence{}, err
	}
	parsed, err := review.ParseConfidence(resp.Raw, p.pr.repository, listed)
	if err != nil {
		return review.Confidence{}, err
	}
	for _, h := range history {
		if i := slices.IndexFunc(listed, func(b review.Bump) bool { return b.Key == h.Key }); i >= 0 && parsed.Reaches[i] != h.Reach {
			p.logger.Info("judge departed from the dependency's last reach",
				"path", h.Path, "line", h.Line, "was", h.Reach, "now", parsed.Reaches[i])
		}
	}
	// What the judge gave against what stands is the calibration signal: a
	// model that keeps giving no level fails the schema, one whose rating
	// the steps keep holding up reads a version step lighter than the rule.
	if parsed.Defaulted() {
		p.logger.Info("confidence risk given as no level", "model", resp.Model, "given", parsed.RiskGiven, "risk", parsed.Risk)
		p.w.Metrics.ConfidenceHeld(p.account.Key(), "risk")
	}
	c := review.Confidence{
		Threshold: p.settings.Confidence.Threshold, Reason: parsed.Reason, Risk: parsed.Risk, Model: resp.Model, Rubric: rubric,
		Bumps: review.BumpVerdicts(bumps, parsed.Reaches), OneBump: review.OneBump(diff),
	}
	if floor, at := review.StepFloor(listed, parsed.Reaches); c.HoldStep(floor, at) {
		p.logger.Info("risk held to the version step", "rated", parsed.Risk, "floor", floor, "step", at)
		p.w.Metrics.ConfidenceHeld(p.account.Key(), "step")
	}
	// The score rests on the risk the change is rated, the step's hold
	// included, and not on the instance's floor: a floor set or cleared
	// since moves a carried rating, never a carried score.
	c.Score = review.Score(res.Counts(), c.Risk, parsed.Concern, parsed.Reject)
	if floor, at := p.settings.Confidence.Floors.Floor(changed); c.Hold(floor, at) {
		p.logger.Info("risk held to the floor", "rated", parsed.Risk, "floor", floor, "path", at)
		p.w.Metrics.ConfidenceHeld(p.account.Key(), "floor")
	}
	return c, nil
}

// dependencyHistory is how the last merged change to each dependency that
// listed, the bumps the judge is shown of bumps, moves was rated under
// rubric, each with the bump of this change that moves the same
// dependency, in the order of listed. A dependency two bumps share a key
// for is not asked after, whether or not the list shows both: which of the
// two the history is of cannot be told.
func (p *publishPhase) dependencyHistory(ctx context.Context, bumps, listed []review.Bump, rubric string) ([]review.BumpHistory, error) {
	keys := uniqueKeys(bumps, len(listed))
	if len(keys) == 0 {
		return nil, nil
	}
	rows, err := p.w.Store.DependencyHistory(ctx, p.account.ID(), p.pr.repositoryID, p.pr.id, rubric, keys)
	if err != nil {
		return nil, err
	}
	return attachHistory(listed, rows), nil
}

// uniqueKeys is the keys of the first listed of bumps that no bump at all
// shares, in their order.
func uniqueKeys(bumps []review.Bump, listed int) []string {
	seen := make(map[string]int, len(bumps))
	for _, b := range bumps {
		seen[b.Key]++
	}
	var keys []string
	for _, b := range bumps[:listed] {
		if seen[b.Key] == 1 {
			keys = append(keys, b.Key)
		}
	}
	return keys
}

// attachHistory is rows, each at the bump of listed that moves its
// dependency, in the order of listed. A row that no bump matches is left
// out.
func attachHistory(listed []review.Bump, rows []review.BumpHistory) []review.BumpHistory {
	var out []review.BumpHistory
	for _, b := range listed {
		if i := slices.IndexFunc(rows, func(h review.BumpHistory) bool { return h.Key == b.Key }); i >= 0 {
			h := rows[i]
			h.Path, h.Line = b.Path, b.Line
			out = append(out, h)
		}
	}
	return out
}

// earlierRisk is the risk that prior was rated. A review of head is asked to
// keep that risk unless the change now does something that the rating does
// not account for. It is nil when prior was not scored. It is also nil when
// prior saw head itself or the review was asked for by hand, so that a re-run
// rates afresh. It is also nil when prior was rated under instructions other
// than rubric's, or was held to a floor. The floor applies again, and a floor
// lowered since then must not live on in the rating.
func earlierRisk(prior priorReview, head, rubric, trigger string) *review.EarlierRisk {
	c := prior.confidence
	if c == nil || prior.headSHA == head || trigger == jobs.TriggerManual || c.Rubric != rubric || c.Rated != "" {
		return nil
	}
	return &review.EarlierRisk{HeadSHA: prior.headSHA, Risk: c.Risk, Reason: c.Reason}
}

// verdict is the commit status of a published review with that many
// findings: a success saying so, with the confidence score where the
// repository asks for one. Where it also gates on the score, the score
// decides, and a review left unscored is an error rather than a pass.
func (p *publishPhase) verdict(findings int) (forge.StatusState, string) {
	desc := "no findings"
	if findings > 0 {
		desc = fmt.Sprintf("%d finding(s)", findings)
	}
	gate := p.settings.Confidence.Gate
	switch c := p.confidence; {
	case p.unfinished > 0:
		return forge.StatusError, "review incomplete, " + desc
	case p.unscored && gate:
		return forge.StatusError, "confidence not scored, " + desc
	case p.unscored:
		return forge.StatusSuccess, "confidence not scored, " + desc
	case c == nil:
		return forge.StatusSuccess, desc
	case c.Passed() || !gate:
		return forge.StatusSuccess, fmt.Sprintf("confidence %d/%d, %s", c.Score, review.MaxConfidence, desc)
	default:
		return forge.StatusFailure, fmt.Sprintf("confidence %d/%d, below %d, %s", c.Score, review.MaxConfidence, c.Threshold, desc)
	}
}

// carriedConfidence is the confidence that a patch unchanged since its last
// review keeps. That is the confidence that the pull request's newest
// completed review other than reviewID got. It is held to the threshold and
// the risk floors that want sets now. skippable says the patch can be skipped
// as unchanged. That is always so for a repository that asks for no score.
// Otherwise it is so only when that review left a score to carry. So a rebase
// neither passes a patch that failed nor leaves an unscored one unscored.
func carriedConfidence(
	ctx context.Context, tx pgx.Tx, prID, reviewID string, want configfile.Confidence,
) (c *review.Confidence, skippable bool, err error) {
	if want.Model == "" {
		return nil, true, nil
	}
	baseline, err := store.LastReviewBaseline(ctx, tx, prID, reviewID)
	c = baseline.Confidence
	if err != nil || c == nil {
		return nil, false, err
	}
	c.Threshold = want.Threshold
	c.Hold(want.Floors.Floor(baseline.Changed))
	return c, true, nil
}

// skipVerdict is the commit status of a review skipped for reason: a
// success saying so, with the score its unchanged patch carries, which
// decides as it did where the repository gates on it.
func skipVerdict(carried *review.Confidence, gate bool, reason string) (forge.StatusState, string) {
	desc := "skipped (" + reason + ")"
	switch {
	case carried == nil:
		return forge.StatusSuccess, desc
	case carried.Passed() || !gate:
		return forge.StatusSuccess, fmt.Sprintf("confidence %d/%d, %s", carried.Score, review.MaxConfidence, desc)
	default:
		return forge.StatusFailure, fmt.Sprintf("confidence %d/%d, below %d, %s", carried.Score, review.MaxConfidence, carried.Threshold, desc)
	}
}

// carryApproval applies the verdict that an unchanged patch carries to
// kritika's approval, where the repository has it approve. The skip reviewed
// nothing. But the threshold, the risk ceiling, or the approval's conditions
// can move after the score is given, and an approval must not outlive them.
// The head is the one that the skip was just decided for. scope is
// judgeApproval's word on the pull request as it stands.
func carryApproval(
	ctx context.Context, logger *slog.Logger, scope approvalScope, client forge.Client, pr *pullRequest,
	settings configfile.Settings, carried *review.Confidence,
) {
	if !settings.Review.Approve {
		return
	}
	// With no score carried the skip has no verdict to apply, unless the
	// approval it stands on is one kritika must no longer give.
	if carried == nil && scope != approvalOutOfScope && !settings.Confidence.RepoUnscored {
		return
	}
	p := &publishPhase{client: client, pr: pr, settings: settings, confidence: carried, logger: logger}
	p.approve(ctx, review.Counts{}, true, scope)
}
