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
	"time"

	"github.com/home-operations/kritika/internal/agent"
	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/review"
)

// conversationTimeout bounds asking the gateway for the conversation to
// carry on.
const conversationTimeout = 30 * time.Second

// maxConversationBody bounds the gateway's answer: a conversation under
// agent.ContinueTokens, at a few bytes a token, with room for its
// encoding.
const maxConversationBody = 16 << 20

// conversationOf asks the gateway for the conversation the run's token
// lets it carry on.
func conversationOf(ctx context.Context, gatewayURL, token string) (*agent.Conversation, error) {
	ctx, cancel := context.WithTimeout(ctx, conversationTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(gatewayURL, "/")+"/v1/conversation", nil)
	if err != nil {
		return nil, fmt.Errorf("runner: conversation: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("runner: conversation: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxConversationBody+1))
	switch {
	case err != nil:
		return nil, fmt.Errorf("runner: conversation: %w", err)
	case len(body) > maxConversationBody:
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

// carryShare bounds the conversation a run carries on to a share of its
// token budget: every step reads the whole of it again, and a budget the
// monthly cap cut short would leave the agent too few steps to work in.
const carryShare = 4

// carryOn is prompt carrying on the last review's conversation, which the
// worker let the run carry on and the gateway serves it, when this review
// would send it the system prompt and tools it was sent with, byte for
// byte, so that the provider's cache still holds it, when it opened on the
// pull request's title, description and linked issues as they are now, and
// when it takes at most a carryShare of the run's budget. Otherwise it is
// prompt, which starts afresh with the last review's findings and notes.
// tools are every tool the agent is offered.
func carryOn(
	ctx context.Context, p Spec, secrets Secrets, prompt agentPrompt, tools []agent.Tool, delta string, logger *slog.Logger,
) agentPrompt {
	c, err := conversationOf(ctx, p.Model.GatewayURL, secrets.GatewayToken)
	if err != nil {
		logger.Info("the last review's conversation is not carried on", "reason", secrets.Mask(err.Error()))
		return prompt
	}
	budget := review.UserBudget(prompt.system, p.Agent.MaxPromptTokens)
	pr := p.Prompt.PullRequest
	brief := review.Input{Number: pr.Number, Title: pr.Title, Body: pr.Body, Issues: p.Prompt.Issues, BudgetTokens: budget}
	var why string
	switch {
	case !(agent.Run{System: prompt.system, Tools: tools, Submit: prompt.submit}).Carries(c):
		why = "its system prompt or tools differ from this review's"
	case len(c.Messages) == 0 || !review.SameBrief(c.Messages[0].Text, brief):
		why = "the pull request's title, description or linked issues changed"
	case c.Tokens*carryShare > p.Agent.limits().MaxTokens:
		why = "too large for the run's budget"
	}
	if why != "" {
		logger.Info("the last review's conversation is not carried on", "reason", why)
		return prompt
	}
	fetched := slices.ContainsFunc(c.Messages, func(m model.Message) bool {
		return slices.ContainsFunc(m.ToolCalls, func(tc model.ToolCall) bool { return tc.Name == "fetch_repo" })
	})
	user, omitted := review.BuildContinuation(review.ContinueInput{
		PriorHeadSHA: p.PriorHead, HeadSHA: p.Head, DeltaDiff: delta, Dismissed: p.Prompt.Dismissed, Diagram: p.Prompt.Diagram,
		Fetched: fetched, BudgetTokens: budget,
	})
	logger.Info("carrying on the last review's conversation", "run", review.ShortSHA(p.Prompt.Continue.RunID), "tokens", c.Tokens,
		"messages", len(c.Messages))
	return agentPrompt{
		system: prompt.system, user: user, submit: prompt.submit, validate: prompt.validate, omitted: omitted, carried: c,
	}
}
