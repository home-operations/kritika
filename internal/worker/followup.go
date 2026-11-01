package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/home-operations/kritika/internal/adapter"
	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/executor"
	"github.com/home-operations/kritika/internal/forge"
	"github.com/home-operations/kritika/internal/forge/github"
	"github.com/home-operations/kritika/internal/gateway"
	"github.com/home-operations/kritika/internal/jobs"
	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/repoconfig"
	"github.com/home-operations/kritika/internal/review"
	"github.com/home-operations/kritika/internal/runspec"
	"github.com/home-operations/kritika/internal/store"
)

// Follow-up bounds: mentions answered per pull request per hour before a
// single "limit reached" reply, and thread messages kept in the prompt. The
// last bound is replies that the bot makes in one of its finding threads to
// replies that did not address it. Past that bound, it leaves the thread to a
// mention.
const (
	followUpsPerHour = 5
	threadMessages   = 20
	repliesPerThread = 3
)

// FollowUp works the followup queue. One job answers one comment that
// @-mentioned the bot, scoped to its thread. Where the repository asks for
// it, one job can instead answer a reply in a thread of one of the bot's
// findings. That reply does not mention the bot, and the job answers it when
// the reply needs an answer. An agent in a runner answers a question, with a
// review's tools. The commands (review, dismiss, pause, resume) are carried
// out here.
type FollowUp struct {
	river.WorkerDefaults[jobs.FollowUpArgs]
	Base
	Executor executor.Executor
	// Steppers pin the models a run is granted (pinModels).
	Steppers *adapter.Steppers
	// GatewayURL is where a runner calls its model, and GatewayTokenTTL how
	// long its run token outlives the Job's deadline.
	GatewayURL      string
	GatewayTokenTTL time.Duration

	// superviseEvery overrides superviseInterval, rowWait agentRowWait, and
	// awaitEvery awaitInterval.
	superviseEvery time.Duration
	rowWait        time.Duration
	awaitEvery     time.Duration
}

// finalError is a follow-up's failure once its runner ran: a retry pays the
// model for the same answer again, so the job ends with it.
type finalError struct{ error }

func (e finalError) Unwrap() error { return e.error }

// Work implements river.Worker.
func (w *FollowUp) Work(ctx context.Context, job *river.Job[jobs.FollowUpArgs]) error {
	args := job.Args
	file := w.Current.Get()
	account, err := w.account(file, args.AccountID)
	if err != nil {
		return err
	}
	logger := w.Logger.With("account", account.Key(), "pr", args.Number, "comment", args.CommentID)
	pr, err := loadPullRequest(ctx, w.Store, args.AccountID, args.RepositoryID, args.Number)
	if err != nil {
		return err
	}
	client, err := w.client(ctx, file, account, pr.repository)
	if err != nil {
		return err
	}
	owner, repo := pr.ownerRepo()
	f := &followUp{w: w, file: file, account: account, settings: file.Settings(account), client: client, pr: pr,
		comment: forge.Comment{ID: args.CommentID, Number: args.Number, Inline: args.Inline}, asReply: args.Reply, owner: owner, repo: repo,
		jobID: job.ID, attempt: job.Attempt, maxAttempts: job.MaxAttempts, requested: job.CreatedAt, logger: logger}
	// The job of an answered review request wakes until the review ends,
	// with nothing to read from the forge each time.
	awaiting, err := f.awaitingReview(ctx)
	if err != nil {
		return err
	}
	if awaiting {
		return f.awaitReview(ctx)
	}
	if pr.closed, err = w.refreshPullRequest(ctx, client, args.AccountID, pr, logger); err != nil {
		return err
	}
	if f.comment, err = client.GetComment(ctx, owner, repo, args.Number, args.CommentID, args.Inline); err != nil {
		return err
	}
	if f.botLogin, err = client.BotLogin(ctx); err != nil {
		return err
	}
	if done, err := f.alreadyAnswered(ctx); err != nil || done {
		return err
	}
	outcome, err := f.run(ctx)
	w.Metrics.FollowUp(account.Key(), string(outcome))
	if _, waits := errors.AsType[*river.JobSnoozeError](err); waits {
		logger.Info("follow-up handled", "outcome", outcome)
		return err
	}
	if err != nil {
		logger.Error("follow-up failed", "error", err)
		// An answered review request keeps its durable marker so a retry
		// resumes polling. Any other failure is recorded on past the job's
		// ctx, whose end can be the failure: River does not retry a job it
		// canceled, and the record is the mention's only trace.
		if outcome != store.FollowupAnswered {
			rctx, cancel := detach(ctx)
			if rerr := f.record(rctx, store.FollowupFailed, err.Error(), 0, ""); rerr != nil {
				logger.Error("follow-up not recorded", "error", rerr)
			}
			cancel()
		}
		if _, final := errors.AsType[finalError](err); final {
			return river.JobCancel(err)
		}
		return err
	}
	logger.Info("follow-up handled", "outcome", outcome)
	return nil
}

