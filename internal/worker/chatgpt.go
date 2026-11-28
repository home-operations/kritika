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
	"github.com/home-operations/kritika/internal/tuning"
)

// ChatGPTRefresher renews configured providers' tokens. Only the elected
// leader runs it, and a new leader waits out the last one's refreshes, so
// rotating refresh tokens have one refresh owner.
type ChatGPTRefresher struct {
	Store   *store.Store
	Current *configfile.Current
	Logger  *slog.Logger
	// LeaderInterval is how often the leader pings its lock. The wait before
	// the first refresh assumes that a previous leader pinged as often. A
	// rollout that shortens the interval breaks that assumption.
	LeaderInterval time.Duration
	// Client and TokenURL can point to a token endpoint in tests.
	Client   *http.Client
	TokenURL string

	now func() time.Time
	// after stands in for time.After in the wait before the first refresh,
	// in tests.
	after func(time.Duration) <-chan time.Time
}

// Run refreshes near expiry until this leader's tenure ends. The first
// refresh waits until a previous leader's last refresh must be over. A leader
// whose lock the server dropped goes on refreshing until it notices. A
// refresh that it started by then runs on for at most tuning's
// Worker.ChatGPTRefreshTimeout. A refresh alongside it sends the refresh token
// that it just spent, and the refusal signs the provider out.
func (r *ChatGPTRefresher) Run(ctx context.Context) {
	after := time.After
	if r.after != nil {
		after = r.after
	}
	select {
	case <-ctx.Done():
		return
	case <-after(store.LeaderStepDown(r.LeaderInterval) + tuning.Get().Worker.ChatGPTRefreshTimeout):
	}
	t := time.NewTicker(tuning.Get().Worker.ChatGPTRefreshInterval)
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

// Refresh renews due sessions. It preserves credentials on transient failures
// and clears unusable tokens after a terminal refresh error.
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
		client = &http.Client{Timeout: tuning.Get().Worker.ChatGPTHTTPTimeout}
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

func (r *ChatGPTRefresher) refresh(
	leading context.Context, client *http.Client, key string, c chatgpt.Credentials, now time.Time,
) error {
	w := tuning.Get().Worker
	ctx, cancel := context.WithTimeout(context.WithoutCancel(leading), w.ChatGPTRefreshTimeout)
	defer cancel()
	// refresh makes sure that its leader still leads once the timeout runs.
	// So a refresh starts only while its leader leads, and ends within
	// tuning's Worker.ChatGPTRefreshTimeout of that.
	if err := leading.Err(); err != nil {
		return err
	}
	// The wait before a leader's first refresh assumes its predecessor had
	// the same timeout. The claim does not: a predecessor's refresh still
	// in flight holds it, and this one leaves the session to it. It is held
	// for twice the timeout, so that a clock off from the database's does
	// not end it while this refresh runs.
	until, claimed, err := r.Store.ClaimChatGPTRefresh(ctx, key, c.RefreshToken, 2*w.ChatGPTRefreshTimeout)
	if err != nil || !claimed {
		return err
	}
	defer func() {
		// Best effort: a claim left behind ends on its own.
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), w.ChatGPTRefreshTimeout)
		defer cancel()
		_ = r.Store.ReleaseChatGPTRefresh(rctx, key, until)
	}()
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
		if err == nil || attempt == w.ChatGPTSaveAttempts {
			return err
		}
		select {
		case <-ctx.Done():
			return errors.Join(err, ctx.Err())
		case <-time.After(time.Duration(attempt) * w.ChatGPTSaveBackoff):
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
