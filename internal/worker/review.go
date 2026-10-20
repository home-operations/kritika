// Package worker consumes River jobs. The review worker owns a review from
// the moment its job starts until the runner answer is checkpointed: it
// checks the head, spawns a runner, and hands preparation and delivery to
// their own durable jobs.
package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/home-operations/kritika/internal/adapter"
	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/executor"
	"github.com/home-operations/kritika/internal/forge"
	"github.com/home-operations/kritika/internal/gitfetch"
	"github.com/home-operations/kritika/internal/jobs"
	"github.com/home-operations/kritika/internal/review"
	"github.com/home-operations/kritika/internal/runner"
	"github.com/home-operations/kritika/internal/store"
)

// Review works the review queue.
type Review struct {
	river.WorkerDefaults[jobs.ReviewArgs]
	Base
	Executor executor.Executor
	// Steppers reach the model that scores a review's confidence, and the
	// review model that writes a split review's summary from its parts';
	// they also pin the models a run is granted (pinModels).
	Steppers *adapter.Steppers
	// GatewayURL is where a runner calls its model, and GatewayTokenTTL how
	// long its run token outlives the Job's deadline.
	GatewayURL      string
	GatewayTokenTTL time.Duration
	// WebURL is the dashboard's origin, which the summary comment links
	// back to for a re-run; nil leaves the link out.
	WebURL *url.URL

	// superviseEvery overrides superviseInterval, and rowWait agentRowWait.
	superviseEvery time.Duration
	rowWait        time.Duration
}

// pullRequest is what the worker reads back before starting.
type pullRequest struct {
	id, repositoryID string
	repository       string
	number           int
	headSHA, baseRef string
	title, author    string
	authorIsBot      bool
	// closed is why it takes no more reviews, "" while it is open.
	closed string
}

// dedupesBotPatch says whether an unchanged patch from a bot author skips
// the review. A manual re-run bypasses the skip: the human asked for it, so
// an identical bot patch is reviewed again rather than deduped away.
func (pr *pullRequest) dedupesBotPatch(trigger string) bool {
	return pr.authorIsBot && trigger != jobs.TriggerManual
}

// ownerRepo splits the repository's full name as the forge client takes it.
func (pr *pullRequest) ownerRepo() (owner, repo string) {
	owner, repo, _ = strings.Cut(pr.repository, "/")
	return owner, repo
}

