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
	"github.com/home-operations/kritika/internal/runspec"
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

// budget is what a review in parts parts can spend through its grant: each
// part maxTokens, together within what the month leaves.
func (a admission) budget(parts int) int64 {
	total := int64(parts) * a.maxTokens
	if a.monthLeft > 0 {
		total = min(total, a.monthLeft)
	}
	return total
}

// agentAdmit settles what a review can spend before its runner starts,
// because the runner spends against the model through the gateway. A review
// model must be configured, and the account's caps must allow a review.
// agentAdmit takes a free model lease, renewed until released, or returns
// errNoSlot. A non-empty status ends the review before it runs, for the
// reason given.
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
// that a reviewer in settings that is on reviews with. settings is cut to the
// reviewers that the review can run (see mayRun). Which of them have files to
// read is known only once the runner has the diff. So a review holds one slot
// for each reviewer that can have files to read. A model with no free slot
// lets go of the slots taken and is a noSlot.
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

// agentCaps is the token budget that a review can spend and what the month's
// cap leaves, 0 when there is no such cap. If a cap stops the review, it is
// that cap instead.
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

// minAgentTokens is the least monthly headroom a review starts with: below it
// the agent cannot read the diff before running out.
const minAgentTokens = 50_000

// agentBudget is how many tokens one review can spend: the repository's agent
// budget, cut to what is left of the account's monthly cap when one is set. A
// non-empty reason caps the review instead, when too little is left for an
// agent to do anything with.
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

// ruleFiles is where a review discovers rule files, with every rule of
// rules, its when conditions holding or not, to leave a file to. It is nil
// for none, when dirs has no paths.
func ruleFiles(dirs configfile.RuleFiles, rules []configfile.Rule) *runspec.RuleFiles {
	if len(dirs.Paths) == 0 {
		return nil
	}
	out := &runspec.RuleFiles{Paths: dirs.Paths}
	for _, r := range rules {
		out.IDs = append(out.IDs, r.ID)
		if r.File != "" {
			out.Files = append(out.Files, r.File)
		}
	}
	return out
}