type followUp struct {
	w        *FollowUp
	file     *configfile.File
	account  *configfile.Account
	settings configfile.Settings
	// eff is settings with the merge-base .kritika.yaml applied, and
	// mergeBase is that commit. repoConfig reads both.
	eff       Effective
	mergeBase string
	client    forge.Client
	pr        *pullRequest
	comment   forge.Comment
	// asReply says the comment can be answered as a reply in one of the bot's
	// finding threads when it does not mention the bot. Once disqualified
	// qualifies it as such a reply, fingerprint is the finding that the
	// thread holds, and the comment is not a mention.
	asReply     bool
	fingerprint string
	owner       string
	repo        string
	botLogin    string
	jobID       int64
	// attempt is the job's, 1 the first time, and maxAttempts its last.
	attempt, maxAttempts int
	// requested is when the job was queued: a review that ended since can be
	// the one that a review request waits for.
	requested time.Time
	logger    *slog.Logger
}

// alreadyAnswered guards a retried job: once a reply is on the forge, the
// mention is done, whatever happened after posting. The record says so first.
// On a retry, the forge is asked too, by the FollowUpMarker that the reply
// carries. An attempt killed between posting and recording left no record.
func (f *followUp) alreadyAnswered(ctx context.Context) (bool, error) {
	var state store.FollowupState
	err := f.w.Store.WithAccount(ctx, f.account.ID(), func(tx pgx.Tx) error {
		var err error
		state, err = store.FindFollowup(ctx, tx, f.pr.id, f.comment.ID, f.comment.Inline)
		return err
	})
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return false, fmt.Errorf("worker: read follow-up: %w", err)
	}
	status, replyID := state.Status, state.ReplyCommentID
	if status == store.FollowupAnswered || status == store.FollowupLimited || replyID != nil {
		f.logger.Info("follow-up already handled", "status", status)
		if replyID != nil {
			f.unmark(ctx)
		}
		return true, nil
	}
	if f.attempt <= 1 {
		return false, nil
	}
	posted, err := f.repliedOnForge(ctx)
	if err != nil || posted.ID == 0 {
		return false, err
	}
	f.logger.Info("follow-up already answered on the forge", "reply", posted.ID)
	f.unmark(ctx)
	// A reply that conceded the finding says so to whoever reads it. The
	// attempt that posted it can end before it dismisses the finding. The
	// marker is kritika's own: the model's prose loses any marker that it
	// echoes, and only the thread's finding is conceded.
	if fingerprint, ok := review.MarkedConcession(posted.Body); ok && f.asReply {
		held, err := f.dismissedFinding(ctx)
		if err != nil {
			return true, err
		}
		if held == fingerprint {
			return true, f.recordConcession(ctx, posted.ID, "", fingerprint, posted.Body)
		}
		f.logger.Warn("conceded finding is not the thread's; not dismissed", "conceded", fingerprint, "thread", held)
	}
	return true, f.record(ctx, store.FollowupAnswered, "", posted.ID, "")
}

// unmark ends the marks an earlier attempt left on the mention when it was
// killed between posting the reply and ending them: the reply is up, so
// the mention is answered.
func (f *followUp) unmark(ctx context.Context) {
	if f.attempt > 1 {
		f.marks().start(ctx)(answered, false)
	}
}

// repliedOnForge returns the bot's reply to the comment, by its
// FollowUpMarker, where the reply is posted, or the zero comment.
func (f *followUp) repliedOnForge(ctx context.Context) (forge.Comment, error) {
	var comments []forge.Comment
	var err error
	if f.comment.Inline {
		comments, err = f.client.ListInline(ctx, f.owner, f.repo, f.pr.number)
	} else {
		comments, err = f.client.ListConversation(ctx, f.owner, f.repo, f.pr.number)
	}
	if err != nil {
		return forge.Comment{}, err
	}
	return markedReply(comments, f.botLogin, f.comment.ID), nil
}

