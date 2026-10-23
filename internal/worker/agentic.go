package worker

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/adapter"
	"github.com/home-operations/kritika/internal/agent"
	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/executor"
	"github.com/home-operations/kritika/internal/forge"
	"github.com/home-operations/kritika/internal/gateway"
	"github.com/home-operations/kritika/internal/jobs"
	"github.com/home-operations/kritika/internal/jobtimeout"
	"github.com/home-operations/kritika/internal/repoconfig"
	"github.com/home-operations/kritika/internal/review"
	"github.com/home-operations/kritika/internal/runner"
	"github.com/home-operations/kritika/internal/store"
)

// agentDeadline bounds a review's runner Job: the account's runner deadline,
// unless the agent's timeout plus the fetch headroom needs longer.
func agentDeadline(runnerDeadline, agentTimeout time.Duration) time.Duration {
	return max(runnerDeadline, agentTimeout+jobtimeout.AgentFetchHeadroom)
}

// stopError is nil for a run that submitted a review, and otherwise the
// review's error.
func stopError(r store.AgentRunRow) error {
	stop := agent.StopReason(r.StopReason)
	switch {
	case stop == agent.StopSubmitted && len(r.Result) > 0:
		return nil
	case stop == agent.StopSubmitted:
		return errors.New("agent stopped: submitted without a result")
	case r.Error != "":
		return fmt.Errorf("agent stopped: %s: %s", stop, r.Error)
	}
	return fmt.Errorf("agent stopped: %s", stop)
}

// admission is what a review holds before its runner starts.
type admission struct {
	lease *store.Lease
	// reviewerLeases are its slots on the models of its other reviewers
	// that are not the review model, by model.
	reviewerLeases map[string]*store.Lease
	// maxTokens is the agent's token budget for this review, or for each
	// part of a split one.
	maxTokens int64
	// monthLeft is what the account's monthly cap leaves, 0 when it sets
	// none.
	monthLeft int64
}

// budget is what a review in parts parts may spend through its grant: each
// part maxTokens, together within what the month leaves.
func (a admission) budget(parts int) int64 {
	total := int64(parts) * a.maxTokens
	if a.monthLeft > 0 {
		total = min(total, a.monthLeft)
	}
	return total
}

// agentAdmit settles what a review may spend before its runner
// starts, since the runner spends against the model through the gateway:
// a review model must be configured, the account's caps must allow a
// review, and a free model lease is taken, renewed until released, or
// errNoSlot returned. A non-empty status ends the review
// before it runs, for the reason given.
func (w *Review) agentAdmit(
	ctx context.Context, logger *slog.Logger, file *configfile.File, account *configfile.Account, settings configfile.Settings, jobID int64,
) (admission, store.ReviewStatus, string, error) {
	ref := settings.Models.Review
	if ref == "" {
		return admission{}, store.ReviewSkipped, "no review model is configured for this repository", nil
	}
	if _, ok := file.Provider(account, ref.Provider()); !ok {
		return admission{}, store.ReviewFailed, fmt.Sprintf("provider %q is not in the configuration", ref.Provider()), nil
	}
	l, err := w.Store.TakeLease(ctx, account.ID(), string(ref), settings.Limits.Concurrency, jobID)
	if err != nil {
		return admission{}, "", "", err
	}
	if l == nil {
		return admission{}, "", "", errNoSlot
	}
	a := admission{lease: l}
	if a.reviewerLeases, err = w.reviewerLeases(ctx, logger, account, settings, jobID); err != nil {
		w.releaseLease(ctx, logger, l, string(ref))
		return admission{}, "", "", err
	}
	// The caps are read under the lease, so concurrent reviews cannot all
	// pass a cap of one.
	budget, left, capped, err := w.agentCaps(ctx, account, settings)
	if err != nil || capped != "" {
		w.release(ctx, logger, a, ref)
		if err != nil {
			return admission{}, "", "", err
		}
		return admission{}, store.ReviewCapped, capped, nil
	}
	a.maxTokens, a.monthLeft = budget, left
	return a, "", "", nil
}

// release lets go of every slot a holds, ref being the review model.
func (w *Review) release(ctx context.Context, logger *slog.Logger, a admission, ref configfile.ModelRef) {
	if a.lease != nil {
		w.releaseLease(ctx, logger, a.lease, string(ref))
	}
	for model, l := range a.reviewerLeases {
		w.releaseLease(ctx, logger, l, model)
	}
}