// Work implements river.Worker.
func (w *Review) Work(ctx context.Context, job *river.Job[jobs.ReviewArgs]) (err error) {
	args := job.Args
	file := w.Current.Get()
	logger := w.Logger.With("account", args.AccountID, "pr", args.Number, "head", review.ShortSHA(args.HeadSHA))
	started := time.Now()
	// River refunds shutdown attempts, so a checkpoint may exist on attempt one.
	if resumed, err := w.resumePublication(ctx, job, file, logger); resumed {
		return err
	}
	account, err := w.account(file, args.AccountID)
	if err != nil {
		return err
	}
	logger = w.Logger.With("account", account.Key(), "pr", args.Number, "head", review.ShortSHA(args.HeadSHA))
	w.recordSlotWait(logger, job, account.Key(), started)
	clearSlotWait(ctx, logger, job)

	b, done, err := w.begin(ctx, job, file, account, logger, started)
	if done {
		return err
	}
	pr, client, owner, repo, mergeBase, eff := b.early.pr, b.client, b.owner, b.repo, b.early.mergeBase, b.eff
	settings := eff.Settings
	admitted, done, err := w.admit(ctx, b.early, job, file, account, settings)
	if done {
		return err
	}
	if admitted.lease != nil {
		defer w.releaseLease(ctx, logger, admitted.lease, string(settings.Models.Review))
	}

	// The status says the review is under way until its outcome replaces
	// it, so a head is never silent while its runner works. It is set under
	// the lock the start holds, so it never lands over the result of a
	// newer review, which cannot start until it is set.
	s, prior, err := w.start(ctx, args, pr, mergeBase, b.early.forgePatch, job.ID, func(ctx context.Context) {
		if err := client.SetStatus(ctx, owner, repo, args.HeadSHA, forge.StatusPending, "kritika: review running"); err != nil {
			logger.Warn("commit status not set", "error", err)
		}
	})
	switch {
	case errors.Is(err, store.ErrPublicationBusy):
		logger.Info("review snoozed: a review of the pull request is being published", "for", publishWait)
		return river.JobSnooze(publishWait)
	case errors.Is(err, store.ErrHeadMoved):
		logger.Info("review superseded at start: the head moved")
		return w.endHeadMoved(ctx, b.early, job)
	case errors.Is(err, store.ErrReviewReplaced):
		return river.JobCancel(err)
	case err != nil:
		return err
	}
	reviewID, runID := s.ReviewID, s.RunID
	if len(s.Replaced) > 0 {
		logger.Info("older reviews of the pull request canceled: this review replaces them", "replaced", s.Replaced)
	}
	ended := endedReview{
		accountID: args.AccountID, accountKey: account.Key(), reviewID: reviewID, prID: pr.id, headSHA: args.HeadSHA,
		owner: owner, repo: repo, client: client, started: started, logger: logger,
	}
	publicationHandled := false
	// An error on the last attempt has River discard the job, so nothing
	// would replace the pending status.
	defer func() {
		if publicationHandled || !reviewAttemptFailed(context.Cause(ctx), err, job.Attempt >= job.MaxAttempts) {
			return
		}
		sctx, cancel := detach(ctx)
		defer cancel()
		w.reportEnd(sctx, ended, reviewFailedDescription)
	}()
	// The pull request carries the bot's eyes while a review of it runs,
	// then its thumbs up once one is posted or its confused face when one
	// failed. An error on the last attempt leaves the review as it is, so
	// it counts as failed here as the commit status says.
	end := pullMarks(client, owner, repo, args.Number, logger).start(ctx)
	defer func() {
		if publicationHandled {
			return
		}
		mctx, cancel := detach(ctx)
		defer cancel()
		end(w.reviewOutcome(mctx, logger, args.AccountID, reviewID, err,
			reviewAttemptFailed(context.Cause(ctx), err, job.Attempt >= job.MaxAttempts)),
			w.otherReviewRunning(mctx, logger, args.AccountID, pr.id, reviewID))
	}()
	deadline := file.RunnerDeadline()
	spec := runner.Spec{
		Version: runner.SpecVersion, Kind: runner.KindReview, RunID: runID, CloneURL: client.CloneURL(owner, repo),
		Head: args.HeadSHA, Base: mergeBase, PriorHead: prior.headSHA, PriorChanged: prior.changed, PriorBase: prior.mergeBase,
		Ignore: settings.Ignore, RepoFiles: eff.repoFiles(),
	}
	secrets := runner.Secrets{GitToken: b.token}
	secrets.DatabaseToken, err = w.Store.MintRunnerToken(ctx, args.AccountID, runID)
	if err != nil {
		return w.agentSpecFailed(ctx, ended, runID, err)
	}
	provider, _ := file.Provider(account, settings.Models.Review.Provider())
	granted := pinModels(ctx, logger, w.Steppers, file, account, settings.Models)
	spec.CheckpointScope = fmt.Sprintf("%s/%s/%s", file.Hash(), granted.Effort, granted.Fallback)
	cont := w.continuation(ctx, logger, args.AccountID, prior, args.HeadSHA, mergeBase, granted.Review, provider.Type)
	parts := w.splitParts(ctx, client, pr, settings.Agent.MaxParts, logger)
	slots := &reviewSlots{worker: w, logger: logger, accountID: account.ID(), runID: runID,
		model: string(settings.Models.Review), limit: settings.Limits.Concurrency, maxParts: parts, jobID: job.ID}
	defer slots.close(ctx)
	if err := slots.resize(ctx, parts); err != nil {
		return w.agentSpecFailed(ctx, ended, runID, err)
	}
	extra := len(slots.leases)
	logger.Info("review model slots granted", "estimated_parts", parts, "slots", 1+extra, "limit", settings.Limits.Concurrency)
	deadline, promptNotes, err := w.agentSpec(ctx, args.AccountID, reviewID, runID, args.Trigger, pr, eff, granted, prior, cont, admitted,
		sizing{parts: parts, slots: 1 + extra}, &spec, &secrets, deadline, client, logger)
	if err != nil {
		return w.agentSpecFailed(ctx, ended, runID, err)
	}
	spec.Agent.Hosts = file.EgressHosts()
	spec.Agent.CacheLocked = file.GatewayCaches()
	b.notes = append(b.notes, promptNotes...)
	sup := runSupervision(w.Store, args.AccountID, runID, pr.id, reviewID, args.HeadSHA, w.superviseEvery, logger)
	stopSlots := slots.watch(ctx)
	defer stopSlots()
	out := w.runRunner(ctx, w.Executor, sup, executor.Spec{
		Labels:      runnerLabels(account.Key(), pr.repository, jobs.QueueReview, args.Number),
		Annotations: runnerAnnotations(job.ID, args.HeadSHA),
		Job:         spec,
		Secrets:     secrets,
		Deadline:    deadline,
	}, args.AccountID, runID, settings.Models.Review, w.rowWait, logger)
	// The parts are done with the slots they ran in.
	stopSlots()
	slots.close(ctx)
	res, cause, agentOutcome, agentErr := out.res, out.cause, out.agent, out.agentErr
	cctx, cancel := detach(ctx)
	defer cancel()
	ended.jobName = res.JobName
	if err := recordRun(cctx, w.Store, w.Metrics, account.Key(), args.AccountID, runID, jobs.QueueReview, res); err != nil {
		logger.Error("runner run not recorded", "error", err)
		return w.finishEnded(ctx, ended, err)
	}
	if judged, err := w.judgeRun(ctx, cctx, job, ended, res, cause, agentErr); judged {
		return err
	}
	pctx, stopPreparation := preparationContext(ctx)
	defer stopPreparation()
	prep, status, err := w.afterRun(pctx, job, account, pr, eff, b.notes, client, reviewID, runID, prior,
		spec.Prompt.PullRequest.Labels, logger)
	if status != "" && prep.patchID == "" {
		stopPreparation()
		w.Metrics.Review(account.Key(), string(status), time.Since(started))
		return err
	}
	if err != nil {
		// Cancellation and timeout still reach afterRun; the review
		// ends rather than staying running while River retries the job.
		return w.finishEnded(ctx, ended, err)
	}
	phase := &publishPhase{
		w: w, file: file, account: account, settings: prep.eff.Settings, client: client, pr: pr,
		reviewID: reviewID, runID: runID, jobID: job.ID, lease: admitted.lease, trigger: args.Trigger, logger: logger,
		parse: review.ParseOptions{
			RequireSuggestedFix: prep.eff.Review.RequireSuggestedFix, Rules: prep.ruleIDs,
			Repository: pr.repository, Diagram: prep.eff.Review.Diagram, Prior: reviewFindings(prior.findings),
		},
		repoNotes: prep.notes, prior: prior, scope: prep.scope, templates: prep.templates,
		agent: agentOutcome,
	}
	phase.handoff = func(ctx context.Context, tx pgx.Tx) error {
		return phase.advance(ctx, tx, job, jobs.PrepareArgs{ReviewArgs: job.Args, ReviewID: reviewID})
	}
	if err := phase.checkpoint(pctx, nil); err != nil {
		stopPreparation()
		if ctx.Err() != nil {
			return w.finishEnded(ctx, ended, err)
		}
		logger.Error("runner answer not checkpointed", "error", err)
		w.Metrics.Review(account.Key(), string(store.ReviewFailed), time.Since(started))
		fctx, finish := detach(ctx)
		defer finish()
		return w.failReview(fctx, ended, prep.patchID, err.Error())
	}
	stopPreparation()
	publicationHandled = true
	return nil
}