// markedReply is the comment by login whose FollowUpMarker names commentID,
// or the zero comment.
func markedReply(comments []forge.Comment, login string, commentID int64) forge.Comment {
	want := strconv.FormatInt(commentID, 10)
	for _, c := range comments {
		if !strings.EqualFold(c.Author, login) {
			continue
		}
		if id, ok := review.MarkedFollowUp(c.Body); ok && id == want {
			return c
		}
	}
	return forge.Comment{}
}

// run qualifies the mention, or the reply, gathers the thread and the last
// review's findings, has an agent answer, and posts the reply. After the reply
// is posted, the job must not fail: a retry will answer twice.
func (f *followUp) run(ctx context.Context) (_ store.FollowupStatus, err error) {
	if reason := f.disqualified(ctx); reason != "" {
		f.logger.Info("follow-up ignored", "reason", reason)
		return store.FollowupIgnored, f.record(ctx, store.FollowupIgnored, reason, 0, "")
	}
	if f.asReply {
		return f.runReply(ctx)
	}
	slug := strings.TrimSuffix(f.botLogin, "[bot]")
	if requestsReview(f.comment.Body, slug) {
		return f.requestReview(ctx)
	}
	if reason, ok := requestsDismiss(f.comment.Body, slug); ok {
		return f.dismiss(ctx, reason)
	}
	if paused, ok := requestsPause(f.comment.Body, slug); ok {
		return f.pause(ctx, slug, paused)
	}
	reason, err := f.repoConfig(ctx)
	if err != nil {
		return store.FollowupFailed, err
	}
	if reason != "" {
		f.logger.Info("follow-up ignored", "reason", reason)
		return store.FollowupIgnored, f.record(ctx, store.FollowupIgnored, reason, 0, "")
	}
	limited, err := f.rateLimited(ctx)
	if err != nil {
		return store.FollowupFailed, err
	}
	if limited {
		return store.FollowupLimited, nil
	}
	thread, _, err := f.thread(ctx)
	if err != nil {
		return store.FollowupFailed, err
	}
	rec, err := f.reviewRecord(ctx)
	if err != nil {
		return store.FollowupFailed, err
	}
	end, o := f.marks().start(ctx), unanswered
	defer func() {
		if err != nil {
			o = f.failure(err)
		}
		end(o, false)
	}()
	agent, err := f.ask(ctx, thread, rec)
	if err != nil {
		return store.FollowupFailed, err
	}
	reply, err := review.ParseFollowUp(string(agent.Result), f.pr.repository)
	if err != nil {
		return store.FollowupFailed, finalError{err}
	}
	// The reply is posted even once the job's ctx ended: the agent is already
	// paid for it. A fenced job is the exception, because another replica can
	// be answering it by now.
	if cause := context.Cause(jobFence(ctx)); errors.Is(cause, errJobFenced) {
		return store.FollowupFailed, cause
	}
	pctx, cancel := detachUnlessFenced(ctx)
	defer cancel()
	replyID, err := f.reply(pctx, review.FollowUpBody(reply, agent.Model, string(f.settings.Models.Effort), agent.CommandErrors))
	if err != nil {
		return store.FollowupFailed, err
	}
	o = answered
	f.logger.Info("follow-up answered", "model", agent.Model, "reply", replyID, "steps", agent.Steps, "commands", agent.CommandsRun,
		"input_tokens", agent.Usage.Prompt(), "output_tokens", agent.Usage.Output, "cost_usd", agent.CostUSD)
	if err := f.record(pctx, store.FollowupAnswered, "", replyID, agent.Model); err != nil {
		f.logger.Error("follow-up not recorded", "error", err, "reply", replyID)
	}
	return store.FollowupAnswered, nil
}

// marks are the bot's reactions on the mention: the 👀 while its agent
// works, the 👍 once the reply is up.
func (f *followUp) marks() marks { return commentMarks(f.client, f.owner, f.repo, f.comment, f.logger) }

// failure is what the marks say of an answer that ended in err: failed
// once no attempt follows it, unanswered while River retries the job, or
// snoozes it for GitHub's rate limit (see ForgeRateLimit).
func (f *followUp) failure(err error) outcome {
	if _, limited := github.RetryAfter(err, time.Now()); limited {
		return unanswered
	}
	if _, final := errors.AsType[finalError](err); final || f.attempt >= f.maxAttempts {
		return failed
	}
	return unanswered
}

