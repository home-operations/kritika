package adapter

import (
	"context"
	"fmt"
	"time"

	"github.com/home-operations/kritika/internal/chatgpt"
	"github.com/home-operations/kritika/internal/store"
)

// PlanSessions is the store's view of a plan's live token set, which the
// leader keeps current (worker.ChatGPTRefresher).
type PlanSessions interface {
	ChatGPTSession(ctx context.Context, clientID string) (store.ChatGPTSession, bool, error)
	ExpireChatGPTSession(ctx context.Context, clientID string) error
}

// planTokens hands a chatgpt adapter the plan's current access token: the
// store's, or the configured record's until the leader has seeded it.
type planTokens struct {
	sessions PlanSessions
	seed     chatgpt.Credentials
}

// Token implements model.TokenSource.
func (p planTokens) Token(ctx context.Context) (string, error) {
	s, ok, err := p.sessions.ChatGPTSession(ctx, p.seed.ClientID)
	if err != nil {
		return "", err
	}
	if !ok {
		return p.seed.AccessToken, nil
	}
	if s.SignedOut() {
		return "", fmt.Errorf("%w (%s); run kritika chatgpt login again", chatgpt.ErrSignedOut, s.SignedOutReason)
	}
	return s.Credentials.AccessToken, nil
}

// expireTimeout bounds the nudge Expire gives the store.
const expireTimeout = 5 * time.Second

// Expire implements model.TokenSource: it asks the leader to renew the
// token on its next pass. Best effort, since the leader renews it within
// the hour regardless.
func (p planTokens) Expire() {
	ctx, cancel := context.WithTimeout(context.Background(), expireTimeout)
	defer cancel()
	_ = p.sessions.ExpireChatGPTSession(ctx, p.seed.ClientID)
}