// loadPullRequest reads a job's pull request. One the store does not know
// cancels the job: it will not appear by retrying.
func loadPullRequest(ctx context.Context, st *store.Store, accountID, repositoryID string, number int) (*pullRequest, error) {
	var row store.ReviewPullRequest
	err := st.WithAccount(ctx, accountID, func(tx pgx.Tx) error {
		var err error
		row, err = store.FindReviewPullRequest(ctx, tx, repositoryID, number)
		return err
	})
	if errors.Is(err, store.ErrNotFound) {
		return nil, river.JobCancel(fmt.Errorf("worker: pull request %d of %s is unknown", number, repositoryID))
	}
	if err != nil {
		return nil, fmt.Errorf("worker: load pull request: %w", err)
	}
	var closed string
	switch {
	case row.Merged:
		closed = "the pull request was merged"
	case row.State != "open":
		closed = "the pull request is closed"
	}
	return &pullRequest{id: row.ID, repositoryID: row.RepositoryID, repository: row.Repository, number: row.Number,
		headSHA: row.HeadSHA, baseRef: row.BaseRef, title: row.Title, author: row.Author,
		authorIsBot: row.AuthorIsBot, closed: closed}, nil
}

// reviewBase is the commit a review of pr diffs its head against: the
// merge base with its base branch. A merge commit or a fast-forward puts
// the head in the base branch, where the merge base is the head itself and
// shows no change; the one the pull request's last completed review used
// still does, and is "" when there is none.
func reviewBase(ctx context.Context, st *store.Store, client forge.Client, accountID string, pr *pullRequest) (string, error) {
	owner, repo := pr.ownerRepo()
	base, err := client.MergeBase(ctx, owner, repo, pr.baseRef, pr.headSHA)
	if err != nil || base != pr.headSHA {
		return base, err
	}
	err = st.WithAccount(ctx, accountID, func(tx pgx.Tx) error {
		var err error
		base, err = store.LastReviewMergeBase(ctx, tx, pr.id, pr.headSHA)
		return err
	})
	if err != nil {
		return "", fmt.Errorf("worker: read the last review's merge base: %w", err)
	}
	return base, nil
}

