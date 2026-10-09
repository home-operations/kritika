// Package worker consumes River jobs. The review worker owns a review from
// the moment its job starts until write-back: it checks the head is still
// current, asks the forge for the merge-base, spawns a runner for the
// checkout work, and records everything about the run.
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
	// review model that writes a split review's summary from its parts'.
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

// lastPatchID is the patch id of the pull request's newest prepared or
// completed review other than reviewID, "" when it has none.
func lastPatchID(ctx context.Context, tx pgx.Tx, prID, reviewID string) (string, error) {
	var patch string
	err := tx.QueryRow(ctx, `SELECT patch_id FROM reviews WHERE pull_request_id = $1 AND id <> $2
		AND status IN ('prepared', 'completed') AND NOT partial ORDER BY created_at DESC LIMIT 1`, prID, reviewID).Scan(&patch)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("worker: read last review: %w", err)
	}
	return patch, nil
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
	account, err := w.account(file, args.AccountID)
	if err != nil {
		return err
	}
	logger := w.Logger.With("account", account.Key(), "pr", args.Number, "head", review.ShortSHA(args.HeadSHA))
	started := time.Now()
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

	reviewID, runID, prior, err := w.start(ctx, args, pr, mergeBase, b.early.forgePatch, job.ID)
	if err != nil {
		return err
	}
	// The status says the review is under way until its outcome replaces
	// it, so a head is never silent while its runner works.
	if err := client.SetStatus(ctx, owner, repo, args.HeadSHA, forge.StatusPending, "kritika: review running"); err != nil {
		logger.Warn("commit status not set", "error", err)
	}
	// An error on the last attempt has River discard the job, so nothing
	// would replace the pending status.
	defer func() {
		if err == nil || job.Attempt < job.MaxAttempts {
			return
		}
		sctx, cancel := detach(ctx)
		defer cancel()
		if serr := client.SetStatus(sctx, owner, repo, args.HeadSHA, forge.StatusError, "kritika: review failed"); serr != nil {
			logger.Warn("commit status not set", "error", serr)
		}
	}()
	// The pull request carries the bot's eyes while a review of it runs,
	// then its thumbs up once one is posted or its confused face when one
	// failed. An error on the last attempt leaves the review as it is, so
	// it counts as failed here as the commit status says.
	end := pullMarks(client, owner, repo, args.Number, logger).start(ctx)
	defer func() {
		mctx, cancel := detach(ctx)
		defer cancel()
		end(w.reviewOutcome(mctx, logger, args.AccountID, reviewID, err, job.Attempt >= job.MaxAttempts),
			w.otherReviewRunning(mctx, logger, args.AccountID, pr.id, reviewID))
	}()
	deadline, resources := file.RunnerFor()
	spec := runner.Spec{
		Version: runner.SpecVersion, Kind: runner.KindReview, RunID: runID, CloneURL: client.CloneURL(owner, repo),
		Head: args.HeadSHA, Base: mergeBase, PriorHead: prior.headSHA, PriorChanged: prior.changed, PriorBase: prior.mergeBase,
		Ignore: settings.Ignore, RepoFiles: eff.repoFiles(),
	}
	secrets := runner.Secrets{GitToken: b.token}
	ended := endedReview{
		accountID: args.AccountID, accountKey: account.Key(), reviewID: reviewID, headSHA: args.HeadSHA,
		owner: owner, repo: repo, client: client, started: started, logger: logger,
	}
	provider, _ := file.Provider(account, settings.Models.Review.Provider())
	cont := w.continuation(ctx, logger, args.AccountID, prior, args.HeadSHA, mergeBase, settings.Models.Review, provider.Type)
	parts := w.splitParts(ctx, client, pr, settings.Agent.MaxParts, logger)
	extra, releaseExtra := w.freeSlots(ctx, logger, account, settings, parts-1, job.ID)
	defer releaseExtra()
	deadline, promptNotes, err := w.agentSpec(ctx, args.AccountID, reviewID, runID, args.Trigger, pr, eff, prior, cont, admitted,
		sizing{parts: parts, slots: 1 + extra}, &spec, &secrets, deadline, client, logger)
	if err != nil {
		return w.agentSpecFailed(ctx, ended, runID, err)
	}
	b.notes = append(b.notes, promptNotes...)
	tools := file.ToolsFor(settings.Agent.Commands)
	sup := runSupervision(w.Store, args.AccountID, runID, pr.id, args.HeadSHA, w.superviseEvery, logger)
	out := w.runRunner(ctx, w.Executor, sup, executor.Spec{
		Labels:      runnerLabels(account.Key(), pr.repository, jobs.QueueReview, args.Number),
		Annotations: runnerAnnotations(job.ID, args.HeadSHA),
		Job:         spec,
		Secrets:     secrets,
		Deadline:    deadline,
		Resources:   resources,
		Tools:       tools,
	}, args.AccountID, runID, settings.Models.Review, w.rowWait, logger)
	// The parts are done with the slots they ran in.
	releaseExtra()
	res, cause, agentOutcome, agentErr := out.res, out.cause, out.agent, out.agentErr
	// A River cancel (JobCancelTx from a web request) cancels ctx itself,
	// unlike supervise's own errSuperseded/errHeartbeatLost, which only
	// cancel the child ctx passed to the executor. ctx is left live from here
	// on so a cancel that arrives during afterRun (review status "prepared")
	// still takes effect there. cctx is a detached copy for the terminal
	// writes up to afterRun, which must still land once ctx itself has
	// ended.
	canceled := errors.Is(context.Cause(ctx), river.ErrJobCancelledRemotely)
	cctx, cancel := detach(ctx)
	defer cancel()
	ended.jobName = res.JobName
	if err := recordRun(cctx, w.Store, w.Metrics, account.Key(), args.AccountID, runID, jobs.QueueReview, res); err != nil {
		if !canceled {
			return err
		}
		// River will not retry a canceled job, so the review ends here
		// whether or not its run's record could be written.
		logger.Error("runner run not recorded", "error", err)
	}
	if judged, err := w.judgeRun(ctx, cctx, job, ended, canceled, res, cause, agentErr); judged {
		return err
	}
	prep, status, err := w.afterRun(ctx, job, account, pr, eff, b.notes, client, reviewID, runID, prior,
		spec.Prompt.PullRequest.Labels, logger)
	if err != nil {
		// ctx stayed live through afterRun, so a cancel or a timeout that
		// arrived while it ran surfaces here as a plain error; the review
		// ends rather than staying running while River retries the job.
		return w.finishEnded(ctx, ended, err)
	}
	if prep.patchID == "" {
		w.Metrics.Review(account.Key(), string(status), time.Since(started))
		return nil
	}
	patchID := prep.patchID
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
	status, perr := phase.run(ctx)
	// Publishing finishes on a detached ctx, so a clean result stands even
	// if ctx ended meanwhile: the comment and the commit status already say
	// so. Only a publish that failed while ctx ended ends as canceled or
	// timed out.
	if perr != nil && ctx.Err() != nil {
		return w.finishEnded(ctx, ended, perr)
	}
	if perr != nil && status == store.ReviewFailed {
		logger.Error("review failed", "error", perr)
	}
	logger.Info("review " + string(status))
	w.Metrics.Review(account.Key(), string(status), time.Since(started))
	fctx, fcancel := detach(ctx)
	defer fcancel()
	if status == store.ReviewFailed && !phase.statusReported {
		return w.failReview(fctx, ended, patchID, errText(perr))
	}
	return w.finishReview(fctx, args.AccountID, reviewID, status, patchID, errText(perr))
}

