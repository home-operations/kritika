package worker

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/home-operations/kritika/internal/chatgpt"
	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/store"
)

const chatGPTRefreshInterval = time.Minute

// ChatGPTRefresher renews configured providers' tokens. Only the elected
// leader runs it, so rotating refresh tokens have one refresh owner.
type ChatGPTRefresher struct {
	Store   *store.Store
	Current *configfile.Current
	Logger  *slog.Logger
	// Client and TokenURL may point to a token endpoint in tests.
	Client   *http.Client
	TokenURL string

	now func() time.Time
}

// Run refreshes near expiry until this leader's tenure ends.
func (r *ChatGPTRefresher) Run(ctx context.Context) {
	t := time.NewTicker(chatGPTRefreshInterval)
	defer t.Stop()
	for {
		if err := r.Refresh(ctx); err != nil && ctx.Err() == nil {
			r.Logger.Warn("chatgpt tokens not all refreshed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Refresh renews due sessions, preserving credentials on transient failures
// and clearing unusable tokens after a terminal refresh error.
func (r *ChatGPTRefresher) Refresh(ctx context.Context) error {
	configured := r.configured()
	if len(configured) == 0 {
		return nil
	}
	sessions, err := r.Store.ChatGPTSessions(ctx)
	if err != nil {
		return err
	}
	now := time.Now()
	if r.now != nil {
		now = r.now()
	}
	client := r.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	var errs []error
	for _, s := range sessions {
		c := s.Credentials
		if !configured[s.Key] || s.SignedOut() || c.RefreshToken == "" || !c.Due(now) {
			continue
		}
		renewed, err := chatgpt.Refresh(ctx, client, cmp.Or(r.TokenURL, chatgpt.TokenURL), c, now)
		switch {
		case errors.Is(err, chatgpt.ErrSignedOut):
			r.Logger.Warn("chatgpt provider disconnected; run kritika chatgpt login", "provider", s.Key)
			if err := r.Store.SignOutChatGPTSession(ctx, s.Key, c.RefreshToken, err.Error()); err != nil {
				errs = append(errs, err)
			}
		case err != nil:
			errs = append(errs, fmt.Errorf("%s: %w", s.Key, err))
		default:
			if _, err := r.Store.RefreshChatGPTSession(ctx, s.Key, renewed, c.RefreshToken); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

func (r *ChatGPTRefresher) configured() map[string]bool {
	f := r.Current.Get()
	out := map[string]bool{}
	for name, p := range f.Providers {
		if p.Type == configfile.ProviderChatGPT {
			resolved, _ := f.Provider(nil, name)
			out[resolved.ChatGPTSessionKey()] = true
		}
	}
	for i := range f.Accounts {
		a := &f.Accounts[i]
		for name, p := range a.Providers {
			if p.Type == configfile.ProviderChatGPT {
				resolved, _ := f.Provider(a, name)
				out[resolved.ChatGPTSessionKey()] = true
			}
		}
	}
	return out
}