// nothingToReview is why pr, whose head is in its base branch with no
// earlier merge base to diff it against, is not reviewed.
func nothingToReview(pr *pullRequest) string {
	return "nothing to review: the head is already in " + pr.baseRef
}

// statusless is a forge client that sets no commit status.
type statusless struct{ forge.Client }

func (statusless) SetStatus(context.Context, string, string, string, forge.StatusState, string) error {
	return nil
}

// earlyEnd is what a review ended before its runner is recorded with.
type earlyEnd struct {
	args                              jobs.ReviewArgs
	pr                                *pullRequest
	accountKey, mergeBase, forgePatch string
	// skip is why the review was skipped: the repository's .kritika.yaml
	// did (a repoconfig.SkipReason), or a bot's patch was unchanged.
	skip string
	// filter is the name of the exclusion that filtered the review out, ""
	// for one without a name or a review no inclusion let in.
	filter string
	// carried is the confidence an unchanged patch keeps from its last
	// review, nil when the repository asks for no score, and gate whether
	// the repository has it fail the status.
	carried *review.Confidence
	gate    bool
	started time.Time
	jobID   int64
	// created dates the review the end records, now when zero.
	created time.Time
	logger  *slog.Logger
	// client reports the end on the head commit, under owner/repo; nil
	// until begin has built it, when a superseded head is the only end.
	client      forge.Client
	owner, repo string
}

// end records a review that never ran as status, for reason, counts it,
// and says so on the head commit, so a head no runner was spent on does
// not read as one still waiting for its review. A failed one marks the
// pull request as one whose review ran does. A retry of its job that ends
// it the same way records and counts nothing new, but says so again.
func (w *Review) end(ctx context.Context, e earlyEnd, status store.ReviewStatus, reason string) error {
	var recorded bool
	err := w.Store.WithAccount(ctx, e.args.AccountID, func(tx pgx.Tx) error {
		var err error
		recorded, err = store.RecordEndedReview(ctx, tx, store.EndedReview{
			AccountID: e.args.AccountID, PullRequestID: e.pr.id, HeadSHA: e.args.HeadSHA, MergeBaseSHA: e.mergeBase,
			ForgePatchID: e.forgePatch, Status: status, SkipReason: e.skip, Trigger: e.args.Trigger, Error: reason, JobID: e.jobID,
			CreatedAt: e.created,
		})
		return err
	})
	if err != nil {
		return err
	}
	if recorded {
		w.Metrics.Review(e.accountKey, string(status), time.Since(e.started))
	}
	state, desc := e.status(status, reason)
	if state == "" || e.client == nil {
		return nil
	}
	if err := e.client.SetStatus(ctx, e.owner, e.repo, e.args.HeadSHA, state, desc); err != nil {
		e.logger.Warn("commit status not set", "error", err)
	}
	if status == store.ReviewFailed {
		pullMarks(e.client, e.owner, e.repo, e.args.Number, e.logger).settle(ctx, failed)
	}
	return nil
}