// agentPrompt reads what the runner needs to write the prompt and to
// recognize a review that the worker will skip. The runner needs the pull
// request as the repository filter sees it: that is pull, read once for the
// whole job. It also needs the issues that the pull request's description
// says it closes. For a bot author, it also needs the patch id of the pull
// request's last prepared review, which afterRun skips as unchanged. The notes
// say which issues agentPrompt failed to read.
func (w *Review) agentPrompt(
	ctx context.Context, accountID, reviewID, trigger string, pr *pullRequest, pull repoconfig.PullRequest, eff Effective,
	prior priorReview, client forge.Client, logger *slog.Logger,
) (*runspec.Prompt, []string, error) {
	p := &runspec.Prompt{
		Repository: pr.repository, Context: eff.Review.Context, PullRequest: pull,
		RequireSuggestedFix: eff.Review.RequireSuggestedFix, Diagram: eff.Review.Diagram,
		MaxDeltaFiles: eff.Incremental.MaxDeltaFiles, Prior: reviewFindings(prior.findings), Dismissed: dismissedFindings(prior.dismissed),
		PriorChecked: prior.checked,
	}
	if eff.Review.Diagram {
		p.PriorDiagram = prior.diagram
	}
	err := w.Store.WithAccount(ctx, accountID, func(tx pgx.Tx) error {
		// agentPrompt judges the rules' when conditions here, before the
		// runner cuts the body. So a rule applies as it will when the worker
		// reads the run.
		judged := p.PullRequest
		judged.Event = trigger
		vars, err := judged.Vars()
		if err != nil {
			return err
		}
		p.Rules = repoconfig.RulesFor(eff.Review.Rules, vars)
		if sk := eff.Skills; len(sk.Paths) > 0 {
			p.Skills = &runspec.Skills{Paths: sk.Paths, Scope: sk.Scope, Off: repoconfig.SkillsOff(sk.Scope, vars)}
		}
		p.RuleFiles = ruleFiles(eff.RuleFiles, eff.Review.Rules)
		// The runner owns the conditions that only the diff can judge. It
		// judges them with the review's trigger as the event. A review that
		// someone asked for passes the admin's lists, whatever they say.
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

// loadAgentRun reads the agent_runs row of a runner run. found is false when
// the runner wrote none.
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

// agentRun is what a runner's run came to. It holds the executor's result and
// the cause that supervision ended the run with, nil when supervision did not
// end it. It also holds the agent's row, or the reason that the read of the
// row failed.
type agentRun struct {
	res      executor.Result
	cause    error
	agent    *store.AgentRunRow
	agentErr error
	// unpaid is set when the run failed with no agent row and no model step
	// recorded: it spent nothing on the model, so it can run again.
	unpaid bool
}

// runRunner runs spec on exec, supervised as sup, until it ends, then revokes
// the run's gateway tokens and reads the agent's row. The row is read before
// the caller records the run, which settles its phase: a stopped run's row
// can still be on its way from the terminating pod.
func (b *Base) runRunner(
	ctx context.Context, exec executor.Executor, sup supervision, spec executor.Spec, accountID, runID string, ref configfile.ModelRef,
	rowWait time.Duration, logger *slog.Logger,
) agentRun {
	res, cause := supervise(ctx, sup, exec, spec)
	returned := time.Now()
	tokensRevoked := b.revokeGatewayTokens(ctx, logger, runID)
	revoked := time.Now()
	row, agentErr := b.readAgentRun(ctx, accountID, runID, ref, stopped(ctx, res, cause), rowWait)
	logger.Info("runner completion handled", "run", runID, "job_name", res.JobName,
		"revoke_elapsed", revoked.Sub(returned), "row_read_elapsed", time.Since(revoked))
	if row != nil && !row.CreatedAt.IsZero() {
		logger.Info("agent completion observed", "run", runID, "job_name", res.JobName,
			"agent_to_executor_return", max(0, returned.Sub(row.CreatedAt)), "elapsed", max(0, time.Since(row.CreatedAt)))
	}
	// With the run's tokens revoked, no step can begin after the read.
	unpaid := res.Err != nil && tokensRevoked && row == nil && agentErr == nil && b.modelUntouched(ctx, accountID, runID, logger)
	return agentRun{res: res, cause: cause, agent: row, agentErr: agentErr, unpaid: unpaid}
}

// modelUntouched reports whether no model step of the run began. A step is
// reserved before the provider is called. So a step in flight when the runner
// died counts, though its record can be on its way. A read that fails counts
// as a step: a retry on a guess can pay for the model twice. ctx's
// cancellation is not inherited, as readAgentRun's is not.
func (b *Base) modelUntouched(ctx context.Context, accountID, runID string, logger *slog.Logger) bool {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	var begun bool
	err := b.Store.WithAccount(ctx, accountID, func(tx pgx.Tx) error {
		var err error
		begun, err = store.ModelStepsBegun(ctx, tx, runID)
		return err
	})
	if err != nil {
		logger.Warn("model steps of the failed run not read; it counts as paid for", "run", runID, "error", err)
		return false
	}
	return !begun
}

// readAgentRun reads the run's agent_runs row, nil when the runner wrote
// none. What the agent spent is already charged: the gateway records usage
// for every step it serves, whatever becomes of the review. A run no step of
// which was answered names ref's model, the one it was granted.
//
// A run that ended in error can still be writing its row: a deleted runner
// pod records how its agent stopped while it terminates. await waits for
// that, until the run settles or wait, agentRowWait when zero, passes. ctx's
// cancellation is not inherited, so a job River cancels still reads the row.
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

// pinModels is models as a run's grant names them. Each floating alias is
// pinned to the model that it selects now (adapter.Steppers.Pin). So every
// step of the run goes to that model, whichever replica's gateway serves it,
// and a conversation that the run keeps names that model. An alias that
// cannot be pinned is granted as it is, and each step then resolves it.
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

// agentSpec gives spec its agent. The agent gets the prompt, with the
// reviewers it runs, the gateway, and a run token for the gateway. It also
// gets its bounds, for a review sized for size's parts. The token grants
// models and the grants of each other reviewer, which lets the agent carry on
// cont when cont is set. The token is minted last, so an error leaves none
// behind. The caller revokes it once the run ends. agentSpec returns the
// runner Job's deadline, which the agent's timeout can lengthen, and the
// prompt's notes.
func (w *Review) agentSpec(
	ctx context.Context, accountID, reviewID, runID, trigger, focus string, pr *pullRequest, eff Effective, models configfile.Models,
	prior priorReview, cont *runspec.Continuation, admitted admission, size sizing, spec *runspec.Spec, secrets *runspec.Secrets,
	deadline time.Duration, client forge.Client, logger *slog.Logger, pull repoconfig.PullRequest, chosen reviewers,
	grants map[string]store.ReviewerGrant,
) (time.Duration, []string, error) {
	settings := eff.Settings
	prompt, notes, err := w.agentPrompt(ctx, accountID, reviewID, trigger, pr, pull, eff, prior, client, logger)
	if err != nil {
		return deadline, nil, err
	}
	prompt.Reviewers, prompt.Focus = chosen.specs, focus
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
	spec.Model = &runspec.ModelEndpoint{GatewayURL: w.GatewayURL, Model: gateway.ModelName, Granted: grant.Model}
	spec.Agent = &runspec.AgentLimits{
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

// partsTimeout is the agent time that a review needs if its parts run in
// rounds rounds, each part within timeout. It is at most as much as a job can
// run.
func partsTimeout(rounds int, timeout time.Duration) time.Duration {
	return min(time.Duration(rounds)*timeout, jobtimeout.MaxAgentTimeout)
}

// sizing is what a review is sized for: the parts that its runner can split
// it into, and the slots that it holds on the review model. Its parts can run
// in those slots at once.
type sizing struct {
	parts, slots int
}

// rounds is how many parts run one after another at the most.
func (s sizing) rounds() int { return (s.parts + s.slots - 1) / s.slots }

// revokeGatewayTokens ends the run's token once its runner is done. It uses a
// context of its own, because the job's context can already be over. A token
// that is not revoked still expires on its own.
func (b *Base) revokeGatewayTokens(ctx context.Context, logger *slog.Logger, runID string) bool {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), releaseTimeout)
	defer cancel()
	if err := b.Store.RevokeGatewayTokens(rctx, runID); err != nil {
		logger.Warn("gateway token not revoked", "error", err)
		return false
	}
	return true
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
