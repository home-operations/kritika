package chatgpt

import (
	"cmp"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/google/uuid"
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

// Login is one Sign in with ChatGPT on the operator's machine, where the
// browser is: it registers kritika as a client of the chosen account,
// asks for plan usage, and writes the credential record the chatgpt
// provider's credentials reference holds. OpenAI's callback is a loopback
// address, which is why it runs here and not in the pod.
type Login struct {
	// Issuer is OpenAI's authorization server; a test sets its own.
	Issuer string
	// Client makes the requests; nil means the default.
	Client *http.Client
	// Out is where the URL to open and the outcome go.
	Out io.Writer
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// callback is what the browser brought back.
type callback struct {
	code, clientID, scope string
}

// Run signs in and writes the record to path, readable by its owner only.
// It opens nothing itself: it prints the URL for the operator to open and
// waits for the browser to come back, until ctx ends.
func (l Login) Run(ctx context.Context, path string) (Credentials, error) {
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
	state, nonce, verifier := random(), random(), oauth2.GenerateVerifier()
	hostID := "urn:uuid:" + uuid.NewString()

	// The listener is up before the URL is printed, so the browser's
	// return finds it; its port is whatever is free, which OpenAI allows.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return Credentials{}, fmt.Errorf("chatgpt: login: %w", err)
	}
	redirect := "http://" + listener.Addr().String() + callbackPath
	authorize := discovered.Endpoint().AuthURL + "?" + url.Values{
		"client_id": {registrationClient}, "agent_name_hint": {agentName}, "ext_agent_host_id": {hostID},
		"response_type": {"code"}, "redirect_uri": {redirect}, "scope": {strings.Join(scopes, " ")}, "resource": {Resource},
		"state": {state}, "nonce": {nonce}, "code_challenge_method": {"S256"}, "code_challenge": {oauth2.S256ChallengeFromVerifier(verifier)},
	}.Encode()
	l.say("Open this URL in your browser to sign in with ChatGPT and let kritika use your plan:\n\n  %s\n\n"+
		"Waiting for the browser to come back...\n", authorize)

	cb, err := awaitCallback(ctx, listener, state)
	if err != nil {
		return Credentials{}, err
	}
	conf := oauth2.Config{
		ClientID:    cb.clientID,
		Endpoint:    oauth2.Endpoint{TokenURL: discovered.Endpoint().TokenURL, AuthStyle: oauth2.AuthStyleInParams},
		RedirectURL: redirect,
	}
	tok, err := conf.Exchange(ctx, cb.code, oauth2.VerifierOption(verifier), oauth2.SetAuthURLParam("resource", Resource))
	if err != nil {
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
	if err := writeRecord(path, c); err != nil {
		return Credentials{}, err
	}
	l.say("\nSigned in as %s. The credentials are in %s, readable by you alone.\n"+
		"Give them to kritika as the variable a chatgpt provider's credentials reference names, from a Secret:\n\n"+
		"  kubectl create secret generic kritika-chatgpt --from-file=CHATGPT_CREDENTIALS=%s\n", claims.Email, path, path)
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
func awaitCallback(ctx context.Context, listener net.Listener, state string) (callback, error) {
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
		var out outcome
		switch {
		case q.Get("error") != "":
			out.err = fmt.Errorf("chatgpt: login: the sign-in was refused: %s", cmp.Or(q.Get("error_description"), q.Get("error")))
		case q.Get("code") == "":
			out.err = errors.New("chatgpt: login: the callback carries no code")
		case q.Get("client_id") == "" || q.Get("client_id") == registrationClient:
			out.err = errors.New("chatgpt: login: the sign-in registered no client; try again")
		default:
			out.cb = callback{code: q.Get("code"), clientID: q.Get("client_id"), scope: q.Get("scope")}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if out.err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, "<p>The sign-in did not complete. Go back to the terminal.</p>")
		} else {
			_, _ = io.WriteString(w, "<p>kritika is signed in. You can close this window.</p>")
		}
		select {
		case done <- out:
		default:
		}
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(listener) }()
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		_ = srv.Shutdown(closeCtx)
	}()
	select {
	case out := <-done:
		return out.cb, out.err
	case <-ctx.Done():
		return callback{}, fmt.Errorf("chatgpt: login: the browser did not come back: %w", ctx.Err())
	}
}

// writeRecord writes c to path, owner-readable, through a temporary file
// in the same directory so a reader never sees half a record.
func writeRecord(path string, c Credentials) error {
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("chatgpt: login: encode: %w", err)
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return fmt.Errorf("chatgpt: login: write: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("chatgpt: login: write: %w", err)
	}
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		return fmt.Errorf("chatgpt: login: write: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("chatgpt: login: write: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("chatgpt: login: write: %w", err)
	}
	return nil
}

// random is a fresh value for a state or a nonce.
func random() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // the system's randomness is gone; nothing to sign in with
	}
	return hex.EncodeToString(b[:])
}