// status is the commit status an early end reports, or "" for none: a
// superseded head leaves the status to its successor.
func (e earlyEnd) status(status store.ReviewStatus, reason string) (forge.StatusState, string) {
	switch status {
	case store.ReviewFailed:
		return forge.StatusError, "kritika: review failed (" + reason + ")"
	case store.ReviewCapped:
		return forge.StatusSuccess, "kritika: capped (" + reason + ")"
	case store.ReviewSkipped:
		// A skip with no reason of its own is an admission's, whose
		// reason is the error it records.
		if e.skip != "" {
			reason = skipDescription(e.skip, e.filter)
		}
		state, desc := skipVerdict(e.carried, e.gate, reason)
		return state, "kritika: " + desc
	}
	return "", ""
}

// skipUnchangedBot ends a bot's review whose rebase changed nothing,
// before a lease or a runner is spent on it; the runner's own patch check
// stays the backstop for what the forge cannot tell. A manual re-run is
// never skipped, nor is a review the pull request owes someone (see
// store.ReviewOwed): a push that replaced the run they asked for leaves
// their request to this one. It returns the forge patch id the review
// records, and whether it ended the review, with the error of recording
// that.
func (w *Review) skipUnchangedBot(
	ctx context.Context, e earlyEnd, client forge.Client, owner, repo string, settings configfile.Settings,
) (string, bool, error) {
	if !e.pr.authorIsBot || e.args.Trigger == jobs.TriggerManual {
		return "", false, nil
	}
	patch, unchanged := w.botPatch(ctx, e.logger, client, owner, repo, e.args.AccountID, e.pr, e.mergeBase)
	if !unchanged {
		return patch, false, nil
	}
	var owed, skippable bool
	e.gate = settings.Confidence.Gate
	if err := w.Store.WithAccount(ctx, e.args.AccountID, func(tx pgx.Tx) error {
		var err error
		if owed, err = store.ReviewOwed(ctx, tx, e.pr.id); err != nil || owed {
			return err
		}
		e.carried, skippable, err = carriedConfidence(ctx, tx, e.pr.id, "", settings.Confidence)
		return err
	}); err != nil {
		return "", true, err
	}
	if owed {
		e.logger.Info("bot patch unchanged, reviewed anyway: a review asked for since the last was not delivered")
		return patch, false, nil
	}
	if !skippable {
		return patch, false, nil
	}
	e.logger.Info("review skipped before its runner: bot patch unchanged", "forge_patch_id", review.ShortSHA(patch))
	e.forgePatch, e.skip = patch, runner.SkipUnchangedPatch
	if err := w.end(ctx, e, store.ReviewSkipped, ""); err != nil {
		return patch, true, err
	}
	carryApproval(ctx, e.logger, client, e.pr, settings, e.carried)
	return patch, true, nil
}

// begun is a review job past everything before its admission: its pull
// request is current, a model slot was free when it looked, the forge
// answered, the repository's .kritika.yaml is applied and does not skip it,
// and it is not an unchanged bot rebase. notes are what the review's
// summary says about the file.
type begun struct {
	early              earlyEnd
	eff                Effective
	notes              []string
	client             forge.Client
	owner, repo, token string
}