// noSlot is errNoSlot for the model whose every slot is held.
type noSlot struct{ model string }

func (e noSlot) Error() string        { return errNoSlot.Error() + ": " + e.model }
func (e noSlot) Is(target error) bool { return target == errNoSlot }

// reviewerLeases takes a slot on each model, other than the review model,
// that one of settings' reviewers that is on reviews with, settings being
// cut to those the review may run (see mayRun): which of them have files
// to read is known only once the runner has the diff, so a review holds
// one for each that may. A model with no free slot lets go of those taken
// and is a noSlot.
func (w *Review) reviewerLeases(
	ctx context.Context, logger *slog.Logger, account *configfile.Account, settings configfile.Settings, jobID int64,
) (map[string]*store.Lease, error) {
	var out map[string]*store.Lease
	for _, model := range reviewerModelKeys(settings) {
		l, err := w.Store.TakeLease(ctx, account.ID(), model, settings.Limits.Concurrency, jobID)
		if err == nil && l == nil {
			err = noSlot{model: model}
		}
		if err != nil {
			for m, held := range out {
				w.releaseLease(ctx, logger, held, m)
			}
			return nil, err
		}
		if out == nil {
			out = map[string]*store.Lease{}
		}
		out[model] = l
	}
	return out, nil
}

// reviewerModelKeys are the models, other than the review model, the
// repository's reviewers that are on review with, sorted.
func reviewerModelKeys(settings configfile.Settings) []string {
	var out []string
	for name, r := range settings.Reviewers {
		if name == review.DefaultReviewer || !r.On() {
			continue
		}
		if m := string(r.Models(settings.Models).Review); m != string(settings.Models.Review) && !slices.Contains(out, m) {
			out = append(out, m)
		}
	}
	slices.Sort(out)
	return out
}

// agentCaps is the token budget a review may spend and what the month's
// cap leaves, 0 when there is none, or the cap that stops it.
func (w *Review) agentCaps(
	ctx context.Context, account *configfile.Account, settings configfile.Settings,
) (budget, left int64, capped string, err error) {
	limits := settings.Limits
	if limits.ReviewsPerDay <= 0 && limits.TokensPerMonth <= 0 {
		return settings.Agent.MaxTokens, 0, "", nil
	}
	u, err := w.Store.AccountUsage(ctx, account.ID())
	if err != nil {
		return 0, 0, "", fmt.Errorf("worker: read caps: %w", err)
	}
	if capped := store.CapReason(u, limits); capped != "" {
		return 0, 0, capped, nil
	}
	if limits.TokensPerMonth > 0 {
		left = limits.TokensPerMonth - u.Tokens
	}
	budget, capped = agentBudget(settings.Agent.MaxTokens, limits.TokensPerMonth, u.Tokens)
	return budget, left, capped, nil
}

// minAgentTokens is the least monthly headroom a review starts
// with: below it the agent could not read the diff before running out.
const minAgentTokens = 50_000

// agentBudget is how many tokens one review may spend: the
// repository's agent budget, cut to what is left of the account's monthly
// cap when one is set. A non-empty reason caps the review instead, when
// too little is left for an agent to do anything with.
func agentBudget(agentMax, tokensPerMonth, usedThisMonth int64) (int64, string) {
	if tokensPerMonth <= 0 {
		return agentMax, ""
	}
	left := tokensPerMonth - usedThisMonth
	if left <= minAgentTokens {
		return 0, fmt.Sprintf("tokensPerMonth (%d) nearly reached: %d tokens left", tokensPerMonth, max(left, 0))
	}
	return min(agentMax, left), ""
}

