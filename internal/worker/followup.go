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

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/executor"
	"github.com/home-operations/kritika/internal/forge"
	"github.com/home-operations/kritika/internal/gateway"
	"github.com/home-operations/kritika/internal/jobs"
	"github.com/home-operations/kritika/internal/repoconfig"
	"github.com/home-operations/kritika/internal/review"
	"github.com/home-operations/kritika/internal/runner"
	"github.com/home-operations/kritika/internal/store"
)

// Follow-up bounds: mentions answered per pull request per hour before a
// single "limit reached" reply, and thread messages kept in the prompt.
const (
	followUpsPerHour = 5
	threadMessages   = 20
)

// FollowUp works the followup queue: one job answers one comment that
// @-mentioned the bot, scoped to its thread. A question is answered by an
// agent in a runner, with a review's tools; the commands (review, dismiss,
// pause, resume) are carried out here.
type FollowUp struct {
	river.WorkerDefaults[jobs.FollowUpArgs]
	Base
	Executor executor.Executor
	// GatewayURL is where a runner calls its model, and GatewayTokenTTL how
	// long its run token outlives the Job's deadline.
	GatewayURL      string
	GatewayTokenTTL time.Duration

	// superviseEvery overrides superviseInterval, and rowWait agentRowWait.
	superviseEvery time.Duration
	rowWait        time.Duration
}

// finalError is a follow-up's failure once its runner has run: a retry
// would pay the model for the same answer again, so the job ends with it.
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
	comment, err := client.GetComment(ctx, owner, repo, args.CommentID, args.Inline)
	if err != nil {
		return err
	}
	login, err := client.BotLogin(ctx)
	if err != nil {
		return err
	}
	f := &followUp{w: w, file: file, account: account, settings: file.Settings(account, pr.repository), client: client, pr: pr,
		comment: comment, owner: owner, repo: repo, botLogin: login, jobID: job.ID, attempt: job.Attempt, logger: logger}
	if done, err := f.alreadyAnswered(ctx); err != nil || done {
		return err
	}
	outcome, err := f.run(ctx)
	w.Metrics.FollowUp(account.Key(), string(outcome))
	if err != nil {
		logger.Error("follow-up failed", "error", err)
		// The failure itself is what goes back to River; a record of it that
		// could not be written is lost with it, and the retry records anew.
		_ = f.record(ctx, store.FollowupFailed, err.Error(), 0, "")
		if _, final := errors.AsType[finalError](err); final {
			return river.JobCancel(err)
		}
		return err
	}
	logger.Info("follow-up " + string(outcome))
	return nil
}

type followUp struct {
	w        *FollowUp
	file     *configfile.File
	account  *configfile.Account
	settings configfile.Settings
	// eff is settings with the merge-base .kritika.yaml applied, and
	// mergeBase that commit; repoConfig reads both.
	eff       Effective
	mergeBase string
	client    forge.Client
	pr        *pullRequest
	comment   forge.Comment
	owner     string
	repo      string
	botLogin  string
	jobID     int64
	// attempt is the job's, 1 the first time.
	attempt int
	logger  *slog.Logger
}