// begin takes a review job up to its admission, or ends it: superseded,
// replaced, snoozed while every model slot is held, or skipped by the
// merge-base .kritika.yaml or as an unchanged bot rebase.
// The admin's model's slots are checked before any forge call, so a job
// snoozed through a busy spell costs the forge nothing each time it wakes;
// a repository that chooses another model then waits for that model's
// slots too. It reports whether it ended the job, with the error of that or
// of getting this far.
func (w *Review) begin(
	ctx context.Context, job *river.Job[jobs.ReviewArgs], file *configfile.File, account *configfile.Account,
	logger *slog.Logger, started time.Time,
) (begun, bool, error) {
	args := job.Args
	pr, err := loadPullRequest(ctx, w.Store, args.AccountID, args.RepositoryID, args.Number)
	if err != nil {
		return begun{}, true, err
	}
	e := earlyEnd{args: args, pr: pr, accountKey: account.Key(), started: started, jobID: job.ID, logger: logger}
	if pr.headSHA != args.HeadSHA {
		logger.Info("review superseded before start", "current_head", review.ShortSHA(pr.headSHA))
		return begun{}, true, w.endHeadMoved(ctx, e, job)
	}
	// A retry of a job whose review a newer review replaced starts none in
	// its place; start would refuse it too, after the forge calls below.
	if replaced, err := w.jobReviewReplaced(ctx, args.AccountID, pr.id, job.ID); err != nil || replaced {
		if replaced {
			err = river.JobCancel(store.ErrReviewReplaced)
		}
		return begun{}, true, err
	}
	// A job queued while the pull request was open may only run, after a
	// retry or a wait for a slot, once it is merged or closed: an automatic
	// review has nothing left to do then, and no status is set on its head.
	// Only a review someone asked for runs.
	if pr.closed != "" && args.Trigger != jobs.TriggerManual {
		logger.Info("review skipped before start", "reason", pr.closed)
		return begun{}, true, w.end(ctx, e, store.ReviewSkipped, pr.closed)
	}
	settings := file.Settings(account, pr.repository)
	if held, err := w.slotsHeld(ctx, e, job, account.ID(), settings); held {
		return begun{}, true, err
	}
	client, err := w.client(ctx, file, account, pr.repository)
	if err != nil {
		return begun{}, true, err
	}
	if pr.closed != "" {
		// A look back at a merged or closed pull request someone asked
		// for: its head will not merge again, so no check on it says
		// anything of the review.
		logger.Info("reviewing on request", "reason", pr.closed)
		client = statusless{client}
	}
	owner, repo := pr.ownerRepo()
	e.client, e.owner, e.repo = client, owner, repo
	if e.mergeBase, err = reviewBase(ctx, w.Store, client, args.AccountID, pr); err != nil {
		return begun{}, true, err
	}
	if e.mergeBase == "" {
		reason := nothingToReview(pr)
		logger.Info("review skipped before start", "reason", reason)
		return begun{}, true, w.end(ctx, e, store.ReviewSkipped, reason)
	}
	doc, notes, err := readRepoConfig(ctx, client, owner, repo, e.mergeBase)
	if err != nil {
		return begun{}, true, err
	}
	eff, parseNotes := effective(settings, doc)
	if done, err := w.skipByRepo(ctx, e, job, &eff); done {
		return begun{}, true, err
	}
	if eff.Models.Review != settings.Models.Review {
		if held, err := w.slotsHeld(ctx, e, job, account.ID(), eff.Settings); held {
			return begun{}, true, err
		}
	}
	token, err := client.GitToken(ctx, repo)
	if err != nil {
		return begun{}, true, err
	}
	forgePatch, done, err := w.skipUnchangedBot(ctx, e, client, owner, repo, eff.Settings)
	if done {
		return begun{}, true, err
	}
	e.forgePatch = forgePatch
	return begun{early: e, eff: eff, notes: append(notes, parseNotes...), client: client, owner: owner, repo: repo, token: token}, false, nil
}

// endHeadMoved ends a review whose head moved before it started as
// superseded. One someone asked for is owed to them until a review
// completes (see store.ReviewOwed), and is dated by its job, when it was
// asked: a job that wakes to find the head moved records its review
// late, after the review that answered it or a newer request may have
// started, and the date keeps it from outranking them.
func (w *Review) endHeadMoved(ctx context.Context, e earlyEnd, job *river.Job[jobs.ReviewArgs]) error {
	if e.args.Trigger == jobs.TriggerManual {
		e.created = job.CreatedAt
	}
	return w.end(ctx, e, store.ReviewSuperseded, "")
}

// errNoSlot is a review's admission finding every model slot held after
// begin saw one free.
var errNoSlot = errors.New("worker: every model slot is held")