// ask has an agent answer the thread's last message in a runner, as a
// review's agent is run: against the pull request's head, with the
// repository's commands, through the model gateway, which charges what it
// spends as the follow-up's. It holds one of the review model's slots while
// the runner works. It returns the agent's run, which submitted a reply. A
// failure once the runner ran is a finalError, unless the worker is stopping,
// when the job is retried.
func (f *followUp) ask(ctx context.Context, thread []review.Message, rec reviewRecord) (*store.AgentRunRow, error) {
	ref := f.settings.Models.Review
	if ref == "" {
		return nil, errors.New("worker: no review model is configured for this repository")
	}
	if _, ok := f.file.Provider(f.account, ref.Provider()); !ok {
		return nil, fmt.Errorf("worker: provider %q is not in the configuration", ref.Provider())
	}
	gitToken, err := f.client.GitToken(ctx, f.repo)
	if err != nil {
		return nil, err
	}
	prompt := &runspec.Prompt{
		Repository: f.pr.repository, Context: f.eff.Review.Context, Rules: repoconfig.RulesFor(f.eff.Review.Rules, rec.vars),
		RuleFiles: ruleFiles(f.eff.Review.Rules), Prior: rec.findings,
	}
	var answering *store.ReviewerGrant
	name := f.findingReviewer(ctx, rec)
	if r, ok := f.eff.Reviewers[name]; ok && name != "" && r.On() {
		// The finding's reviewer answers for it, by its own instructions,
		// rules and models, while it is on.
		prompt.Rules, prompt.RuleFiles = repoconfig.RulesFor(r.Rules, rec.vars), nil
		prompt.Reviewers = []runspec.Reviewer{{Name: name, Instructions: r.Instructions, File: r.File}}
		models := r.Models(f.settings.Models)
		ref = models.Review
		f.settings.Models.Effort = models.Effort
		answering = &store.ReviewerGrant{Model: string(models.Review), Fallback: string(models.Fallback), Effort: string(models.Effort)}
	}
	var agent *store.AgentRunRow
	err = f.w.withLease(ctx, f.account, string(ref), f.settings.Limits.Concurrency, f.jobID, func(ctx context.Context) error {
		var runID string
		err := f.w.Store.WithAccount(ctx, f.account.ID(), func(tx pgx.Tx) error {
			var err error
			if prompt.PullRequest, err = store.ReadFilterPullRequest(ctx, tx, f.pr.id); err != nil {
				return err
			}
			runID, err = store.InsertRunnerRun(ctx, tx, f.account.ID(), store.RunnerKindFollowUp, "", f.jobID)
			return err
		})
		if err != nil {
			return err
		}
		prompt.Trim()
		agent, err = f.runAgent(ctx, runID, gitToken, prompt, thread, rec, answering)
		return err
	})
	return agent, err
}