// agentPrompt reads what the runner needs to write the prompt and to tell
// a review the worker will skip: the pull request as the repository filter
// sees it, pull, read once for the whole job, the issues its description says it closes, and, for a bot
// author, the patch id of its last prepared review, which afterRun skips as
// unchanged. The notes say which issues could not be read.
func (w *Review) agentPrompt(
	ctx context.Context, accountID, reviewID, trigger string, pr *pullRequest, pull repoconfig.PullRequest, eff Effective,
	prior priorReview, client forge.Client, logger *slog.Logger,
) (*runner.Prompt, []string, error) {
	p := &runner.Prompt{
		Repository: pr.repository, Context: eff.Review.Context, PullRequest: pull,
		RequireSuggestedFix: eff.Review.RequireSuggestedFix, Diagram: eff.Review.Diagram,
		MaxDeltaFiles: eff.Incremental.MaxDeltaFiles, Prior: reviewFindings(prior.findings), Dismissed: dismissedFindings(prior.dismissed),
		PriorChecked: prior.checked,
	}
	if eff.Review.Diagram {
		p.PriorDiagram = prior.diagram
	}
	err := w.Store.WithAccount(ctx, accountID, func(tx pgx.Tx) error {
		// The rules' when conditions are judged here, before the runner cuts the
		// body, so a rule applies as it will when the worker reads the run.
		judged := p.PullRequest
		judged.Event = trigger
		vars, err := judged.Vars()
		if err != nil {
			return err
		}
		p.Rules = repoconfig.RulesFor(eff.Review.Rules, vars)
		if sk := eff.Skills; len(sk.Paths) > 0 {
			p.Skills = &runner.Skills{Paths: sk.Paths, Scope: sk.Scope, Off: repoconfig.SkillsOff(sk.Scope, vars)}
		}
		// The conditions only the diff can judge are the runner's; it
		// judges them as the review's trigger, and a review someone asked
		// for passes the admin's lists whatever they say.
		p.PullRequest.Event = trigger
		if trigger != jobs.TriggerManual && eff.Filters.NeedsDiff() {
			p.Filters = append(p.Filters, eff.Filters)
		}
		if eff.InRepoFilters.NeedsDiff() {
			p.Filters = append(p.Filters, eff.InRepoFilters)
		}
		if !pr.dedupesBotPatch(trigger) {
			return nil
		}
		// A review someone asked for that this one's start canceled is
		// owed: this one stands in for it, so its patch is not deduped.
		if owed, err := store.ReviewOwed(ctx, tx, pr.id); err != nil || owed {
			return err
		}
		// A patch whose last review left no score to carry is reviewed
		// again where a score is asked for.
		baseline, err := store.LastReviewBaseline(ctx, tx, pr.id, reviewID)
		if err != nil || (eff.Confidence.Model != "" && baseline.Confidence == nil) {
			return err
		}
		p.UnchangedPatchID = baseline.PatchID
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	owner, repo := pr.ownerRepo()
	var notes []string
	p.Issues, notes = linkedIssues(ctx, client, owner, repo, p.PullRequest.Body, logger)
	p.Trim()
	return p, notes, nil
}

// loadAgentRun reads the agent_runs row of a runner run; found is false
// when the runner wrote none.
func (b *Base) loadAgentRun(ctx context.Context, accountID, runID string) (run store.AgentRunRow, found bool, err error) {
	err = b.Store.WithAccount(ctx, accountID, func(tx pgx.Tx) error {
		var err error
		run, err = store.FindAgentRun(ctx, tx, runID)
		return err
	})
	if errors.Is(err, store.ErrNotFound) {
		return store.AgentRunRow{}, false, nil
	}
	if err != nil {
		return store.AgentRunRow{}, false, fmt.Errorf("worker: read agent run: %w", err)
	}
	return run, true, nil
}

// agentRun is what a runner's run came to: the executor's result, the
// cause supervision ended it with, nil when it did not, and the agent's
// row, or why it could not be read.
type agentRun struct {
	res      executor.Result
	cause    error
	agent    *store.AgentRunRow
	agentErr error
}

// runRunner runs spec on exec, supervised as sup, until it ends, then
// revokes the run's gateway tokens and reads the agent's row. The row is
// read before the caller records the run, which settles its phase: a
// stopped run's row may still be on its way from the terminating pod.
func (b *Base) runRunner(
	ctx context.Context, exec executor.Executor, sup supervision, spec executor.Spec, accountID, runID string, ref configfile.ModelRef,
	rowWait time.Duration, logger *slog.Logger,
) agentRun {
	res, cause := supervise(ctx, sup, exec, spec)
	returned := time.Now()
	b.revokeGatewayTokens(ctx, logger, runID)
	revoked := time.Now()
	row, agentErr := b.readAgentRun(ctx, accountID, runID, ref, stopped(ctx, res, cause), rowWait)
	logger.Info("runner completion handled", "run", runID, "job_name", res.JobName,
		"revoke_elapsed", revoked.Sub(returned), "row_read_elapsed", time.Since(revoked))
	if row != nil && !row.CreatedAt.IsZero() {
		logger.Info("agent completion observed", "run", runID, "job_name", res.JobName,
			"agent_to_executor_return", max(0, returned.Sub(row.CreatedAt)), "elapsed", max(0, time.Since(row.CreatedAt)))
	}
	return agentRun{res: res, cause: cause, agent: row, agentErr: agentErr}
}

// readAgentRun reads the run's agent_runs row, nil when the runner wrote
// none. What the agent spent is already charged: the gateway records usage
// for every step it serves, whatever becomes of the review. A run no step
// of which was answered names ref's model, the one it was granted.
//
// A run that ended in error may still be writing its row: a deleted runner
// pod records how its agent stopped while it terminates. await waits for
// that, until the run settles or wait, agentRowWait when zero, passes.
// ctx's cancellation is not inherited, so a job River cancels still reads
// the row.
func (b *Base) readAgentRun(
	ctx context.Context, accountID, runID string, ref configfile.ModelRef, await bool, wait time.Duration,
) (*store.AgentRunRow, error) {
	wait = cmp.Or(wait, agentRowWait)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), wait+10*time.Second)
	defer cancel()
	run, found, err := b.loadAgentRun(ctx, accountID, runID)
	if err == nil && !found && await {
		run, found, err = b.awaitAgentRun(ctx, accountID, runID, wait)
	}
	if err != nil || !found {
		return nil, err
	}
	run.Model = cmp.Or(run.Model, ref.Model())
	return &run, nil
}