// loadPullRequest reads a job's pull request. One the store does not know
// cancels the job: it will not appear by retrying.
func loadPullRequest(ctx context.Context, st *store.Store, accountID, repositoryID string, number int) (*pullRequest, error) {
	var pr pullRequest
	err := st.WithAccount(ctx, accountID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT p.id, p.repository_id, r.name, p.number, p.head_sha, p.base_ref, p.title, p.author, p.author_is_bot,
				CASE WHEN p.merged THEN 'the pull request was merged' WHEN p.state <> 'open' THEN 'the pull request is closed'
					ELSE '' END
			FROM pull_requests p JOIN repositories r ON r.id = p.repository_id
			WHERE p.repository_id = $1 AND p.number = $2`, repositoryID, number).
			Scan(&pr.id, &pr.repositoryID, &pr.repository, &pr.number,
				&pr.headSHA, &pr.baseRef, &pr.title, &pr.author, &pr.authorIsBot, &pr.closed)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, river.JobCancel(fmt.Errorf("worker: pull request %d of %s is unknown", number, repositoryID))
	}
	if err != nil {
		return nil, fmt.Errorf("worker: load pull request: %w", err)
	}
	return &pr, nil
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
		err := tx.QueryRow(ctx, `SELECT merge_base_sha FROM reviews WHERE pull_request_id = $1 AND status = 'completed'
			AND merge_base_sha NOT IN ('', $2) ORDER BY created_at DESC LIMIT 1`, pr.id, pr.headSHA).Scan(&base)
		if errors.Is(err, pgx.ErrNoRows) {
			base = ""
			return nil
		}
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
	logger  *slog.Logger
	// client reports the end on the head commit, under owner/repo; nil
	// until begin has built it, when a superseded head is the only end.
	client      forge.Client
	owner, repo string
}

// end records a review that never ran as status, for reason, counts it,
// and says so on the head commit, so a head no runner was spent on does
// not read as one still waiting for its review. A failed one marks the
// pull request as one whose review ran does.
func (w *Review) end(ctx context.Context, e earlyEnd, status store.ReviewStatus, reason string) error {
	w.Metrics.Review(e.accountKey, string(status), time.Since(e.started))
	err := w.Store.WithAccount(ctx, e.args.AccountID, func(tx pgx.Tx) error {
		return store.RecordEndedReview(ctx, tx, store.EndedReview{
			AccountID: e.args.AccountID, PullRequestID: e.pr.id, HeadSHA: e.args.HeadSHA, MergeBaseSHA: e.mergeBase,
			ForgePatchID: e.forgePatch, Status: status, SkipReason: e.skip, Trigger: e.args.Trigger, Error: reason,
		})
	})
	if err != nil {
		return err
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
// never skipped. It returns the forge patch id the review records, and
// whether it ended the review, with the error of recording that.
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
	var skippable bool
	e.gate = settings.Confidence.Gate
	if err := w.Store.WithAccount(ctx, e.args.AccountID, func(tx pgx.Tx) error {
		var err error
		e.carried, skippable, err = carriedConfidence(ctx, tx, e.pr.id, "", settings.Confidence)
		return err
	}); err != nil {
		return "", true, err
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
// its settle time is over, and it is not an unchanged bot rebase. notes are
// what the review's summary says about the file.
type begun struct {
	early              earlyEnd
	eff                Effective
	notes              []string
	client             forge.Client
	owner, repo, token string
}

// begin takes a review job up to its admission, or ends it: superseded,
// snoozed while every model slot is held or until its settle time is over,
// or skipped by the merge-base .kritika.yaml or as an unchanged bot rebase.
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
	e := earlyEnd{args: args, pr: pr, accountKey: account.Key(), started: started, logger: logger}
	if pr.headSHA != args.HeadSHA {
		logger.Info("review superseded before start", "current_head", review.ShortSHA(pr.headSHA))
		return begun{}, true, w.end(ctx, e, store.ReviewSuperseded, "")
	}
	// A job queued while the pull request was open may only run, after a
	// settle time, a retry or a wait for a slot, once it is merged or
	// closed: an automatic review has nothing left to do then, and no status
	// is set on its head. Only a review someone asked for runs.
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
	if wait := settleLeft(args.Trigger, eff.Settle, job.CreatedAt, time.Now()); wait > 0 {
		logger.Info("review snoozed until its settle time is over", "for", wait.Round(time.Second))
		return begun{}, true, river.JobSnooze(wait)
	}
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
	e.logger.Warn("review "+string(status), "reason", reason)
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
	d := store.Backoff(meta.Snoozes, snoozeMin, snoozeMax)
	e.logger.Info("review snoozed: every model slot is held", "model", modelKey, "snoozes", meta.Snoozes+1, "for", d.Round(time.Second))
	w.Metrics.ReviewSnoozed(e.accountKey, modelKey)
	return river.JobSnooze(d)
}

// clearSlotWait clears the mark an earlier snooze for a slot left on the
// job, so a release does not wake it while it waits out its settle time or
// a retry in place of a review that waits for a slot. snooze sets it again
// when this attempt finds every slot held.
func clearSlotWait(ctx context.Context, logger *slog.Logger, job *river.Job[jobs.ReviewArgs]) {
	var meta map[string]any
	if json.Unmarshal(job.Metadata, &meta) != nil || meta[store.SlotWaitKey] == nil {
		return
	}
	if err := river.MetadataSet(ctx, store.SlotWaitKey, nil); err != nil {
		logger.Warn("job's slot wait mark not cleared; a release may wake it early", "error", err)
	}
}

// botPatch is the patch id of a bot pull request's diff as its forge
// reports it, and whether the pull request's last prepared or completed
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
		return tx.QueryRow(ctx, `SELECT forge_patch_id FROM reviews WHERE pull_request_id = $1
			AND status IN ('prepared', 'completed') AND NOT partial ORDER BY created_at DESC LIMIT 1`, pr.id).Scan(&last)
	})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		logger.Warn("last forge patch id not read; the runner checks the patch", "error", err)
	}
	return patch, last != "" && last == patch
}

// start records the review and its runner run, and reads the last
// completed review the new one may build on.
func (w *Review) start(
	ctx context.Context, args jobs.ReviewArgs, pr *pullRequest, mergeBase, forgePatchID string, jobID int64,
) (reviewID, runID string, prior priorReview, err error) {
	err = w.Store.WithAccount(ctx, args.AccountID, func(tx pgx.Tx) error {
		var err error
		if prior, err = lastCompleted(ctx, tx, pr.id); err != nil {
			return err
		}
		dismissed, err := store.Dismissals(ctx, tx, pr.id)
		if err != nil {
			return err
		}
		prior.withDismissals(dismissed)
		reviewID, runID, err = store.StartReview(ctx, tx, store.NewReview{
			AccountID: args.AccountID, PullRequestID: pr.id, HeadSHA: args.HeadSHA, MergeBaseSHA: mergeBase,
			ForgePatchID: forgePatchID, Trigger: args.Trigger, JobID: jobID,
		})
		return err
	})
	return reviewID, runID, prior, err
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
	cause := context.Cause(ctx)
	return ctx.Err() != nil && !errors.Is(cause, river.ErrJobCancelledRemotely) && !errors.Is(cause, context.DeadlineExceeded)
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