// runAgent runs the follow-up's runner as run runID and reads the agent's run
// back. answering, when set, is the models of the reviewer that the prompt
// names. Its steps are granted those models by the reviewer's name, pinned as
// the review model is.
func (f *followUp) runAgent(
	ctx context.Context, runID, gitToken string, prompt *runspec.Prompt, thread []review.Message, rec reviewRecord,
	answering *store.ReviewerGrant,
) (*store.AgentRunRow, error) {
	limits, ref := f.settings.Agent, f.settings.Models.Review
	deadline := agentDeadline(f.file.RunnerDeadline(), limits.Timeout)
	models := pinModels(ctx, f.logger, f.w.Steppers, f.file, f.account, f.settings.Models)
	grant := store.GatewayGrant{
		RunID: runID, AccountID: f.account.ID(), ReviewID: rec.id, RepositoryID: f.pr.repositoryID, FollowupCommentID: f.comment.ID,
		Model: string(models.Review), Fallback: string(models.Fallback), Effort: string(models.Effort), Budget: limits.MaxTokens,
	}
	if answering != nil {
		pinned := pinModels(ctx, f.logger, f.w.Steppers, f.file, f.account, configfile.Models{
			Review: configfile.ModelRef(answering.Model), Fallback: configfile.ModelRef(answering.Fallback),
			Effort: model.Effort(answering.Effort),
		})
		ref = pinned.Review
		grant.Reviewers = map[string]store.ReviewerGrant{prompt.Reviewers[0].Name: {
			Model: string(pinned.Review), Fallback: string(pinned.Fallback), Effort: string(pinned.Effort),
		}}
	}
	token, err := f.w.Store.MintGatewayToken(ctx, grant, time.Now().Add(deadline+f.w.GatewayTokenTTL))
	if err != nil {
		dctx, cancel := detach(ctx)
		defer cancel()
		return nil, errors.Join(err, failRun(dctx, f.w.Store, f.account.ID(), runID, err.Error()))
	}
	reportToken, err := f.w.Store.MintRunnerToken(ctx, f.account.ID(), runID)
	if err != nil {
		return nil, err
	}
	sup := runSupervision(f.w.Store, f.account.ID(), runID, "", "", "", f.w.superviseEvery, f.logger)
	out := f.w.runRunner(ctx, f.w.Executor, sup, executor.Spec{
		Labels:      runnerLabels(f.account.Key(), f.pr.repository, jobs.QueueFollowUp, f.pr.number),
		Annotations: runnerAnnotations(f.jobID, f.pr.headSHA),
		Job: runspec.Spec{
			Version: runspec.Version, Kind: runspec.KindFollowUp, RunID: runID, CloneURL: f.client.CloneURL(f.owner, f.repo),
			GatewayURL: f.w.GatewayURL,
			Forge:      string(f.account.Forge), Head: f.pr.headSHA, Base: f.diffBase(rec), Ignore: f.settings.Ignore, RepoFiles: f.eff.repoFiles(),
			Tools: f.eff.Tools, Prompt: prompt, Thread: thread, Reply: f.asReply,
			Model: &runspec.ModelEndpoint{Model: gateway.ModelName},
			Agent: &runspec.AgentLimits{
				MaxSteps: limits.MaxSteps, MaxToolOutputBytes: limits.MaxToolOutputBytes, MaxTokens: limits.MaxTokens,
				MaxPromptTokens: limits.MaxPromptTokens, TimeoutSeconds: int(limits.Timeout / time.Second),
				CommandTimeoutSeconds: int(limits.CommandTimeout / time.Second), Fetches: limits.MaxFetches,
				Hosts: f.file.EgressHosts(), CacheLocked: f.file.GatewayCaches(),
			},
		},
		Secrets:  runspec.Secrets{GitToken: gitToken, GatewayToken: token, ReportToken: reportToken},
		Deadline: deadline,
	}, f.account.ID(), runID, ref, f.w.rowWait, f.logger)
	res, cause, agent, agentErr := out.res, out.cause, out.agent, out.agentErr
	// The run's record must land even once the job's ctx ended. A record that
	// cannot be written does not cost the agent's answer: the run is then
	// ended by the sweep of runs whose job is over.
	dctx, cancel := detach(ctx)
	defer cancel()
	if err := recordRun(dctx, f.w.Store, f.w.Metrics, f.account.Key(), f.account.ID(), runID, jobs.QueueFollowUp, res); err != nil {
		f.logger.Error("runner run not recorded", "error", err)
	}
	switch {
	case res.Err != nil && workerStopping(ctx):
		return nil, fmt.Errorf("worker: follow-up cut by a restart: %w", res.Err)
	// A runner whose pod never started spent nothing: the job is retried.
	case res.Err != nil && res.NeverStarted:
		return nil, fmt.Errorf("worker: runner did not start: %w", res.Err)
	// One whose pod Kubernetes disrupted did not fail on its own: the job is
	// retried too, and it pays the model again for its answer.
	case res.Err != nil && res.Disruption != "":
		return nil, fmt.Errorf("worker: runner pod disrupted: %w", res.Err)
	case res.Err != nil && errors.Is(cause, errHeartbeatLost):
		return nil, finalError{errors.New("worker: runner heartbeat lost")}
	// One that failed before any model step was recorded spent nothing on
	// the model either: the job is retried.
	case res.Err != nil && out.unpaid:
		return nil, fmt.Errorf("worker: runner failed before the model ran: %w", res.Err)
	case res.Err != nil:
		return nil, finalError{fmt.Errorf("worker: runner failed: %w", res.Err)}
	case agentErr != nil:
		return nil, finalError{agentErr}
	case agent == nil:
		return nil, finalError{errors.New("worker: the runner recorded no agent run")}
	}
	if err := stopError(*agent); err != nil {
		return nil, finalError{fmt.Errorf("worker: %w", err)}
	}
	return agent, nil
}