// pinModels is models as a run's grant names them: each floating alias
// pinned to the model it selects now (adapter.Steppers.Pin), so every step
// of the run goes to that model, whichever replica's gateway serves it,
// and a conversation the run keeps names it. An alias that cannot be
// pinned is granted as it is, which each step then resolves.
func pinModels(
	ctx context.Context, logger *slog.Logger, steppers *adapter.Steppers, file *configfile.File, account *configfile.Account,
	models configfile.Models,
) configfile.Models {
	for _, ref := range []*configfile.ModelRef{&models.Review, &models.Fallback} {
		pinned, err := steppers.Pin(ctx, file, account, *ref, models.Effort)
		if err != nil {
			provider, _ := file.Provider(account, ref.Provider())
			logger.Warn("floating alias not pinned; each step resolves it", "model", *ref,
				"error", adapter.Mask(file, provider)(err.Error()))
			continue
		}
		*ref = pinned
	}
	return models
}

// agentSpec gives spec its agent: the prompt, with the reviewers it runs,
// the gateway and a run token for it, granting models and each other
// reviewer's grants, which lets it carry on cont when that is set, and the
// agent's bounds, for a review sized for size's parts. The
// token is minted last, so an error leaves none behind; the caller
// revokes it once the run ends. It returns the runner Job's deadline,
// which the agent's timeout may lengthen, and the prompt's notes.
func (w *Review) agentSpec(
	ctx context.Context, accountID, reviewID, runID, trigger string, pr *pullRequest, eff Effective, models configfile.Models,
	prior priorReview, cont *runner.Continuation, admitted admission, size sizing, spec *runner.Spec, secrets *runner.Secrets,
	deadline time.Duration, client forge.Client, logger *slog.Logger, pull repoconfig.PullRequest, chosen reviewers,
	grants map[string]store.ReviewerGrant,
) (time.Duration, []string, error) {
	settings := eff.Settings
	prompt, notes, err := w.agentPrompt(ctx, accountID, reviewID, trigger, pr, pull, eff, prior, client, logger)
	if err != nil {
		return deadline, nil, err
	}
	prompt.Reviewers = chosen.specs
	deadline = agentDeadline(deadline, partsTimeout(size.rounds(), settings.Agent.Timeout))
	grant := store.GatewayGrant{
		RunID: runID, AccountID: accountID, ReviewID: reviewID, RepositoryID: pr.repositoryID,
		Model: string(models.Review), Fallback: string(models.Fallback), Effort: string(models.Effort),
		Budget: admitted.budget(size.parts), Reviewers: grants,
	}
	if cont != nil {
		prompt.Continue, grant.Continues, grant.Session = cont, cont.RunID, cont.Session
	}
	token, err := w.Store.MintGatewayToken(ctx, grant, time.Now().Add(deadline+w.GatewayTokenTTL))
	if err != nil {
		return deadline, nil, err
	}
	spec.Prompt, secrets.GatewayToken = prompt, token
	spec.Model = &runner.ModelEndpoint{GatewayURL: w.GatewayURL, Model: gateway.ModelName, Granted: grant.Model}
	spec.Agent = &runner.AgentLimits{
		MaxSteps: settings.Agent.MaxSteps, MaxToolOutputBytes: settings.Agent.MaxToolOutputBytes, MaxTokens: admitted.maxTokens,
		MaxPromptTokens: settings.Agent.MaxPromptTokens, Parts: size.parts, Parallel: size.slots,
		TimeoutSeconds:        int(settings.Agent.Timeout / time.Second),
		CommandTimeoutSeconds: int(settings.Agent.CommandTimeout / time.Second), Fetches: settings.Agent.MaxFetches,
	}
	return deadline, notes, nil
}

