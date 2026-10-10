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

// chatGPTRefreshTimeout bounds one session's refresh and the write that
// saves it, which run on past the leader's cancellation: once OpenAI
// answers, the refresh token it rotated is spent, and only the saved
// replacement can renew the session again.
const chatGPTRefreshTimeout = time.Minute

// chatGPTSaveAttempts is how many times a refreshed token set is written
// before it is given up, a second apart and more each time.
const chatGPTSaveAttempts = 4

// Refresh renews due sessions, preserving credentials on transient failures
// and clearing unusable tokens after a terminal refresh error.
func (r *ChatGPTRefresher) Refresh(ctx context.Context) error {
	configured := r.configured()
	if len(configured) == 0 {
		return nil
	}
	sessions, err := r.Store.ChatGPTSessions(ctx)
	if err != nil {
		return fmt.Errorf("worker: chatgpt refresh: %w", err)
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
		if ctx.Err() != nil {
			break
		}
		c := s.Credentials
		if !configured[s.Key] || s.SignedOut() || c.RefreshToken == "" || !c.Due(now) {
			continue
		}
		if err := r.refresh(ctx, client, s.Key, c, now); err != nil {
			errs = append(errs, fmt.Errorf("worker: chatgpt refresh of %s: %w", s.Key, err))
		}
	}
	return errors.Join(errs...)
}

func (r *ChatGPTRefresher) refresh(ctx context.Context, client *http.Client, key string, c chatgpt.Credentials, now time.Time) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), chatGPTRefreshTimeout)
	defer cancel()
	renewed, err := chatgpt.Refresh(ctx, client, cmp.Or(r.TokenURL, chatgpt.TokenURL), c, now)
	if errors.Is(err, chatgpt.ErrSignedOut) {
		r.Logger.Warn("chatgpt provider disconnected; run kritika chatgpt login", "provider", key)
		return r.Store.SignOutChatGPTSession(ctx, key, c.RefreshToken, err.Error())
	}
	if err != nil {
		return err
	}
	for attempt := 1; ; attempt++ {
		_, err := r.Store.RefreshChatGPTSession(ctx, key, renewed, c.RefreshToken)
		if err == nil || attempt == chatGPTSaveAttempts {
			return err
		}
		select {
		case <-ctx.Done():
			return errors.Join(err, ctx.Err())
		case <-time.After(time.Duration(attempt) * time.Second):
		}
	}
}

func (r *ChatGPTRefresher) configured() map[string]bool {
	f := r.Current.Get()
	out := map[string]bool{}
	add := func(t *configfile.Account) {
		for _, p := range f.ChatGPTProviders(t) {
			out[p.ChatGPTSessionKey()] = true
		}
	}
	add(nil)
	for i := range f.Accounts {
		add(&f.Accounts[i])
	}
	return out
}