// disqualified returns why the mention, or the reply, is not answered, or "".
// A comment that mentions the bot is a mention, whatever thread it is in. A
// comment that does not is a reply when the job allows it and the thread is
// one of the bot's findings', still unresolved.
func (f *followUp) disqualified(ctx context.Context) string {
	if f.comment.AuthorIsBot || strings.EqualFold(f.comment.Author, f.botLogin) {
		return "author is a bot"
	}
	slug := strings.TrimSuffix(f.botLogin, "[bot]")
	switch mentions := mentioned(f.comment.Body, slug); {
	case mentions:
		f.asReply = false
	case !f.asReply:
		return "does not mention @" + slug
	case !f.settings.Review.Replies:
		return "replies in finding threads are off for this repository"
	}
	perm, err := f.client.Permission(ctx, f.owner, f.repo, f.comment.Author)
	if err != nil {
		f.logger.Warn("permission lookup failed", "error", err)
		return "permission unknown"
	}
	if !forge.CanWrite(perm) {
		return "author has " + string(perm) + " access, write is required"
	}
	// The thread is read for the author's reply alone: anyone can reply on
	// a public repository, and the lookups page the pull request's threads.
	if f.asReply {
		return f.replyDisqualified(ctx)
	}
	return ""
}

// rateLimited records the mention as limited once the pull request got its
// share of answers this hour. The first mention that it limits posts the
// limit notice. The record is made before the post, in the transaction that
// counts: a mention handled at the same time counts it, and a retry finds it.
// So the notice, which carries no marker to find it by on the forge, is
// posted at most once.
func (f *followUp) rateLimited(ctx context.Context) (bool, error) {
	var answered, notices int
	err := f.w.Store.WithAccount(ctx, f.account.ID(), func(tx pgx.Tx) error {
		var err error
		if answered, notices, err = store.RecentFollowupCounts(ctx, tx, f.pr.id); err != nil || answered < followUpsPerHour {
			return err
		}
		return f.recordTx(ctx, tx, store.FollowupLimited, "", 0, "")
	})
	if err != nil {
		return false, fmt.Errorf("worker: limit follow-ups: %w", err)
	}
	if answered < followUpsPerHour {
		return false, nil
	}
	var replyID int64
	if notices == 0 {
		pctx, cancel := detach(ctx)
		defer cancel()
		if replyID, err = f.client.CreateComment(pctx, f.owner, f.repo, f.pr.number, review.LimitBody); err != nil {
			f.logger.Warn("limit notice not posted", "error", err)
		} else if err := f.record(pctx, store.FollowupLimited, "", replyID, ""); err != nil {
			f.logger.Error("limit notice not recorded", "error", err, "reply", replyID)
		}
	}
	f.logger.Info("follow-up rate limited", "answered_last_hour", answered, "notice_posted", replyID != 0)
	return true, nil
}

// thread returns the messages the model sees, the asking comment always last,
// and how many replies the bot made in the thread: its comments that carry a
// FollowUpMarker, which a finding's own comment does not, counted over the
// whole thread, not the messages kept.
func (f *followUp) thread(ctx context.Context) ([]review.Message, int, error) {
	var comments []forge.Comment
	var err error
	switch {
	case !f.comment.Inline:
		if comments, err = f.client.ListConversation(ctx, f.owner, f.repo, f.pr.number); err != nil {
			return nil, 0, err
		}
	// Only a reply has a thread to gather: a comment that replies to none
	// starts its thread.
	case f.comment.InReplyTo != 0:
		root := f.comment.InReplyTo
		all, err := f.client.ListInline(ctx, f.owner, f.repo, f.pr.number)
		if err != nil {
			return nil, 0, err
		}
		for _, c := range all {
			if c.ID == root || c.InReplyTo == root {
				comments = append(comments, c)
			}
		}
	}
	replied := 0
	for _, c := range comments {
		if _, ok := review.MarkedFollowUp(c.Body); ok && strings.EqualFold(c.Author, f.botLogin) {
			replied++
		}
	}
	// The asking comment closes the thread whatever the timestamps say.
	asking := f.comment
	if i := slices.IndexFunc(comments, func(c forge.Comment) bool { return c.ID == f.comment.ID }); i >= 0 {
		asking = comments[i]
		comments = slices.Delete(comments, i, i+1)
	}
	slices.SortStableFunc(comments, func(a, b forge.Comment) int { return a.CreatedAt.Compare(b.CreatedAt) })
	comments = append(comments, asking)
	if len(comments) > threadMessages {
		comments = comments[len(comments)-threadMessages:]
	}
	msgs := make([]review.Message, 0, len(comments))
	for _, c := range comments {
		msgs = append(msgs, review.NewMessage(c.Author, c.Body, c.CreatedAt))
	}
	return msgs, replied, nil
}