// diffBytesPerLine and diffBytesPerFile estimate a unified diff's size
// from the forge's counts: a changed line with its share of the context
// around it, and a file's header.
const (
	diffBytesPerLine = 64
	diffBytesPerFile = 256
)

// estimateParts is how many parts the diff of a pull request of pr's size
// is likely split into, at most maxParts.
func estimateParts(pr forge.OpenPullRequest, maxParts int) int {
	size := diffBytesPerLine*(pr.Additions+pr.Deletions) + diffBytesPerFile*pr.ChangedFiles
	return min(max((size+review.PartBytes-1)/review.PartBytes, 1), max(maxParts, 1))
}

// splitParts is how many parts a review of pr is sized for, its grant and
// its deadline: estimated from the size the forge reports, since only the
// runner sees the diff, which it splits within that. A review is not split
// when agent.parts is 1 or the size cannot be read.
func (w *Review) splitParts(ctx context.Context, client forge.Client, pr *pullRequest, maxParts int, logger *slog.Logger) int {
	if maxParts < 2 {
		return 1
	}
	owner, repo := pr.ownerRepo()
	size, err := client.PullRequest(ctx, owner, repo, pr.number)
	if err != nil {
		logger.Warn("pull request size not read; the review is not split", "error", err)
		return 1
	}
	return estimateParts(size, maxParts)
}

// partsTimeout is the agent time a review needs whose parts run in rounds
// rounds, each part within timeout, as much of it as a job may run.
func partsTimeout(rounds int, timeout time.Duration) time.Duration {
	return min(time.Duration(rounds)*timeout, jobtimeout.MaxAgentTimeout)
}

// sizing is what a review is sized for: the parts its runner may split it
// into, and the review model's slots it holds, which its parts may run in
// at once.
type sizing struct {
	parts, slots int
}

// rounds is how many parts run one after another at the most.
func (s sizing) rounds() int { return (s.parts + s.slots - 1) / s.slots }

// revokeGatewayTokens ends the run's token once its runner is done, on a
// context of its own since the job's may have ended. A token not revoked
// still expires on its own.
func (b *Base) revokeGatewayTokens(ctx context.Context, logger *slog.Logger, runID string) {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), releaseTimeout)
	defer cancel()
	if err := b.Store.RevokeGatewayTokens(rctx, runID); err != nil {
		logger.Warn("gateway token not revoked", "error", err)
	}
}

// stopped reports whether a run was stopped from outside, by supervision,
// the job's context or the Job's deadline, rather than ending on its own.
func stopped(ctx context.Context, res executor.Result, cause error) bool {
	return res.Err != nil && (cause != nil || ctx.Err() != nil || res.DeadlineExceeded)
}

// agentRowWait is how long a failed run's agent row is waited for: a
// runner pod's termination grace period.
const agentRowWait = 30 * time.Second

// agentRowPoll is how often awaitAgentRun looks again.
const agentRowPoll = time.Second

// awaitAgentRun polls for a run's agent_runs row until it appears, the
// run's phase settles without one, or wait passes.
func (b *Base) awaitAgentRun(ctx context.Context, accountID, runID string, wait time.Duration) (store.AgentRunRow, bool, error) {
	deadline := time.After(wait)
	for {
		var phase string
		err := b.Store.WithAccount(ctx, accountID, func(tx pgx.Tx) error {
			var err error
			phase, err = store.RunnerPhase(ctx, tx, runID)
			return err
		})
		if err != nil {
			return store.AgentRunRow{}, false, fmt.Errorf("worker: read run phase: %w", err)
		}
		// The runner writes its agent row before it settles the phase.
		settled := phase == "done" || phase == "failed"
		run, found, err := b.loadAgentRun(ctx, accountID, runID)
		if err != nil || found || settled {
			return run, found, err
		}
		select {
		case <-deadline:
			return store.AgentRunRow{}, false, nil
		case <-ctx.Done():
			return store.AgentRunRow{}, false, nil
		case <-time.After(agentRowPoll):
		}
	}
}
