package adapter

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/home-operations/kritika/internal/chatgpt"
	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/store"
)

// PlanSessions exposes the credentials the leader keeps current.
type PlanSessions interface {
	ChatGPTSession(ctx context.Context, key string) (store.ChatGPTSession, bool, error)
	ExpireChatGPTSession(ctx context.Context, key, token string) error
	UpdateChatGPTAllowances(ctx context.Context, key, token string, allowances []chatgpt.Allowance) error
}

// StepperBuilder resolves mutable ChatGPT credentials from the store.
func StepperBuilder(sessions PlanSessions) func(configfile.Provider) (model.Stepper, error) {
	return func(p configfile.Provider) (model.Stepper, error) {
		if p.Type != configfile.ProviderChatGPT {
			return BuildStepper(p)
		}
		tokens := planTokens{sessions: sessions, key: p.ChatGPTSessionKey()}
		return model.NewChatGPT(model.ChatGPTConfig{
			BaseURL: p.BaseURL,
			Tokens:  tokens, ObserveAllowances: tokens.observe,
		})
	}
}

type planTokens struct {
	sessions PlanSessions
	key      string
}

func (p planTokens) Token(ctx context.Context) (string, error) {
	s, ok, err := p.sessions.ChatGPTSession(ctx, p.key)
	if err != nil {
		return "", err
	}
	if !ok || s.SignedOut() || s.Credentials.AccessToken == "" {
		return "", fmt.Errorf("%w: provider %s; run kritika chatgpt login", chatgpt.ErrSignedOut, p.key)
	}
	return s.Credentials.AccessToken, nil
}

func (p planTokens) Expire(ctx context.Context, token string) error {
	return p.sessions.ExpireChatGPTSession(ctx, p.key, token)
}

func (p planTokens) observe(ctx context.Context, token string, allowances []chatgpt.Allowance) {
	// The response may finish after its caller cancels; retaining quota data
	// is best effort and must not change the outcome of the model call.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), recordTimeout)
	defer cancel()
	if err := p.sessions.UpdateChatGPTAllowances(ctx, p.key, token, allowances); err != nil {
		slog.WarnContext(ctx, "chatgpt allowances not recorded", "provider", p.key, "error", err)
	}
}
