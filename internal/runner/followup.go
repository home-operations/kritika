package runner

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/home-operations/kritika/internal/agent"
	"github.com/home-operations/kritika/internal/gitfetch"
	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/review"
	"github.com/home-operations/kritika/internal/store"
)

const submitReplyDescription = "Submit the reply and end the run. The input is the whole reply, as it is posted in the thread. " +
	"Call it exactly once, when you are done."

// runFollowUp answers the last comment of the spec's thread: it fetches the
// head and merge-base as a review does, shows the agent the diff, the
// findings kritika posted and the thread, and runs it with a review's
// tools until it submits the reply, which the worker posts. It writes no
// context pack: nothing builds on a follow-up.
func runFollowUp(ctx context.Context, st *store.Store, p Spec, secrets Secrets, logger *slog.Logger) error {
	if err := setPhase(ctx, st, p.RunID, "fetching"); err != nil {
		return err
	}
	res, err := gitfetch.Run(ctx, gitfetch.Fetch{CloneURL: p.CloneURL, Token: secrets.GitToken, Head: p.Head, Base: p.Base})
	if err != nil {
		return err
	}
	defer func() { _ = res.Close() }()
	logger.Info("fetched", "head", p.Head[:7], "base", p.Base[:7], "changed_paths", len(res.Changed), "diff_bytes", len(res.Diff))

	if err := setPhase(ctx, st, p.RunID, "parsing"); err != nil {
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
	// A follow-up has no summary to state what could not be kept in.
	files, _, err := repoFiles(baseTree, p.RepoFiles, res.Changed)
	if err != nil {
		return err
	}
	chunks, _, err := stages(ctx, headTree, baseTree, res, p.Ignore)
	if err != nil {
		return err
	}
	in := newPromptInputs(p, files, nil, res.Changed)
	var tools agentTools
	similar, search := similarContext(ctx, p, secrets, res, logger)
	chunks, tools.search = append(chunks, similar...), search
	var cleanup func()
	tools.run, tools.fetch, cleanup = commandTool(ctx, p, agent.NewTree(headTree, p.Ignore), secrets.GitToken,
		p.Agent.limits().MaxToolOutputBytes, logger)
	defer cleanup()

	system := review.FollowUpSystemPrompt(in.rules, in.instructions, tools.commands(), tools.fetch != nil, tools.search != nil)
	pr := p.Prompt.PullRequest
	user := review.BuildFollowUp(review.Input{
		Repository: p.Prompt.Repository, Number: pr.Number, Title: pr.Title, Author: pr.Author, Body: pr.Body, BaseRef: pr.BaseRef,
		Changed: res.Changed, Diff: res.Diff, Context: chunks, References: in.references,
		BudgetTokens: review.UserBudget(system, p.Agent.MaxPromptTokens),
	}, p.Prompt.Prior, p.Thread)
	if err := setPhase(ctx, st, p.RunID, "reviewing"); err != nil {
		return err
	}
	return runAgentic(ctx, st, p, secrets, headTree, p.Ignore, tools, agentPrompt{
		system: system, user: user, validate: review.CheckFollowUp,
		submit: model.ToolDef{Name: review.SubmitReply, Description: submitReplyDescription, InputSchema: review.FollowUpSchema()},
	}, review.ScopeFull, logger)
}
