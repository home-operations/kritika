package runner

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/home-operations/kritika/internal/agent"
	"github.com/home-operations/kritika/internal/gitfetch"
	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/review"
	"github.com/home-operations/kritika/internal/runspec"
)

const submitReplyDescription = "Submit the reply and end the run. The input is the whole reply, as it is posted in the thread. " +
	"Call it exactly once, when you are done."

// submitIfNeededDescription is submitReplyDescription for a reply in a
// finding thread, which can post nothing.
const submitIfNeededDescription = "Submit the reply, or none and why, and end the run. The reply is posted whole in the " +
	"thread. Call it exactly once, when you are done."

// runFollowUp answers the last comment of the spec's thread. It fetches the
// head and merge-base as a review does, and it shows the agent the diff, the
// findings kritika posted, and the thread. It runs the agent with a review's
// tools until the agent submits the reply, which the worker posts. It writes
// no context pack: nothing builds on a follow-up.
func runFollowUp(ctx context.Context, report *reporter, p runspec.Spec, secrets runspec.Secrets, logger *slog.Logger) error {
	if err := report.phase(ctx, "fetching"); err != nil {
		return err
	}
	res, err := gitfetch.Run(ctx, gitfetch.Fetch{CloneURL: p.CloneURL, Token: secrets.GitToken, Head: p.Head, Base: p.Base})
	if err != nil {
		return err
	}
	defer func() { _ = res.Close() }()
	logger.Info("fetched", "head", p.Head[:7], "base", p.Base[:7], "changed_paths", len(res.Changed), "diff_bytes", len(res.Diff))

	if err := report.phase(ctx, "parsing"); err != nil {
		return err
	}
	headTree, err := res.Head.Tree()
	if err != nil {
		return fmt.Errorf("runner: head tree: %w", err)
	}
	baseTree, err := res.Base.Tree()
	if err != nil {
		return fmt.Errorf("runner: base tree: %w", err)
	}
	// A follow-up has no summary to state what was not kept in.
	files, _, err := repoFiles(baseTree, p.RepoFiles)
	if err != nil {
		return err
	}
	p, files, _, err = withRuleFiles(baseTree, p, files)
	if err != nil {
		return err
	}
	chunks, _, err := stages(ctx, headTree, baseTree, res, p.Ignore)
	if err != nil {
		return err
	}
	in := newPromptInputs(p, files, nil, res.Changed)
	tools := agentTools{diff: newReadDiffTool(res.Diff, agentLimits(p.Agent).MaxToolOutputBytes)}
	similar, search := similarContext(ctx, p, secrets, res, logger)
	chunks, tools.search = append(chunks, similar...), search
	var cleanup func()
	tools.run, tools.fetch, tools.notes, cleanup = commandTool(ctx, p, agent.NewTree(headTree, p.Ignore), nil, secrets,
		agentLimits(p.Agent).MaxToolOutputBytes, logger)
	defer cleanup()
	if tools.fetch != nil {
		tools.oci = newFetchOCITool(p)
	}

	systemPrompt, build, validate, schema, description := review.FollowUpSystemPrompt, review.BuildFollowUp, review.CheckFollowUp,
		review.FollowUpSchema, submitReplyDescription
	if p.Reply {
		systemPrompt, build, validate, schema, description = review.ReplySystemPrompt, review.BuildReply, review.CheckReply,
			review.ReplySchema, submitIfNeededDescription
	}
	system := review.WithReviewer(systemPrompt(in.rules, tools.commands(), tools.fetch != nil, tools.search != nil), followUpRole(p, files))
	pr := p.Prompt.PullRequest
	user := build(review.Input{
		Repository: p.Prompt.Repository, Number: pr.Number, Title: pr.Title, Author: pr.Author, Body: pr.Body, BaseRef: pr.BaseRef,
		Changed: res.Changed, Diff: res.Diff, Context: chunks, References: in.references,
		BudgetTokens: review.UserBudget(system, p.Agent.MaxPromptTokens),
	}, p.Prompt.Prior, p.Thread)
	if err := report.phase(ctx, "reviewing"); err != nil {
		return err
	}
	return runAgentic(ctx, report, p, secrets, headTree, p.Ignore, tools, agentPrompt{
		system: system, user: user, validate: validate,
		submit: model.ToolDef{Name: review.SubmitReply, Description: description, InputSchema: schema()},
	}, review.ScopeFull, logger)
}