// admit settles what a review may spend before its runner starts: its
// runner spends against the model through the gateway, so it takes its
// model lease and passes the account's caps (see agentAdmit), and is
// snoozed if the slot begin saw free has been taken since. It reports
// whether it ended the job, with the error of admitting or of recording
// that.
func (w *Review) admit(
	ctx context.Context, e earlyEnd, job *river.Job[jobs.ReviewArgs], file *configfile.File, account *configfile.Account,
	settings configfile.Settings,
) (admission, bool, error) {
	a, status, reason, err := w.agentAdmit(ctx, e.logger, file, account, settings, job.ID)
	if errors.Is(err, errNoSlot) {
		return admission{}, true, w.snooze(ctx, e, job, string(settings.Models.Review))
	}
	if err != nil {
		return admission{}, true, err
	}
	if status == "" {
		return a, false, nil
	}
	e.logger.Warn("review not admitted", "status", status, "reason", reason)
	return admission{}, true, w.end(ctx, e, status, reason)
}

// slotsHeld snoozes the job when every one of the account's slots on the
// review model settings name is held, and reports whether it did, with the
// error that snoozes it. Slots it cannot count let the review go on.
func (w *Review) slotsHeld(
	ctx context.Context, e earlyEnd, job *river.Job[jobs.ReviewArgs], accountID string, settings configfile.Settings,
) (bool, error) {
	ref := string(settings.Models.Review)
	free, err := w.Store.SlotFree(ctx, accountID, ref, settings.Limits.Concurrency)
	if err != nil {
		e.logger.Warn("model slots not read; the review goes on", "error", err)
		return false, nil
	}
	if free {
		return false, nil
	}
	return true, w.snooze(ctx, e, job, ref)
}

// A review that has not started its runner is snoozed while every model
// slot is held, between snoozeMin and snoozeMax, and gives its worker back
// to the queue meanwhile. A slot let go before then wakes it (see
// store.SlotWaitKey and SlotWake); the snooze bounds the wait when none
// is, as when a holder died and its lease expired, or River dropped the
// event of the saved snooze.
const (
	snoozeMin = 5 * time.Second
	snoozeMax = time.Minute
)

// snooze puts a review that found every model slot held back on the
// queue, for longer each time, without counting an attempt: it gives its
// worker back rather than holding it while it waits. The job is marked
// with the model it waits for, so the next release of one of its slots
// wakes it. River keeps the count of a job's snoozes in its metadata.
func (w *Review) snooze(ctx context.Context, e earlyEnd, job *river.Job[jobs.ReviewArgs], modelKey string) error {
	var meta struct {
		Snoozes int `json:"snoozes"`
	}
	if err := json.Unmarshal(job.Metadata, &meta); err != nil {
		e.logger.Warn("job metadata not read; snoozing as if for the first time", "error", err)
	}
	if err := river.MetadataSet(ctx, store.SlotWaitKey, modelKey); err != nil {
		e.logger.Warn("job not marked as waiting for a slot; it wakes when its snooze ends", "error", err)
	}
	if err := river.MetadataSet(ctx, slotWaitSinceKey, time.Now().UTC()); err != nil {
		e.logger.Warn("model slot wait start not recorded", "error", err)
	}
	d := store.Backoff(meta.Snoozes, snoozeMin, snoozeMax)
	e.logger.Info("review snoozed: every model slot is held", "model", modelKey, "snoozes", meta.Snoozes+1, "for", d.Round(time.Second))
	w.Metrics.ReviewSnoozed(e.accountKey, modelKey)
	return river.JobSnooze(d)
}

// clearSlotWait clears the mark an earlier snooze for a slot left on the
// job, so a release does not wake it while it waits out a retry or a
// publication in place of a review that waits for a slot. snooze sets it again
// when this attempt finds every slot held.
func clearSlotWait(ctx context.Context, logger *slog.Logger, job *river.Job[jobs.ReviewArgs]) {
	var meta map[string]any
	if json.Unmarshal(job.Metadata, &meta) != nil || meta[store.SlotWaitKey] == nil {
		return
	}
	if err := river.MetadataSet(ctx, store.SlotWaitKey, nil); err != nil {
		logger.Warn("job's slot wait mark not cleared; a release may wake it early", "error", err)
	}
	if err := river.MetadataSet(ctx, slotWaitSinceKey, nil); err != nil {
		logger.Warn("model slot wait start not cleared", "error", err)
	}
}