// alreadyAnswered guards a retried job: once a reply is on the forge the
// mention is done, whatever happened after posting. The record says so
// first; on a retry, the forge is asked too, by the FollowUpMarker the
// reply carries, since an attempt killed between posting and recording
// left none.
func (f *followUp) alreadyAnswered(ctx context.Context) (bool, error) {
	var status store.FollowupStatus
	var replyID *int64
	err := f.w.Store.WithAccount(ctx, f.account.ID(), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT status, reply_comment_id FROM followups WHERE pull_request_id = $1 AND comment_id = $2`,
			f.pr.id, f.comment.ID).Scan(&status, &replyID)
	})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, fmt.Errorf("worker: read follow-up: %w", err)
	}
	if status == store.FollowupAnswered || status == store.FollowupLimited || replyID != nil {
		f.logger.Info("follow-up already handled", "status", status)
		f.unmark(ctx)
		return true, nil
	}
	if f.attempt <= 1 {
		return false, nil
	}
	id, err := f.repliedOnForge(ctx)
	if err != nil || id == 0 {
		return false, err
	}
	f.logger.Info("follow-up already answered on the forge", "reply", id)
	f.unmark(ctx)
	return true, f.record(ctx, store.FollowupAnswered, "", id, "")
}

// unmark ends the marks an earlier attempt left on the mention when it was
// killed between posting the reply and ending them: the reply is up, so
// the mention is answered.
func (f *followUp) unmark(ctx context.Context) {
	if f.attempt > 1 {
		f.marks().start(ctx)(true, false)
	}
}

// repliedOnForge returns the id of the bot's reply to the comment, by its
// FollowUpMarker, where the reply would be posted, or 0.
func (f *followUp) repliedOnForge(ctx context.Context) (int64, error) {
	var comments []forge.Comment
	var err error
	if f.comment.Inline {
		comments, err = f.client.ListInline(ctx, f.owner, f.repo, f.pr.number)
	} else {
		comments, err = f.client.ListConversation(ctx, f.owner, f.repo, f.pr.number)
	}
	if err != nil {
		return 0, err
	}
	return markedReply(comments, f.botLogin, f.comment.ID), nil
}

// markedReply is the id of the comment by login whose FollowUpMarker names
// commentID, or 0.
func markedReply(comments []forge.Comment, login string, commentID int64) int64 {
	want := strconv.FormatInt(commentID, 10)
	for _, c := range comments {
		if !strings.EqualFold(c.Author, login) {
			continue
		}
		if id, ok := review.MarkedFollowUp(c.Body); ok && id == want {
			return c.ID
		}
	}
	return 0
}

// run qualifies the mention, gathers the thread and the last review's
// findings, has an agent answer, and posts the reply. Nothing after the
// reply is posted may fail the job: a retry would answer twice.
func (f *followUp) run(ctx context.Context) (store.FollowupStatus, error) {
	if reason := f.disqualified(ctx); reason != "" {
		f.logger.Info("follow-up ignored", "reason", reason)
		return store.FollowupIgnored, f.record(ctx, store.FollowupIgnored, reason, 0, "")
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
	thread, err := f.thread(ctx)
	if err != nil {
		return store.FollowupFailed, err
	}
	rec, err := f.reviewRecord(ctx)
	if err != nil {
		return store.FollowupFailed, err
	}
	end, answered := f.marks().start(ctx), false
	defer func() { end(answered, false) }()
	agent, err := f.ask(ctx, thread, rec)
	if err != nil {
		return store.FollowupFailed, err
	}
	reply, err := review.ParseFollowUp(string(agent.Result), f.pr.repository)
	if err != nil {
		return store.FollowupFailed, finalError{err}
	}
	// The reply is posted even once the job's ctx has ended: the agent
	// has been paid for it. A fenced job is the exception, since another
	// replica may be answering it by now.
	if cause := context.Cause(ctx); errors.Is(cause, errJobFenced) {
		return store.FollowupFailed, cause
	}
	pctx, cancel := detach(ctx)
	defer cancel()
	replyID, err := f.reply(pctx, review.FollowUpBody(reply, agent.Model, string(f.settings.Models.Effort)))
	if err != nil {
		return store.FollowupFailed, err
	}
	answered = true
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

// ask has an agent answer the thread's last message in a runner, as a
// review's agent is run: against the pull request's head, with the
// repository's commands, through the model gateway, which charges what it
// spends as the follow-up's. It holds one of the review model's slots
// while the runner works. It returns the agent's run, which submitted a
// reply. A failure once the runner has run is a finalError, unless the
// worker is stopping, when the job is retried.
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
	prompt := &runner.Prompt{
		Repository: f.pr.repository, Context: f.eff.Review.Context, Rules: repoconfig.RulesFor(f.eff.Review.Rules, rec.vars),
		Prior: rec.findings,
	}
	var agent *store.AgentRunRow
	err = f.w.withLease(ctx, f.account, string(ref), f.settings.Limits.Concurrency, f.jobID, func(ctx context.Context) error {
		var runID string
		err := f.w.Store.WithAccount(ctx, f.account.ID(), func(tx pgx.Tx) error {
			var err error
			if prompt.PullRequest, err = loadFilterPR(ctx, tx, f.pr.id); err != nil {
				return err
			}
			runID, err = store.InsertRunnerRun(ctx, tx, f.account.ID(), store.RunnerKindFollowUp, "", f.jobID)
			return err
		})
		if err != nil {
			return err
		}
		prompt.Trim()
		agent, err = f.runAgent(ctx, runID, gitToken, prompt, thread, rec)
		return err
	})
	return agent, err
}

// runAgent runs the follow-up's runner as run runID and reads the agent's
// run back.
func (f *followUp) runAgent(
	ctx context.Context, runID, gitToken string, prompt *runner.Prompt, thread []review.Message, rec reviewRecord,
) (*store.AgentRunRow, error) {
	limits, ref := f.settings.Agent, f.settings.Models.Review
	deadline, resources := f.file.RunnerFor()
	deadline = agentDeadline(deadline, limits.Timeout)
	token, err := f.w.Store.MintGatewayToken(ctx, store.GatewayGrant{
		RunID: runID, AccountID: f.account.ID(), ReviewID: rec.id, RepositoryID: f.pr.repositoryID, FollowupCommentID: f.comment.ID,
		Model: string(ref), Fallback: string(f.settings.Models.Fallback), Effort: string(f.settings.Models.Effort), Budget: limits.MaxTokens,
	}, time.Now().Add(deadline+f.w.GatewayTokenTTL))
	if err != nil {
		dctx, cancel := detach(ctx)
		defer cancel()
		return nil, errors.Join(err, failRun(dctx, f.w.Store, f.account.ID(), runID, err.Error()))
	}
	sup := runSupervision(f.w.Store, f.account.ID(), runID, "", "", f.w.superviseEvery, f.logger)
	res, cause := supervise(ctx, sup, f.w.Executor, executor.Spec{
		Labels:      runnerLabels(f.account.Key(), f.pr.repository, jobs.QueueFollowUp, f.pr.number),
		Annotations: runnerAnnotations(f.jobID, f.pr.headSHA),
		Job: runner.Spec{
			Version: runner.SpecVersion, Kind: runner.KindFollowUp, RunID: runID, CloneURL: f.client.CloneURL(f.owner, f.repo),
			Head: f.pr.headSHA, Base: f.diffBase(rec), Ignore: f.settings.Ignore, RepoFiles: f.eff.repoFiles(),
			Prompt: prompt, Thread: thread,
			Model: &runner.ModelEndpoint{GatewayURL: f.w.GatewayURL, Model: gateway.ModelName},
			Agent: &runner.AgentLimits{
				MaxSteps: limits.MaxSteps, MaxToolOutputBytes: limits.MaxToolOutputBytes, MaxTokens: limits.MaxTokens,
				MaxPromptTokens: limits.MaxPromptTokens, TimeoutSeconds: int(limits.Timeout / time.Second),
				Commands: limits.Commands, CommandTimeoutSeconds: int(limits.CommandTimeout / time.Second),
			},
		},
		Secrets:   runner.Secrets{GitToken: gitToken, GatewayToken: token},
		Deadline:  deadline,
		Resources: resources,
		Tools:     f.file.ToolsFor(limits.Commands),
	})
	f.w.revokeGatewayTokens(ctx, f.logger, runID)
	agent, agentErr := f.w.readAgentRun(ctx, f.account.ID(), runID, ref, stopped(ctx, res, cause), f.w.rowWait)
	// The run's record must land even once the job's ctx has ended. A
	// record that cannot be written does not cost the agent's answer: the
	// run is then ended by the sweep of runs whose job is over.
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
	case res.Err != nil && errors.Is(cause, errHeartbeatLost):
		return nil, finalError{errors.New("worker: runner heartbeat lost")}
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

// disqualified returns why the mention is not answered, or "".
func (f *followUp) disqualified(ctx context.Context) string {
	if f.comment.AuthorIsBot || strings.EqualFold(f.comment.Author, f.botLogin) {
		return "author is a bot"
	}
	slug := strings.TrimSuffix(f.botLogin, "[bot]")
	if !mentioned(f.comment.Body, slug) {
		return "does not mention @" + slug
	}
	perm, err := f.client.Permission(ctx, f.owner, f.repo, f.comment.Author)
	if err != nil {
		f.logger.Warn("permission lookup failed", "error", err)
		return "permission unknown"
	}
	if !forge.CanWrite(perm) {
		return "author has " + string(perm) + " access, write is required"
	}
	return ""
}

// rateLimited posts the limit notice once per hour when the pull request
// has had its share of answers, and records it.
func (f *followUp) rateLimited(ctx context.Context) (bool, error) {
	var answered, notices int
	err := f.w.Store.WithAccount(ctx, f.account.ID(), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status = 'answered'), count(*) FILTER (WHERE status = 'limited')
			FROM followups WHERE pull_request_id = $1 AND created_at > now() - interval '1 hour'`, f.pr.id).Scan(&answered, &notices)
	})
	if err != nil {
		return false, fmt.Errorf("worker: count follow-ups: %w", err)
	}
	if answered < followUpsPerHour {
		return false, nil
	}
	var replyID int64
	if notices == 0 {
		if replyID, err = f.client.CreateComment(ctx, f.owner, f.repo, f.pr.number, review.LimitBody); err != nil {
			return true, err
		}
	}
	f.logger.Info("follow-up rate limited", "answered_last_hour", answered, "notice_posted", notices == 0)
	return true, f.record(ctx, store.FollowupLimited, "", replyID, "")
}

