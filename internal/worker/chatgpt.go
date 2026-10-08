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

// chatGPTRefreshInterval is how often the leader looks at the plans'
// tokens; an access token lasts an hour and is renewed with five minutes
// to spare, so a minute's granularity is plenty.
const chatGPTRefreshInterval = time.Minute

// chatGPTClientTimeout bounds one refresh.
const chatGPTClientTimeout = 30 * time.Second

// ChatGPTRefresher keeps every chatgpt provider's token set current. One
// process refreshes, as OpenAI asks of a rotating refresh token, and
// every replica reads the set it writes; a refresh token OpenAI no longer
// takes signs the provider out, which its steps report until kritika
// chatgpt login runs again. A leader duty.
type ChatGPTRefresher struct {
	Store   *store.Store
	Current *configfile.Current
	Logger  *slog.Logger
	// Client refreshes with; nil means a default. TokenURL is OpenAI's
	// unless a test sets its own.
	Client   *http.Client
	TokenURL string

	every time.Duration
	now   func() time.Time
}

// Run makes a pass at once, then every chatGPTRefreshInterval until ctx
// ends. A pass that fails in part is logged and tried again next time.
func (r *ChatGPTRefresher) Run(ctx context.Context) {
	t := time.NewTicker(cmp.Or(r.every, chatGPTRefreshInterval))
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

// Refresh makes one pass. Each configured chatgpt provider's record is
// seeded, which puts a new sign-in's set in place and leaves a seeded one
// alone; then every configured plan's set within chatgpt.RefreshLead of
// its expiry is renewed, and one OpenAI refuses to renew is signed out. A
// set another process renewed meanwhile is left as it is; a renewal that
// failed for a passing reason is tried next pass.
func (r *ChatGPTRefresher) Refresh(ctx context.Context) error {
	configured := map[string]bool{}
	var errs []error
	for _, c := range r.configured() {
		configured[c.ClientID] = true
		if err := r.Store.SeedChatGPTSession(ctx, c); err != nil {
			errs = append(errs, err)
		}
	}
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
		client = &http.Client{Timeout: chatGPTClientTimeout}
	}
	for _, s := range sessions {
		c := s.Credentials
		if !configured[c.ClientID] || s.SignedOut() || !c.Due(now) {
			continue
		}
		renewed, err := chatgpt.Refresh(ctx, client, cmp.Or(r.TokenURL, chatgpt.TokenURL), c, now)
		switch {
		case errors.Is(err, chatgpt.ErrSignedOut):
			r.Logger.Warn("chatgpt plan signed out; run kritika chatgpt login again", "client_id", c.ClientID, "error", err)
			if err := r.Store.SignOutChatGPTSession(ctx, c.ClientID, err.Error()); err != nil {
				errs = append(errs, err)
			}
		case err != nil:
			errs = append(errs, fmt.Errorf("%s: %w", c.ClientID, err))
		default:
			replaced, err := r.Store.RefreshChatGPTSession(ctx, renewed, c.RefreshToken)
			if err != nil {
				errs = append(errs, err)
			} else if !replaced {
				r.Logger.Warn("chatgpt tokens renewed elsewhere meanwhile; keeping those", "client_id", c.ClientID)
			}
		}
	}
	return errors.Join(errs...)
}

// configured is the record of every chatgpt provider the running
// configuration declares, the instance's and the accounts' own.
func (r *ChatGPTRefresher) configured() []chatgpt.Credentials {
	f := r.Current.Get()
	var out []chatgpt.Credentials
	add := func(providers map[string]configfile.Provider) {
		for _, p := range providers {
			if p.Type == configfile.ProviderChatGPT {
				out = append(out, p.ChatGPTCredentials())
			}
		}
	}
	add(f.Providers)
	for i := range f.Accounts {
		add(f.Accounts[i].Providers)
	}
	return out
}
