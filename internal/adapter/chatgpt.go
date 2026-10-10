package adapter

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/home-operations/kritika/internal/chatgpt"
	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/store"
)

// planTokenTTL bounds how long a replica keeps using an access token it
// read without reading it again, so a new sign-in reaches every replica
// within it.
const planTokenTTL = time.Minute

// PlanSessions exposes the credentials the leader keeps current.
type PlanSessions interface {
	ChatGPTSession(ctx context.Context, key string) (store.ChatGPTSession, bool, error)
	ExpireChatGPTSession(ctx context.Context, key, token string) error
	UpdateChatGPTAllowances(ctx context.Context, key string, allowances []chatgpt.Allowance) error
}

// StepperBuilder resolves mutable ChatGPT credentials from the store.
func StepperBuilder(sessions PlanSessions) func(configfile.Provider) (model.Stepper, error) {
	return func(p configfile.Provider) (model.Stepper, error) {
		if p.Type != configfile.ProviderChatGPT {
			return BuildStepper(p)
		}
		tokens := &planTokens{sessions: sessions, key: p.ChatGPTSessionKey()}
		return model.NewChatGPT(model.ChatGPTConfig{
			BaseURL: p.BaseURL, Provider: tokens.key,
			Tokens: tokens, ObserveAllowances: tokens.observe,
		})
	}
}

// planTokens reads a provider's access token from the store and keeps it
// until the leader is due to renew it, rather than reading it on every
// step.
type planTokens struct {
	sessions PlanSessions
	key      string
	now      func() time.Time

	mu    sync.Mutex
	token string
	until time.Time
}

func (p *planTokens) Token(ctx context.Context) (string, error) {
	now := time.Now()
	if p.now != nil {
		now = p.now()
	}
	p.mu.Lock()
	token, current := p.token, now.Before(p.until)
	p.mu.Unlock()
	if token != "" && current {
		return token, nil
	}
	s, ok, err := p.sessions.ChatGPTSession(ctx, p.key)
	if err != nil {
		return "", err
	}
	if !ok || s.SignedOut() || s.Credentials.AccessToken == "" {
		return "", fmt.Errorf("%w: provider %s; run kritika chatgpt login", chatgpt.ErrSignedOut, p.key)
	}
	c := s.Credentials
	until := now.Add(planTokenTTL)
	if due := c.Expiry().Add(-chatgpt.RefreshLead); due.Before(until) {
		until = due
	}
	p.mu.Lock()
	p.token, p.until = c.AccessToken, until
	p.mu.Unlock()
	return c.AccessToken, nil
}

func (p *planTokens) Expire(ctx context.Context, token string) error {
	p.mu.Lock()
	if p.token == token {
		p.token = ""
	}
	p.mu.Unlock()
	return p.sessions.ExpireChatGPTSession(ctx, p.key, token)
}

// observe records quota data off the step's path: it is best effort and
// must neither delay nor change the outcome of the model call, which may
// finish after its caller cancels.
func (p *planTokens) observe(ctx context.Context, allowances []chatgpt.Allowance) {
	go func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), recordTimeout)
		defer cancel()
		if err := p.sessions.UpdateChatGPTAllowances(ctx, p.key, allowances); err != nil {
			slog.WarnContext(ctx, "chatgpt allowances not recorded", "provider", p.key, "error", err)
		}
	}()
}