// thread returns the messages the model sees. The asking comment is always
// last.
func (f *followUp) thread(ctx context.Context) ([]review.Message, error) {
	var comments []forge.Comment
	var err error
	switch {
	case !f.comment.Inline:
		if comments, err = f.client.ListConversation(ctx, f.owner, f.repo, f.pr.number); err != nil {
			return nil, err
		}
	// Only a reply has a thread to gather: a comment that replies to none
	// starts its thread.
	case f.comment.InReplyTo != 0:
		root := f.comment.InReplyTo
		all, err := f.client.ListInline(ctx, f.owner, f.repo, f.pr.number)
		if err != nil {
			return nil, err
		}
		for _, c := range all {
			if c.ID == root || c.InReplyTo == root {
				comments = append(comments, c)
			}
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
	return msgs, nil
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
// completed review's findings; without a review the thread stands alone.
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
		rec.id, rec.findings = last.id, reviewFindings(last.findings)
		if err := tx.QueryRow(ctx, `SELECT merge_base_sha FROM reviews WHERE id = $1`, last.id).Scan(&rec.mergeBase); err != nil {
			return fmt.Errorf("worker: load last completed review's merge base: %w", err)
		}
		return nil
	})
	return rec, err
}

// diffBase is the commit the follow-up's runner diffs the head against:
// the merge base, unless that is the head itself, as it is once a merge
// has put the pull request's commits in its base branch. The last review's
// merge base then still shows the change.
func (f *followUp) diffBase(rec reviewRecord) string {
	if f.mergeBase == f.pr.headSHA && rec.mergeBase != "" {
		return rec.mergeBase
	}
	return f.mergeBase
}

