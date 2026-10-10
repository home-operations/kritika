package chatgpt

import (
	"cmp"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// The sign-in's fixed parameters: the registration entrypoint, the name
// the user sees for kritika, and the scopes a review needs.
const (
	registrationClient = "dynamic_agent_client"
	agentName          = "kritika"
	callbackPath       = "/auth/callback"
)

// scopes are the identity scopes and the plan usage scopes, which the
// token response grants together or not at all.
var scopes = []string{oidc.ScopeOpenID, "profile", "email", "offline_access", "resource.invoke", PlanScope}

// Login runs Sign in with ChatGPT on a loopback listener. An operator
// forwards its port from the browser's machine when running it in a pod.
type Login struct {
	// Addr is the loopback listener, 127.0.0.1:1455 unless a test sets it.
	Addr string
	// Issuer is OpenAI's authorization server; a test sets its own.
	Issuer string
	// Client makes the requests; nil means the default.
	Client *http.Client
	// Out is where the URL to open and the outcome go.
	Out io.Writer
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	// Registered, if set, saves the client ID a first sign-in's callback
	// issued, before the code exchange: OpenAI asks that a failed attempt
	// be repeated as that client rather than register another.
	Registered func(ctx context.Context, clientID string) error
}

// callback is what the browser brought back.
type callback struct {
	code, clientID string
}

// Run signs in with the saved registration and host ID, or registers a
// new client when current has none. The caller persists the validated set.
func (l Login) Run(ctx context.Context, current Credentials) (Credentials, error) {
	if current.HostID == "" {
		return Credentials{}, errors.New("chatgpt: login: a persisted host ID is required")
	}
	issuer, client, now := l.Issuer, l.Client, l.Now
	if issuer == "" {
		issuer = Issuer
	}
	if client == nil {
		client = &http.Client{Timeout: clientTimeout}
	}
	if now == nil {
		now = time.Now
	}
	ctx = oidc.ClientContext(ctx, client)
	discovered, err := oidc.NewProvider(ctx, issuer)
	if err != nil {
		return Credentials{}, fmt.Errorf("chatgpt: login: discovery: %w", err)
	}
	state, nonce, verifier := rand.Text(), rand.Text(), oauth2.GenerateVerifier()
	hostID := current.HostID

	listener, err := net.Listen("tcp", cmp.Or(l.Addr, "127.0.0.1:1455"))
	if err != nil {
		return Credentials{}, fmt.Errorf("chatgpt: login: %w", err)
	}
	redirect := "http://" + listener.Addr().String() + callbackPath
	params := url.Values{
		"client_id": {cmp.Or(current.ClientID, registrationClient)}, "ext_agent_host_id": {hostID},
		"response_type": {"code"}, "redirect_uri": {redirect}, "scope": {strings.Join(scopes, " ")}, "resource": {Resource},
		"state": {state}, "nonce": {nonce}, "code_challenge_method": {"S256"}, "code_challenge": {oauth2.S256ChallengeFromVerifier(verifier)},
	}
	if current.ClientID == "" {
		params.Set("agent_name_hint", agentName)
	} else {
		if current.IDToken != "" {
			params.Set("id_token_hint", current.IDToken)
		}
		if current.Email != "" {
			params.Set("login_hint", current.Email)
		}
	}
	authorize := discovered.Endpoint().AuthURL + "?" + params.Encode()
	l.say("Open this URL in your browser to sign in with ChatGPT and let kritika use your plan:\n\n  %s\n\n"+
		"Waiting for the browser to come back...\n", authorize)

	cb, err := awaitCallback(ctx, listener, state, current.ClientID)
	if err != nil {
		return Credentials{}, err
	}
	if current.ClientID == "" && l.Registered != nil {
		if err := l.Registered(ctx, cb.clientID); err != nil {
			return Credentials{}, fmt.Errorf("chatgpt: login: save the registration: %w", err)
		}
	}
	conf := oauth2.Config{
		ClientID:    cb.clientID,
		Endpoint:    oauth2.Endpoint{TokenURL: discovered.Endpoint().TokenURL, AuthStyle: oauth2.AuthStyleInParams},
		RedirectURL: redirect,
	}
	tok, err := conf.Exchange(ctx, cb.code, oauth2.VerifierOption(verifier), oauth2.SetAuthURLParam("resource", Resource))
	if err != nil {
		if e, ok := errors.AsType[*oauth2.RetrieveError](err); ok {
			// RetrieveError includes the raw response body, which may carry tokens.
			return Credentials{}, fmt.Errorf("chatgpt: login: exchange: HTTP %d: %s", e.Response.StatusCode, e.ErrorCode)
		}
		return Credentials{}, fmt.Errorf("chatgpt: login: exchange: %w", err)
	}
	rawID, _ := tok.Extra("id_token").(string)
	if rawID == "" {
		return Credentials{}, errors.New("chatgpt: login: the token response has no id_token")
	}
	idt, err := discovered.Verifier(&oidc.Config{ClientID: cb.clientID, Now: now}).Verify(ctx, rawID)
	if err != nil {
		return Credentials{}, fmt.Errorf("chatgpt: login: id token: %w", err)
	}
	if subtle.ConstantTimeCompare([]byte(idt.Nonce), []byte(nonce)) != 1 {
		return Credentials{}, errors.New("chatgpt: login: id token: nonce mismatch")
	}
	if idt.Subject == "" {
		return Credentials{}, errors.New("chatgpt: login: id token: subject is missing")
	}
	// A registration whose first sign-in failed has no identity yet.
	if current.Subject != "" && idt.Subject != current.Subject {
		return Credentials{}, errors.New("chatgpt: login: the signed-in identity differs from the saved account")
	}
	var claims struct {
		Email string `json:"email"`
	}
	if err := idt.Claims(&claims); err != nil {
		return Credentials{}, fmt.Errorf("chatgpt: login: id token: claims: %w", err)
	}
	granted, _ := tok.Extra("scope").(string)
	c := Credentials{
		Email: claims.Email, Issuer: issuer, Subject: idt.Subject, ClientID: cb.clientID, HostID: hostID, IDToken: rawID,
		AccessToken: tok.AccessToken, RefreshToken: tok.RefreshToken, TokenType: tok.TokenType, ExpiresIn: tok.ExpiresIn,
		Scopes: strings.Fields(granted), SavedAt: now(),
	}
	if err := c.Validate(); err != nil {
		return Credentials{}, fmt.Errorf("chatgpt: login: %w", err)
	}
	return c, nil
}

// say prints to the operator; a terminal that cannot be written to is not
// the sign-in's concern.
func (l Login) say(format string, args ...any) { _, _ = fmt.Fprintf(l.Out, format, args...) }

// awaitCallback serves the loopback listener until the browser brings a
// callback for state, or ctx ends. A request with another state, or none,
// is refused and the wait goes on: a stray request must not end the
// sign-in. A callback that reports an OAuth error, or registers no
// client, ends it.
func awaitCallback(ctx context.Context, listener net.Listener, state, clientID string) (callback, error) {
	type outcome struct {
		cb  callback
		err error
	}
	done := make(chan outcome, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+callbackPath, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(state)) != 1 {
			http.Error(w, "this callback is not for the sign-in in progress", http.StatusBadRequest)
			return
		}
		issued := cmp.Or(q.Get("client_id"), clientID)
		var out outcome
		switch {
		case q.Get("error") != "":
			out.err = fmt.Errorf("chatgpt: login: the sign-in was refused: %s", q.Get("error"))
		case q.Get("code") == "":
			out.err = errors.New("chatgpt: login: the callback carries no code")
		case issued == "" || issued == registrationClient:
			out.err = errors.New("chatgpt: login: the sign-in registered no client; try again")
		case clientID != "" && issued != clientID:
			out.err = errors.New("chatgpt: login: the callback changed the saved client ID")
		default:
			out.cb = callback{code: q.Get("code"), clientID: issued}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if out.err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, "<p>The sign-in did not complete. Go back to the terminal.</p>")
		} else {
			_, _ = io.WriteString(w, "<p>Callback received. Return to the terminal to confirm the sign-in completes.</p>")
		}
		select {
		case done <- out:
		default:
		}
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(listener) }()
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		if err := srv.Shutdown(closeCtx); err != nil {
			_ = srv.Close() // best-effort cleanup after the shutdown deadline
		}
	}()
	select {
	case out := <-done:
		return out.cb, out.err
	case err := <-serveErr:
		return callback{}, fmt.Errorf("chatgpt: login: callback listener: %w", err)
	case <-ctx.Done():
		return callback{}, fmt.Errorf("chatgpt: login: the browser did not come back: %w", ctx.Err())
	}
}
