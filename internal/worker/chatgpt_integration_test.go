//go:build integration

package worker

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/home-operations/kritika/internal/chatgpt"
	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/store"
	"github.com/home-operations/kritika/internal/store/storetest"
)

func TestChatGPTRefresher(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	now := time.Now().UTC()
	f := &configfile.File{Providers: map[string]configfile.Provider{}}
	keys := map[string]string{}
	for _, name := range []string{"fresh", "due", "refused", "transient", "unconfigured", "pending"} {
		key := name + "-" + uuid.NewString()
		keys[name] = key
		if name != "unconfigured" {
			f.Providers[key] = configfile.Provider{Type: configfile.ProviderChatGPT}
		}
		s, err := st.PrepareChatGPTSession(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		if name == "pending" {
			continue
		}
		c := s.Credentials
		c.ClientID, c.AccessToken, c.RefreshToken = "oaiapp_"+name, "at-"+name, "rt-"+name
		c.Scopes, c.ExpiresIn, c.SavedAt = []string{chatgpt.PlanScope}, 3600, now.Add(-56*time.Minute)
		if name == "fresh" {
			c.SavedAt = now
		}
		if err := st.ConnectChatGPTSession(ctx, key, c, ""); err != nil {
			t.Fatal(err)
		}
	}
	var requests atomic.Int32
	tokens := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.PostForm.Get("refresh_token") {
		case "rt-due":
			_, _ = fmt.Fprint(w, `{"access_token":"at-due-2","refresh_token":"rt-due-2","expires_in":3600}`)
		case "rt-refused":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprint(w, `{"error":"refresh_token_reused"}`)
		default:
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	t.Cleanup(tokens.Close)
	r := &ChatGPTRefresher{
		Store: st, Current: configfile.NewCurrent(f), Logger: slog.New(slog.DiscardHandler),
		Client: tokens.Client(), TokenURL: tokens.URL, now: func() time.Time { return now },
	}
	if err := r.Refresh(ctx); err == nil {
		t.Fatal("the transient refresh failure was not reported")
	}
	if requests.Load() != 3 {
		t.Fatalf("refresh requests = %d, want 3", requests.Load())
	}
	for _, tt := range []struct {
		name      string
		refresh   string
		signedOut bool
	}{
		{"fresh", "rt-fresh", false}, {"due", "rt-due-2", false}, {"refused", "", true},
		{"transient", "rt-transient", false}, {"unconfigured", "rt-unconfigured", false}, {"pending", "", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, ok, err := st.ChatGPTSession(ctx, keys[tt.name])
			if err != nil || !ok || s.Credentials.RefreshToken != tt.refresh || s.SignedOut() != tt.signedOut {
				t.Fatalf("session = %+v, %v, %v", s, ok, err)
			}
		})
	}
	if err := r.Refresh(ctx); err == nil || requests.Load() != 4 {
		t.Fatalf("second pass = %v, %d requests; only the transient failure should retry", err, requests.Load())
	}
}

// TestChatGPTRefresherOutlivesTenure: a refresh that OpenAI answered is saved
// even when the leader's tenure ends meanwhile, since the refresh token it
// replaced is spent.
func TestChatGPTRefresherOutlivesTenure(t *testing.T) {
	st := storetest.Open(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	now := time.Now().UTC()
	key := "tenure-" + uuid.NewString()
	s, err := st.PrepareChatGPTSession(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	c := s.Credentials
	c.ClientID, c.AccessToken, c.RefreshToken = "oaiapp_tenure", "at-1", "rt-1"
	c.Scopes, c.ExpiresIn, c.SavedAt = []string{chatgpt.PlanScope}, 3600, now.Add(-56*time.Minute)
	if err := st.ConnectChatGPTSession(ctx, key, c, ""); err != nil {
		t.Fatal(err)
	}
	tokens := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		cancel()
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"access_token":"at-2","refresh_token":"rt-2","expires_in":3600}`)
	}))
	t.Cleanup(tokens.Close)
	f := &configfile.File{Providers: map[string]configfile.Provider{key: {Type: configfile.ProviderChatGPT}}}
	r := &ChatGPTRefresher{
		Store: st, Current: configfile.NewCurrent(f), Logger: slog.New(slog.DiscardHandler),
		Client: tokens.Client(), TokenURL: tokens.URL, now: func() time.Time { return now },
	}
	if err := r.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	saved, _, err := st.ChatGPTSession(t.Context(), key)
	if err != nil || saved.Credentials.RefreshToken != "rt-2" || saved.SignedOut() {
		t.Fatalf("session after the tenure ended = %+v, %v", saved, err)
	}
}

// TestChatGPTRefresherWaitsOutThePreviousLeader: a new leader's first refresh
// waits until a previous leader's refresh in flight saves. So the refresh
// token that it rotated is not sent twice, and the provider stays signed in.
func TestChatGPTRefresherWaitsOutThePreviousLeader(t *testing.T) {
	st := storetest.Open(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	now := time.Now().UTC()
	key := "handover-" + uuid.NewString()
	s, err := st.PrepareChatGPTSession(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	c := s.Credentials
	c.ClientID, c.AccessToken, c.RefreshToken = "oaiapp_handover", "at-1", "rt-1"
	c.Scopes, c.ExpiresIn, c.SavedAt = []string{chatgpt.PlanScope}, 3600, now.Add(-56*time.Minute)
	if err := st.ConnectChatGPTSession(ctx, key, c, ""); err != nil {
		t.Fatal(err)
	}
	// rt-1 is answered once, when released, with tokens already due again.
	// Sent again, it is refused as reused, as OpenAI refuses it.
	inFlight, release, reused := make(chan struct{}), make(chan struct{}), make(chan struct{})
	free, refuse := sync.OnceFunc(func() { close(release) }), sync.OnceFunc(func() { close(reused) })
	defer free()
	renewed := make(chan string, 1)
	var answered atomic.Bool
	tokens := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch rt := r.PostForm.Get("refresh_token"); {
		case rt == "rt-1" && answered.CompareAndSwap(false, true):
			close(inFlight)
			<-release
			_, _ = fmt.Fprint(w, `{"access_token":"at-2","refresh_token":"rt-2","expires_in":60}`)
		case rt == "rt-1":
			refuse()
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprint(w, `{"error":"refresh_token_reused"}`)
		default:
			select {
			case renewed <- rt:
			default:
			}
			_, _ = fmt.Fprint(w, `{"access_token":"at-3","refresh_token":"rt-3","expires_in":3600}`)
		}
	}))
	t.Cleanup(tokens.Close)
	f := &configfile.File{Providers: map[string]configfile.Provider{key: {Type: configfile.ProviderChatGPT}}}
	leader := func() *ChatGPTRefresher {
		return &ChatGPTRefresher{
			Store: st, Current: configfile.NewCurrent(f), Logger: slog.New(slog.DiscardHandler), LeaderInterval: 15 * time.Second,
			Client: tokens.Client(), TokenURL: tokens.URL, now: func() time.Time { return now },
		}
	}
	previous := make(chan error, 1)
	go func() { previous <- leader().Refresh(ctx) }()
	select {
	case <-inFlight:
	case <-ctx.Done():
		t.Fatal("the previous leader never refreshed")
	}
	next, waiting, waited := leader(), make(chan time.Duration, 1), make(chan time.Time)
	next.after = func(d time.Duration) <-chan time.Time {
		waiting <- d
		return waited
	}
	runCtx, stop := context.WithCancel(ctx)
	ran := make(chan struct{})
	go func() {
		defer close(ran)
		next.Run(runCtx)
	}()
	defer func() {
		stop()
		<-ran
	}()
	select {
	case d := <-waiting:
		if want := store.LeaderStepDown(15*time.Second) + chatGPTRefreshTimeout; d != want {
			t.Fatalf("the new leader waits %v, want %v", d, want)
		}
	case <-reused:
		t.Fatal("the new leader sent rt-1 while the previous leader's refresh of it was in flight")
	case <-ctx.Done():
		t.Fatal("the new leader neither waited nor refreshed")
	}
	free()
	if err := <-previous; err != nil {
		t.Fatalf("the previous leader's refresh: %v", err)
	}
	saved, _, err := st.ChatGPTSession(ctx, key)
	if err != nil || saved.Credentials.RefreshToken != "rt-2" || saved.SignedOut() {
		t.Fatalf("session after the previous leader's refresh = %+v, %v", saved, err)
	}
	close(waited)
	select {
	case rt := <-renewed:
		if rt != "rt-2" {
			t.Fatalf("the new leader refreshed %s, want rt-2", rt)
		}
	case <-reused:
		t.Fatal("the new leader sent rt-1 after the previous leader saved rt-2")
	case <-ctx.Done():
		t.Fatal("the new leader never refreshed")
	}
}
