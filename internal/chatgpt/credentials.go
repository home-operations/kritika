// Package chatgpt is kritika's side of Sign in with ChatGPT: the credential
// record a ChatGPT Plus or Pro plan's sign-in issues, which a chatgpt
// provider's credentials reference holds, and the session that keeps the
// record's access token current for the provider's adapter.
package chatgpt

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

// OpenAI's authorization server, and the resource a plan's tokens are for.
const (
	Issuer   = "https://auth.openai.com"
	TokenURL = Issuer + "/api/accounts/oauth/token"
	Resource = "https://api.openai.com/v1"
	// PlanScope is the scope that lets an app's requests draw on the plan;
	// a sign-in without it is an identity and nothing to review with.
	PlanScope = "chatgpt.tokens.use.direct"
)

// Credentials is one sign-in's record, in the shape OpenAI's docs give a
// local credential file: the client the sign-in registered, its tokens,
// and the account they are for. The access token lasts an hour; the
// refresh token thirty days, and every refresh replaces it.
type Credentials struct {
	Email        string    `json:"email,omitempty"`
	Issuer       string    `json:"issuer"`
	Subject      string    `json:"subject,omitempty"`
	ClientID     string    `json:"client_id"`
	HostID       string    `json:"ext_agent_host_id"`
	IDToken      string    `json:"id_token,omitempty"`
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	TokenType    string    `json:"token_type"`
	ExpiresIn    int64     `json:"expires_in"`
	Scopes       []string  `json:"scopes"`
	SavedAt      time.Time `json:"saved_at"`
}

// ParseCredentials reads a record and checks it has what a request and a
// refresh need.
func ParseCredentials(raw []byte) (Credentials, error) {
	var c Credentials
	if err := json.Unmarshal(raw, &c); err != nil {
		return Credentials{}, fmt.Errorf("chatgpt: credentials: %w", err)
	}
	if err := c.Validate(); err != nil {
		return Credentials{}, err
	}
	return c, nil
}

// Validate checks that c names its client, carries both tokens and was
// granted plan usage.
func (c Credentials) Validate() error {
	switch {
	case c.ClientID == "":
		return errors.New("chatgpt: credentials: client_id is missing")
	case c.AccessToken == "":
		return errors.New("chatgpt: credentials: access_token is missing")
	case c.RefreshToken == "":
		return errors.New("chatgpt: credentials: refresh_token is missing")
	}
	if !slices.Contains(c.Scopes, PlanScope) {
		return fmt.Errorf("chatgpt: credentials: the sign-in did not grant %s, so the plan cannot be used", PlanScope)
	}
	return nil
}

// Expiry is when the access token expires.
func (c Credentials) Expiry() time.Time {
	return c.SavedAt.Add(time.Duration(c.ExpiresIn) * time.Second)
}

// ErrSignedOut is a refresh token OpenAI no longer takes: the plan's owner
// disconnected kritika, the token went unused for thirty days, or another
// process refreshed the same session first. The sign-in is over until
// kritika chatgpt login runs again.
var ErrSignedOut = errors.New("chatgpt: signed out")

// signedOutCodes are the token endpoint's errors that end a session, as
// OpenAI's errors and recovery page lists them.
var signedOutCodes = []string{
	"invalid_grant", "invalid_refresh_token", "token_expired", "refresh_token_expired", "refresh_token_invalidated", "refresh_token_reused",
}

// Refresh trades c's refresh token for a new token set at tokenURL, with
// c's other fields kept and SavedAt set to now. A refresh token OpenAI
// refuses is ErrSignedOut; any other failure leaves c as it was, usable
// until its access token expires. Two refreshes of one session race its
// rotating token, so a caller serializes them.
func Refresh(ctx context.Context, client *http.Client, tokenURL string, c Credentials, now time.Time) (Credentials, error) {
	form := url.Values{
		"grant_type": {"refresh_token"}, "client_id": {c.ClientID}, "refresh_token": {c.RefreshToken}, "resource": {Resource},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return c, fmt.Errorf("chatgpt: refresh: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return c, fmt.Errorf("chatgpt: refresh: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return c, fmt.Errorf("chatgpt: refresh: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		var oauthErr struct {
			Code        string `json:"error"`
			Description string `json:"error_description"`
		}
		// A body that is not the OAuth error object is reported as it came.
		_ = json.Unmarshal(body, &oauthErr)
		if slices.Contains(signedOutCodes, oauthErr.Code) {
			return c, fmt.Errorf("%w: %s", ErrSignedOut, cmp.Or(oauthErr.Description, oauthErr.Code))
		}
		return c, fmt.Errorf("chatgpt: refresh: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var tok struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
		TokenType    string `json:"token_type"`
		ExpiresIn    int64  `json:"expires_in"`
		Scope        string `json:"scope"`
	}
	if err := json.Unmarshal(body, &tok); err != nil {
		return c, fmt.Errorf("chatgpt: refresh: decode: %w", err)
	}
	if tok.AccessToken == "" || tok.RefreshToken == "" {
		return c, errors.New("chatgpt: refresh: the token response has no tokens")
	}
	c.AccessToken, c.RefreshToken, c.ExpiresIn, c.SavedAt = tok.AccessToken, tok.RefreshToken, tok.ExpiresIn, now
	c.IDToken = cmp.Or(tok.IDToken, c.IDToken)
	c.TokenType = cmp.Or(tok.TokenType, c.TokenType)
	if tok.Scope != "" {
		c.Scopes = strings.Fields(tok.Scope)
	}
	return c, nil
}