// repoConfig applies the .kritika.yaml at the pull request's merge base to
// the follow-up's settings, so it answers with the repository's model,
// agent settings and instructions. It returns why the file stops the
// follow-up, or "".
func (f *followUp) repoConfig(ctx context.Context) (string, error) {
	base, err := f.client.MergeBase(ctx, f.owner, f.repo, f.pr.baseRef, f.pr.headSHA)
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

// reply posts body, led by its FollowUpMarker, where the mention was made:
// in its inline thread, or on the conversation.
func (f *followUp) reply(ctx context.Context, body string) (int64, error) {
	body = review.FollowUpMarker(f.comment.ID) + "\n" + body
	if f.comment.Inline {
		return f.client.ReplyInline(ctx, f.owner, f.repo, f.pr.number, f.comment, body)
	}
	return f.client.CreateComment(ctx, f.owner, f.repo, f.pr.number, body)
}

func (f *followUp) record(ctx context.Context, status store.FollowupStatus, reason string, replyID int64, modelName string) error {
	return f.w.Store.WithAccount(ctx, f.account.ID(), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO followups
			(account_id, pull_request_id, comment_id, author, inline, path, line, status, reason, reply_comment_id, model)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, left($9, 500), nullif($10::bigint, 0), $11)
			ON CONFLICT (pull_request_id, comment_id) DO UPDATE SET status = excluded.status, reason = excluded.reason,
				reply_comment_id = coalesce(excluded.reply_comment_id, followups.reply_comment_id), model = excluded.model`,
			f.account.ID(), f.pr.id, f.comment.ID, f.comment.Author, f.comment.Inline, f.comment.Path, f.comment.Line,
			status, reason, replyID, modelName)
		if err != nil {
			return fmt.Errorf("worker: record follow-up: %w", err)
		}
		return nil
	})
}
