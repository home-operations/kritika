package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/home-operations/kritika/internal/agent"
	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/review"
	"github.com/home-operations/kritika/internal/runspec"
	"github.com/home-operations/kritika/internal/tuning"
)

// conversationOf asks the gateway for the conversation the run's token
// lets it carry on, within tuning's Runner.ConversationTimeout. Its answer,
// a conversation under tuning's Agent.ContinueTokens, is bounded by
// Gateway.MaxConversationBytes, which bounds every step's conversation.
func conversationOf(ctx context.Context, gatewayURL, token string) (*agent.Conversation, error) {
	ctx, cancel := context.WithTimeout(ctx, tuning.Get().Runner.ConversationTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(gatewayURL, "/")+"/v1/conversation", nil)
	if err != nil {
		return nil, fmt.Errorf("runner: conversation: %w", err)
	}
	req.Header.Set("Authorization", tuning.BearerPrefix+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("runner: conversation: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	most := int64(tuning.Get().Gateway.MaxConversationBytes)
	body, err := io.ReadAll(io.LimitReader(resp.Body, most+1))
	switch {
	case err != nil:
		return nil, fmt.Errorf("runner: conversation: %w", err)
	case int64(len(body)) > most:
		return nil, errors.New("runner: conversation: the gateway's answer is too large")
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("runner: conversation: %s: %s", resp.Status, bytes.TrimSpace(body))
	}
	var c agent.Conversation
	if err := json.Unmarshal(body, &c); err != nil {
		return nil, fmt.Errorf("runner: conversation: %w", err)
	}
	return &c, nil
}

// carryOn is prompt carrying on the last review's conversation, which the
// worker let the run carry on and the gateway serves it. It does this when
// all of these hold. This review's system prompt and tools match those of the
// conversation, byte for byte, so that the provider's cache still holds it.
// The conversation opened on the pull request's title, description, and
// linked issues as they are now. It takes at most 1/Runner.CarryShare of the
// run's budget.
//
// Otherwise, carryOn is prompt, which starts afresh with the last review's
// findings and notes. tools are every tool the agent is offered.
func carryOn(
	ctx context.Context, p runspec.Spec, secrets runspec.Secrets, prompt agentPrompt, tools []agent.Tool,
	diff, delta string, logger *slog.Logger,
) agentPrompt {
	c, err := conversationOf(ctx, p.GatewayURL, secrets.GatewayToken)
	if err != nil {
		logger.Info("the last review's conversation is not carried on", "reason", secrets.Mask(err.Error()))
		return prompt
	}
	budget := review.UserBudget(prompt.system, p.Agent.MaxPromptTokens)
	pr := p.Prompt.PullRequest
	brief := review.Input{Number: pr.Number, Title: pr.Title, BaseRef: pr.BaseRef, DefaultBranch: p.Prompt.DefaultBranch,
		BaseOf: p.Prompt.BaseOf, Below: p.Prompt.Below, Body: pr.Body, Issues: p.Prompt.Issues, BudgetTokens: budget}
	var why string
	switch {
	case !(agent.Run{System: prompt.system, Tools: tools, Submit: prompt.submit}).Carries(c):
		why = "its system prompt or tools differ from this review's"
	case len(c.Messages) == 0 || !review.SameBrief(c.Messages[0].Text, brief):
		why = "the pull request's title, base branch, description or linked issues changed"
	case c.Tokens*int64(tuning.Get().Runner.CarryShare) > agentLimits(p.Agent).MaxTokens:
		why = "too large for the run's budget"
	}
	if why != "" {
		logger.Info("the last review's conversation is not carried on", "reason", why)
		return prompt
	}
	fetched := slices.ContainsFunc(c.Messages, func(m model.Message) bool {
		return slices.ContainsFunc(m.ToolCalls, func(tc model.ToolCall) bool { return tc.Name == "fetch_repo" })
	})
	user, omitted, referencesOmitted := review.BuildContinuation(review.ContinueInput{
		PriorHeadSHA: p.PriorHead, HeadSHA: p.Head, DeltaDiff: delta, Bumps: review.Bumps(diff), Prior: p.Prompt.Prior,
		Dismissed: p.Prompt.Dismissed, Diagram: p.Prompt.Diagram, Fetched: fetched, BudgetTokens: budget,
		References: prompt.references,
	})
	logger.Info("carrying on the last review's conversation", "run", review.ShortSHA(p.Prompt.Continue.RunID), "tokens", c.Tokens,
		"messages", len(c.Messages))
	return agentPrompt{
		system: prompt.system, user: user, submit: prompt.submit, validate: prompt.validate, fallback: prompt.fallback, quotes: prompt.quotes,
		omitted: omitted, carried: c, references: prompt.references, referencesOmitted: referencesOmitted,
	}
}