type reviewRecord struct {
	// id is the review's, empty without one, and mergeBase the commit it
	// diffed the head against.
	id, mergeBase string
	findings      []review.Finding
	// vars is the pull request as a filter sees it, with the event of the
	// review, which the rules' when conditions are judged against.
	vars map[string]any
}

// reviewRecord loads the pull request's filter variables and the latest
// completed review's findings. Without a review, the thread stands alone.
func (f *followUp) reviewRecord(ctx context.Context) (reviewRecord, error) {
	var rec reviewRecord
	err := f.w.Store.WithAccount(ctx, f.account.ID(), func(tx pgx.Tx) error {
		last, err := lastCompleted(ctx, tx, f.pr.id)
		if err != nil {
			return err
		}
		if rec.vars, err = filterVars(ctx, tx, f.pr.id, last.trigger); err != nil || last.id == "" {
			return err
		}
		rec.id, rec.findings, rec.mergeBase = last.id, reviewFindings(last.findings), last.mergeBase
		return nil
	})
	return rec, err
}

// diffBase is the commit the follow-up's runner diffs the head against: the
// merge base, unless that is the head itself, as it is once a merge puts the
// pull request's commits in its base branch. The last review's merge base
// then still shows the change.
func (f *followUp) diffBase(rec reviewRecord) string {
	if f.mergeBase == f.pr.headSHA && rec.mergeBase != "" {
		return rec.mergeBase
	}
	return f.mergeBase
}

// repoConfig applies the .kritika.yaml at the pull request's merge base to
// the follow-up's configuration, so it answers with the repository's model,
// agent configuration, and instructions. It returns why the file stops the
// follow-up, or "".
func (f *followUp) repoConfig(ctx context.Context) (string, error) {
	base, err := f.client.MergeBase(ctx, f.owner, f.repo, f.pr.number, f.pr.baseRef, f.pr.headSHA)
	if err != nil {
		return "", err
	}
	doc, _, err := readRepoConfig(ctx, f.client, f.owner, f.repo, base)
	if err != nil {
		return "", err
	}
	eff, _ := effective(f.settings, doc)
	if !eff.Enabled {
		return repoconfig.SkipDisabled.Description(), nil
	}
	f.eff, f.mergeBase, f.settings = eff, base, eff.Settings
	return "", nil
}

// reply posts body, led by its FollowUpMarker, where the mention was made: in
// its inline thread, or on the conversation. A post that a fence cut fails
// with errJobFenced rather than the cancellation, which River takes for its
// stop and refunds the attempt for.
func (f *followUp) reply(ctx context.Context, body string) (int64, error) {
	body = review.FollowUpMarker(f.comment.ID) + "\n" + body
	var id int64
	var err error
	if f.comment.Inline {
		id, err = f.client.ReplyInline(ctx, f.owner, f.repo, f.pr.number, f.comment, body)
	} else {
		id, err = f.client.CreateComment(ctx, f.owner, f.repo, f.pr.number, body)
	}
	if cause := context.Cause(ctx); err != nil && errors.Is(cause, errJobFenced) {
		return id, cause
	}
	return id, err
}

func (f *followUp) record(ctx context.Context, status store.FollowupStatus, reason string, replyID int64, modelName string) error {
	return f.w.Store.WithAccount(ctx, f.account.ID(), func(tx pgx.Tx) error {
		return f.recordTx(ctx, tx, status, reason, replyID, modelName)
	})
}

func (f *followUp) recordTx(
	ctx context.Context, tx pgx.Tx, status store.FollowupStatus, reason string, replyID int64, modelName string,
) error {
	return store.RecordFollowup(ctx, tx, store.FollowupResult{
		AccountID: f.account.ID(), PullRequestID: f.pr.id, CommentID: f.comment.ID, Author: f.comment.Author,
		Inline: f.comment.Inline, Path: f.comment.Path, Line: f.comment.Line,
		Status: status, Reason: reason, ReplyCommentID: replyID, Model: modelName,
	})
}