// botPatch is the patch id of a bot pull request's diff as its forge
// reports it, and whether the pull request's last completed
// review had the same one. A diff the forge will not give, or a review
// before it without one, tells nothing: the pull request is then reviewed
// as usual.
func (w *Review) botPatch(
	ctx context.Context, logger *slog.Logger, client forge.Client, owner, repo, accountID string, pr *pullRequest, mergeBase string,
) (patch string, unchanged bool) {
	diff, err := client.PullRequestDiff(ctx, owner, repo, mergeBase, pr.headSHA)
	if err != nil {
		logger.Warn("forge diff not read; the runner checks the patch", "error", err)
		return "", false
	}
	patch = gitfetch.PatchID(diff)
	var last string
	err = w.Store.WithAccount(ctx, accountID, func(tx pgx.Tx) error {
		var err error
		last, err = store.LastForgePatchID(ctx, tx, pr.id)
		return err
	})
	if err != nil {
		logger.Warn("last forge patch id not read; the runner checks the patch", "error", err)
	}
	return patch, last != "" && last == patch
}

// publishWait is how long a review whose start found the pull request's
// publication lock held waits before it tries again: the older review is
// posting, which is brief, and ends completed or not before the new one
// may cancel it.
const publishWait = 5 * time.Second

// start records the review and its runner run, canceling the pull
// request's older active reviews (see store.StartReview), and reads the
// last completed review the new one may build on. It holds the pull
// request's publication lock meanwhile, so an older review posting its
// result either finishes before the new one starts or never posts: it is
// store.ErrPublicationBusy while one does. started, when not nil, runs
// under the lock once the review is recorded, for what the new review
// says of its head before any newer one may start.
func (w *Review) start(
	ctx context.Context, args jobs.ReviewArgs, pr *pullRequest, mergeBase, forgePatchID string, jobID int64,
	started func(context.Context),
) (s store.Started, prior priorReview, err error) {
	err = w.Store.WithPublicationLock(ctx, pr.id, func(ctx context.Context) error {
		err := w.Store.WithAccount(ctx, args.AccountID, func(tx pgx.Tx) error {
			var err error
			if prior, err = lastCompleted(ctx, tx, pr.id); err != nil {
				return err
			}
			dismissed, err := store.Dismissals(ctx, tx, pr.id)
			if err != nil {
				return err
			}
			prior.withDismissals(dismissed)
			s, err = store.StartReview(ctx, tx, store.NewReview{
				AccountID: args.AccountID, PullRequestID: pr.id, HeadSHA: args.HeadSHA, MergeBaseSHA: mergeBase,
				ForgePatchID: forgePatchID, Trigger: args.Trigger, JobID: jobID,
			})
			return err
		})
		if err == nil && started != nil {
			started(ctx)
		}
		return err
	})
	return s, prior, err
}

// jobReviewReplaced reports whether a review an earlier attempt of job
// jobID started on the pull request was replaced by a newer review.
func (w *Review) jobReviewReplaced(ctx context.Context, accountID, prID string, jobID int64) (bool, error) {
	var replaced bool
	err := w.Store.WithAccount(ctx, accountID, func(tx pgx.Tx) error {
		var err error
		replaced, err = store.JobReviewReplaced(ctx, tx, prID, jobID)
		return err
	})
	return replaced, err
}

// endReview ends a review as end says, and reports whether it did (see
// store.EndReview).
func (w *Review) endReview(ctx context.Context, accountID, reviewID string, end store.ReviewEnd) (bool, error) {
	var ended bool
	err := w.Store.WithAccount(ctx, accountID, func(tx pgx.Tx) error {
		var err error
		ended, err = store.EndReview(ctx, tx, reviewID, end)
		return err
	})
	return ended, err
}

// workerStopping reports whether ctx, a job's, ended because its River
// client is stopping: River otherwise ends a job's ctx only when an admin
// cancels the job or its timeout passes.
func workerStopping(ctx context.Context) bool {
	return stoppingCause(context.Cause(ctx))
}

// failRun ends a runner run that never got a Job.
func failRun(ctx context.Context, st *store.Store, accountID, runID, errText string) error {
	return st.WithAccount(ctx, accountID, func(tx pgx.Tx) error { return store.FailRunnerRun(ctx, tx, runID, errText) })
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
