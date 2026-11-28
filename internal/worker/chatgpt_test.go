package worker

import (
	"context"
	"testing"
	"time"

	"github.com/home-operations/kritika/internal/store"
	"github.com/home-operations/kritika/internal/tuning"
)

// TestChatGPTFirstRefreshWaitsTheLegacyTimeout: a leader whose refresh
// timeout is shorter than the one from before it was tuned still waits that
// one out, for a predecessor that refreshed within it and took no claim.
func TestChatGPTFirstRefreshWaitsTheLegacyTimeout(t *testing.T) {
	for _, tt := range []struct {
		timeout, want time.Duration
	}{
		{10 * time.Second, legacyRefreshTimeout},
		{3 * time.Minute, 3 * time.Minute},
	} {
		tuning.Override(t, func(v *tuning.Values) { v.Worker.ChatGPTRefreshTimeout = tt.timeout })
		ctx, cancel := context.WithCancel(t.Context())
		var waited time.Duration
		r := &ChatGPTRefresher{LeaderInterval: 15 * time.Second, after: func(d time.Duration) <-chan time.Time {
			waited = d
			cancel()
			return nil
		}}
		r.Run(ctx)
		if want := store.LeaderStepDown(15*time.Second) + tt.want; waited != want {
			t.Errorf("with a %s refresh timeout, the first refresh waits %s, want %s", tt.timeout, waited, want)
		}
	}
}

// TestChatGPTFirstRefreshWaitsTheLegacyPing: a leader with a lock timeout
// shorter than the one from before it was tuned still waits out a
// predecessor's ping at that one.
func TestChatGPTFirstRefreshWaitsTheLegacyPing(t *testing.T) {
	tuning.Override(t, func(v *tuning.Values) {
		v.Store.LockTimeout, v.Worker.ChatGPTRefreshTimeout = time.Second, 10*time.Second
	})
	ctx, cancel := context.WithCancel(t.Context())
	var waited time.Duration
	r := &ChatGPTRefresher{LeaderInterval: 15 * time.Second, after: func(d time.Duration) <-chan time.Time {
		waited = d
		cancel()
		return nil
	}}
	r.Run(ctx)
	if want := 15*time.Second + 5*time.Second + time.Minute; waited != want {
		t.Fatalf("the first refresh waits %s, want %s: the interval, the legacy 5s ping and the legacy minute", waited, want)
	}
}
